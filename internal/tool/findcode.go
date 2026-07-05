package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/parser"
	"github.com/kellenff/yactt/internal/store"
)

// FindCodeArgs is the typed input for find_code. Mirrors design §4.9.
type FindCodeArgs struct {
	Pattern        string `json:"pattern"`
	PatternKind    string `json:"pattern_kind"`
	Scope          string `json:"scope"`
	FileFilter     string `json:"file_filter"`
	IncludeContext bool   `json:"include_context"`
	Limit          int    `json:"limit"`
}

// FindCodeMatch is one hit across the corpus.
type FindCodeMatch struct {
	File      string           `json:"file"`
	LineRange domain.LineRange `json:"range"`
	Snippet   string           `json:"snippet"`
	Context   *FindCodeContext `json:"context,omitempty"`
}

// FindCodeContext provides enclosing function/class metadata.
type FindCodeContext struct {
	NodeID         string           `json:"nodeId"`
	Kind           domain.NodeKind  `json:"kind"`
	Summary        string           `json:"summary"`
	EnclosingRange domain.LineRange `json:"enclosingRange"`
}

// FindCodeSchema is the JSON Schema for find_code. Mirrors §4.9.
var FindCodeSchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "pattern":         { "type": "string" },
    "pattern_kind":    { "enum": ["regex", "tree_sitter"], "default": "regex" },
    "scope":           { "type": "string" },
    "file_filter":     { "type": "string", "description": "Glob over file paths, e.g. 'src/auth/**/*.go'." },
    "include_context": { "type": "boolean", "default": true },
    "limit":           { "type": "integer", "default": 50 }
  },
  "required": ["pattern"],
  "additionalProperties": false
}`)

// FindCodeOutputSchema declares the structuredContent shape of find_code.
// The list of matches is wrapped in an envelope object so the wire frame
// satisfies the MCP spec's "object" requirement on structuredContent. See
// internal/mcp/server.go:validateOutputSchema for the registration check.
var FindCodeOutputSchema = json.RawMessage(`{
  "type": "object",
  "required": ["matches"],
  "properties": {
    "matches": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["file", "range", "snippet"],
        "properties": {
          "file":    { "type": "string" },
          "range":   { "type": "object", "required": ["start", "end"], "properties": { "start": { "type": "integer" }, "end": { "type": "integer" } } },
          "snippet": { "type": "string" },
          "context": { "type": ["object", "null"] }
        }
      }
    }
  },
  "additionalProperties": false
}`)

// FindCode returns a Handler that emits AST-aware (or regex) matches.
//
// MVP supports regex (tree_sitter pattern is wired up via the Language
// interface but the runner itself is Phase 2 — see find_code §4.9).
func FindCode(repo *store.Repo) func(ctx context.Context, args json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		var a FindCodeArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("invalid find_code args: %w", err)
		}
		if a.Pattern == "" {
			return nil, fmt.Errorf("find_code: pattern is required")
		}
		if a.PatternKind == "" {
			a.PatternKind = "regex"
		}
		if a.Limit <= 0 {
			a.Limit = 50
		}
		switch a.PatternKind {
		case "regex":
			rx, err := regexp.Compile(a.Pattern)
			if err != nil {
				return nil, fmt.Errorf("find_code: invalid regex: %w", err)
			}
			// Wrap the slice in an envelope object so structuredContent on
			// the wire is a JSON object (the MCP contract). Matches are
			// surfaced under the `matches` key, declared in
			// FindCodeOutputSchema.
			return map[string]any{"matches": findCodeRegex(repo, a.Scope, a.FileFilter, rx, a.IncludeContext, a.Limit)}, nil
		case "tree_sitter":
			// Boundary validation: pre-compile against every supported
			// grammar. If all grammars reject the pattern, surface an
			// error here rather than failing mid-iteration on the first
			// file. Mirrors the regex path's regexp.Compile up-front.
			if err := validateTreeSitterPattern(a.Pattern); err != nil {
				return nil, err
			}
			matches, err := findCodeTreeSitter(repo, a.Scope, a.FileFilter, a.Pattern, a.IncludeContext, a.Limit)
			if err != nil {
				return nil, err
			}
			return map[string]any{"matches": matches}, nil
		default:
			return nil, fmt.Errorf("find_code: unknown pattern_kind %q", a.PatternKind)
		}
	}
}

// findCodeRegex scans each matching file with a compiled regex. Enclosing
// context (when requested) is the closest enclosing function/class via the
// file's symbols list.
func findCodeRegex(repo *store.Repo, scope, fileFilter string, rx *regexp.Regexp, withContext bool, limit int) []FindCodeMatch {
	out := []FindCodeMatch{}
	for _, p := range repo.Files() {
		if scope != "" && !strings.HasPrefix(p, scope) {
			continue
		}
		if fileFilter != "" {
			ok, err := path.Match(fileFilter, p)
			if err == nil && !ok {
				continue
			}
		}
		f, err := repo.CachedFile(p)
		if err != nil {
			continue
		}
		lines, _ := f.Lines()
		for i, raw := range lines {
			line := string(raw)
			idxs := rx.FindAllStringIndex(line, -1)
			if len(idxs) == 0 {
				continue
			}
			match := FindCodeMatch{
				File:      p,
				LineRange: domain.LineRange{Start: i, End: i + 1},
				Snippet:   trimSnippet(line),
			}
			if withContext {
				ctx := enclosingContext(repo, p, i)
				if ctx != nil {
					match.Context = ctx
				}
			}
			out = append(out, match)
			if len(out) >= limit {
				return out
			}
		}
	}
	return out
}

func trimSnippet(s string) string {
	const max = 200
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// validateTreeSitterPattern pre-compiles `pattern` against every
// supported grammar in parser.All(). If the pattern is rejected by ALL
// grammars, it's genuinely invalid (not just language-specific) and we
// surface a user-readable error before any repo access. Otherwise we
// return nil — the per-file runner will re-compile per language and
// surface language-specific failures at iteration time.
//
// ponytail: tree-sitter compile is microseconds; the per-call cost is
// negligible compared to the regex compile already paid by the regex
// path. Mirrors the regex path's "compile up-front" pattern so
// boundary tests (repo=nil) get the same error contract.
func validateTreeSitterPattern(pattern string) error {
	var firstErr error
	for _, lang := range parser.All() {
		q, err := sitter.NewQuery([]byte(pattern), lang.Grammar())
		if err == nil {
			q.Close()
			return nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	if firstErr == nil {
		// No grammars wired — degenerate case. Surface a clear error
		// rather than letting the runner silently no-op.
		return fmt.Errorf("find_code: no tree-sitter grammars registered; tree_sitter pattern_kind is unavailable")
	}
	return fmt.Errorf("find_code: invalid tree-sitter query: %w", firstErr)
}

// findCodeTreeSitter runs `pattern` (an S-expression tree-sitter query)
// against every parsed file in scope. Tree-sitter predicates
// (#eq?, #match?, …) are evaluated via cursor.FilterPredicates per
// match — a match whose predicates reject it is dropped silently. One
// FindCodeMatch is emitted per remaining capture, holding the wire
// shape parity with the regex path's "one hit per line".
//
// The query is compiled once per language (queries are grammar-bound);
// each file's parse tree is walked by a freshly-created QueryCursor.
//
// ponytail: capture iteration uses NextCapture rather than NextMatch
// because the smacker binding returns empty Captures from NextMatch
// when predicates are present in the query — empirically observed,
// not derivable from the docs. NextCapture populates Captures
// correctly even with predicates; we track match boundaries via
// m.ID and call FilterPredicates once per match. Rejection in
// smacker is signalled by an empty Captures slice on the returned
// QueryMatch (NOT by nil), so we guard on len == 0.
func findCodeTreeSitter(repo *store.Repo, scope, fileFilter, pattern string, withContext bool, limit int) ([]FindCodeMatch, error) {
	out := []FindCodeMatch{}
	// Per-call compile cache. One entry per *sitter.Language pointer —
	// upstream Language implementations cache the singleton in Grammar(),
	// so pointer equality is a safe map key.
	queries := map[*sitter.Language]*sitter.Query{}
	defer func() {
		for _, q := range queries {
			q.Close()
		}
	}()

	for _, p := range repo.Files() {
		if scope != "" && !strings.HasPrefix(p, scope) {
			continue
		}
		if fileFilter != "" {
			ok, err := path.Match(fileFilter, p)
			if err == nil && !ok {
				continue
			}
		}
		lang, lerr := parser.Detect(p)
		if lerr != nil {
			// Unsupported extension — same behaviour as the regex path
			// silently skipping unknown files.
			continue
		}
		grammar := lang.Grammar()
		q, ok := queries[grammar]
		if !ok {
			newQ, qerr := sitter.NewQuery([]byte(pattern), grammar)
			if qerr != nil {
				return nil, fmt.Errorf("find_code: invalid tree-sitter query: %w", qerr)
			}
			queries[grammar] = newQ
			q = newQ
		}
		f, ferr := repo.CachedFile(p)
		if ferr != nil {
			continue
		}
		cursor := sitter.NewQueryCursor()
		cursor.Exec(q, f.Root)
		// Predicate state per cursor. curMatchID tracks the most
		// recently evaluated match; curCaptures is non-nil only
		// when the match's predicates accepted.
		var (
			curMatchID  uint32 = ^uint32(0) // sentinel: no match seen yet
			curCaptures []sitter.QueryCapture
		)
		for {
			m, capIdx, ok := cursor.NextCapture()
			if !ok {
				break
			}
			if m.ID != curMatchID {
				// First capture of a new match — apply predicates.
				curMatchID = m.ID
				filtered := cursor.FilterPredicates(m, f.Bytes)
				if filtered == nil || len(filtered.Captures) == 0 {
					// Predicate rejected (smacker signals rejection
					// via empty Captures, not nil match).
					curCaptures = nil
				} else {
					curCaptures = filtered.Captures
				}
			}
			if curCaptures == nil {
				continue
			}
			capIdxInt := int(capIdx)
			if capIdxInt >= len(curCaptures) {
				continue
			}
			node := curCaptures[capIdxInt].Node
			if node == nil {
				continue
			}
			startRow := int(node.StartPoint().Row)
			endRow := int(node.EndPoint().Row) + 1
			if endRow <= startRow {
				endRow = startRow + 1
			}
			body, _ := f.Slice(domain.LineRange{Start: startRow, End: endRow})
			match := FindCodeMatch{
				File:      p,
				LineRange: domain.LineRange{Start: startRow, End: endRow},
				Snippet:   trimSnippet(body),
			}
			if withContext {
				if ctx := enclosingContext(repo, p, startRow); ctx != nil {
					match.Context = ctx
				}
			}
			out = append(out, match)
			if len(out) >= limit {
				cursor.Close()
				return out, nil
			}
		}
		cursor.Close()
	}
	return out, nil
}

// enclosingContext finds the smallest declaration in the file that contains
// `line`.
func enclosingContext(repo *store.Repo, path string, line int) *FindCodeContext {
	for _, s := range repo.Symbols(path) {
		if s.StartRow <= line && line < s.EndRow {
			return &FindCodeContext{
				NodeID:         symbolID(path, s, repo.Root()),
				Kind:           symbolKind(s),
				Summary:        symbolSummary(s),
				EnclosingRange: domain.LineRange{Start: s.StartRow, End: s.EndRow},
			}
		}
	}
	return nil
}
