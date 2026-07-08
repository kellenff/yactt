// Package search implements symbol/fuzzy search against a *store.Repo.
//
// The design's `search` tool ranks candidate symbols by name + doc-comment
// match + path proximity (§4.5). For MVP we run a single tree-sitter-backed
// pass over the repo's symbol index: we already have (name, file, line range)
// for every declaration, so a name/doc match is cheap.
package search

import (
	"regexp"
	"sort"
	"strings"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/entity"
	"github.com/kellenff/yactt/internal/parser"
	"github.com/kellenff/yactt/internal/store"
)

// Result is one ranked match. Score is in [0, 1]; higher is better.
type Result struct {
	Score float64      `json:"score"`
	Node  SymbolResult `json:"node"`
}

// SymbolResult is the public per-match view: id, kind, summary, file context.
type SymbolResult struct {
	ID          string          `json:"id"`
	Kind        domain.NodeKind `json:"kind"`
	Summary     string          `json:"summary"`
	PathContext string          `json:"pathContext"`
}

// Query is the parsed search request. The caller (an MCP tool handler) is
// responsible for filling these fields from the wire-format request.
type Query struct {
	Terms    []string
	Kind     []domain.NodeKind
	Regex    *regexp.Regexp
	Limit    int
	Scope    string // repo root
	RepoPath string
}

// Search runs the search against r. scope is the absolute path to scope the
// search to; empty means whole repo.
func Search(r *store.Repo, q Query) []Result {
	if q.Limit <= 0 {
		q.Limit = 10
	}
	results := make([]Result, 0, 32)
	syms := r.SymbolsByPath()
	for path, decls := range syms {
		if q.Scope != "" && !strings.HasPrefix(path, q.Scope) {
			continue
		}
		pkg := store.PackagePath(r.Root(), path)
		// Bulk-fetch the doc for every symbol in this file in one CachedFile
		// call. DocComment-per-symbol would stat the file N times per path.
		docs := r.DocComments(path, decls)
		for _, sym := range decls {
			if !kindMatches(sym, q.Kind) {
				continue
			}
			score, matched := scoreSymbol(sym, docs[sym.Name], q, path, pkg)
			if !matched {
				continue
			}
			ent := entity.FromParser(sym, path, pkg)
			results = append(results, Result{
				Score: score,
				Node: SymbolResult{
					ID:          ent.ID(),
					Kind:        ent.DomainKind(),
					Summary:     ent.Summary(),
					PathContext: path,
				},
			})
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	if len(results) > q.Limit {
		results = results[:q.Limit]
	}
	return results
}

// kindMatches reports whether a parser.Symbol matches the requested kind set.
// An empty kind filter matches everything.
func kindMatches(s parser.Symbol, kf []domain.NodeKind) bool {
	if len(kf) == 0 {
		return true
	}
	dk := entity.FromParser(s, "", "").DomainKind()
	for _, k := range kf {
		if k == dk {
			return true
		}
	}
	return false
}

// scoreSymbol returns a score in [0, 1] and whether the symbol "matched" the
// query. Match-decision first: a symbol with no overlap on any signal is out.
//
//   - exact-name match → 0.9 floor
//   - substring match in name → ~0.6 + length-proximity bonus
//   - regex match on name OR doc-comment → 0.5
//   - path substring match → 0.2 bonus (path-proximity)
func scoreSymbol(s parser.Symbol, doc string, q Query, path, pkg string) (float64, bool) {
	if s.Name == "" {
		return 0, false
	}
	name := strings.ToLower(s.Name)
	docLow := strings.ToLower(doc)
	pathLow := strings.ToLower(path)

	var (
		matched bool
		score   float64
	)

	// Regex-only queries: the term loop would be a no-op, so evaluate the
	// regex directly against name / doc / path.
	if len(q.Terms) == 0 && q.Regex != nil {
		if q.Regex.MatchString(s.Name) || q.Regex.MatchString(docLow) || q.Regex.MatchString(pathLow) {
			matched = true
			score = 0.5
		}
		if !matched {
			return 0, false
		}
		return score, true
	}

	// Term loop: take the best signal across all terms.
	for _, term := range q.Terms {
		t := strings.TrimSpace(strings.ToLower(term))
		if t == "" {
			continue
		}
		switch {
		case name == t:
			matched = true
			if s := 0.9; s > score {
				score = s
			}
		case strings.Contains(name, t):
			matched = true
			s := 0.6 + 0.1*float64(len(t))/float64(len(name))
			if s > score {
				score = s
			}
		case strings.Contains(docLow, t):
			matched = true
			if s := 0.4; s > score {
				score = s
			}
		case strings.Contains(pathLow, t):
			matched = true
			if s := 0.25; s > score {
				score = s
			}
		case q.Regex != nil:
			if q.Regex.MatchString(s.Name) || q.Regex.MatchString(docLow) || q.Regex.MatchString(pathLow) {
				matched = true
				if s := 0.5; s > score {
					score = s
				}
			}
		}
	}

	if !matched {
		return 0, false
	}
	// Path-proximity bonus: when the query mentions a directory shared with
	// the candidate's file, nudge the score up.
	for _, term := range q.Terms {
		t := strings.TrimSpace(strings.ToLower(term))
		if t == "" {
			continue
		}
		if strings.Contains(pathLow, t) {
			score += 0.15
		}
	}
	if score > 1 {
		score = 1
	}
	return score, true
}

// buildID renders the canonical node ID for a search hit. Thin
// delegation through entity.FromParser — the canonical mapping lives
// in the entity package now, so id.For is bypassed here. Any future
// kind-table changes happen in one place.
func buildID(s parser.Symbol, pkg string) string {
	return entity.FromParser(s, "", pkg).ID()
}

// Pattern is a structured search pattern used by find_code. It supports regex
// (default) and AST patterns (Phase 2 — wired up to a tree-sitter query
// runner, interface provided here).
type Pattern struct {
	Kind     string // "regex" | "tree_sitter"
	Text     string
	Compiled *regexp.Regexp
}

// FindCode matches `pattern` across every file's source body, returning one
// match per occurrence with file + line range + snippet.
type CodeMatch struct {
	File      string           `json:"file"`
	LineRange domain.LineRange `json:"range"`
	Snippet   string           `json:"snippet"`
}

// FindCodeResults bundles the matches with optional context.
type FindCodeResults struct {
	Matches []CodeMatch        `json:"matches"`
	Context map[string]Context `json:"context,omitempty"`
}

type Context struct {
	NodeID         string           `json:"nodeId"`
	Kind           domain.NodeKind  `json:"kind"`
	Summary        string           `json:"summary"`
	EnclosingRange domain.LineRange `json:"enclosingRange"`
}
