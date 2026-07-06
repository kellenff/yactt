package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/id"
	"github.com/kellenff/yactt/internal/store"
)

// GetCodeSnippetArgs is the typed boundary input for get_code_snippet.
// Either `id` (stable id) or `name_path` (qualified name like "auth.Login"
// or "auth/Login") must be supplied. When both are set, `id` wins; when an
// id-shaped string is passed as `name_path`, it's parsed as an id directly.
type GetCodeSnippetArgs struct {
	ID            string `json:"id"`
	NamePath      string `json:"name_path"`
	Range         []int  `json:"range"`
	IncludeTrivia bool   `json:"include_trivia"`
}

// GetCodeSnippetResult mirrors NodeSourceResult with the addition of `id`
// and `kind` so callers don't need a second lookup to discover which
// symbol they got. `Ambiguous` is set (non-zero) iff `name_path` matched
// more than one declaration; the first match is still returned so the
// snippet is useful even when the caller can't disambiguate.
type GetCodeSnippetResult struct {
	ID         string            `json:"id"`
	Kind       domain.NodeKind   `json:"kind"`
	Text       string            `json:"text"`
	LineRange  domain.LineRange  `json:"lines"`
	Encoding   string            `json:"encoding"`
	Ambiguous  int               `json:"ambiguous,omitempty"`
	Provenance domain.Provenance `json:"provenance"`
}

// GetCodeSnippetSchema mirrors NodeSourceSchema with the additional
// `name_path` argument. Either `id` or `name_path` is required.
var GetCodeSnippetSchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "id": {
      "type": "string",
      "description": "Stable node id, e.g. fn:auth.Login or meth:auth.Session.Attempt."
    },
    "name_path": {
      "type": "string",
      "description": "Qualified name, e.g. auth.Login or auth/Login. Resolved via the symbol index."
    },
    "range": {
      "type": "array",
      "items": { "type": "integer" },
      "description": "[startLine, endLine] (1-based, inclusive start, exclusive end)."
    },
    "include_trivia": { "type": "boolean", "default": false }
  },
  "anyOf": [
    { "required": ["id"] },
    { "required": ["name_path"] }
  ],
  "additionalProperties": false
}`)

// GetCodeSnippetOutputSchema declares the structuredContent shape.
var GetCodeSnippetOutputSchema = json.RawMessage(`{
  "type": "object",
  "required": ["id", "kind", "text", "lines", "encoding", "provenance"],
  "properties": {
    "id":         { "type": "string" },
    "kind":       { "type": "string" },
    "text":       { "type": "string" },
    "lines":      { "type": "object" },
    "encoding":   { "type": "string" },
    "ambiguous":  { "type": "integer" },
    "provenance": { "type": "object" }
  },
  "additionalProperties": false
}`)

// GetCodeSnippet returns a Handler that emits a source slice for a node
// resolved either by stable `id` or by qualified `name_path`. The
// combined-input affordance halves the find_symbol+node_source dance for
// the common "show me the code for X" question.
func GetCodeSnippet(repo *store.Repo) func(ctx context.Context, args json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		var a GetCodeSnippetArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("invalid get_code_snippet args: %w", err)
		}
		if a.ID == "" && a.NamePath == "" {
			return nil, fmt.Errorf("get_code_snippet: id or name_path is required")
		}

		// Prefer the stable id when provided. The name_path branch only
		// runs when `id` is empty.
		if a.ID != "" {
			nodeID, err := id.Parse(a.ID)
			if err != nil {
				return nil, fmt.Errorf("get_code_snippet: %w", err)
			}
			res, err := readSource(repo, nodeID, a.Range, a.IncludeTrivia)
			if err != nil {
				return nil, err
			}
			return res, nil
		}

		nodeID, ambiguous, err := resolveNamePath(repo, a.NamePath)
		if err != nil {
			return nil, err
		}
		res, err := readSource(repo, nodeID, a.Range, a.IncludeTrivia)
		if err != nil {
			return nil, err
		}
		res.Ambiguous = ambiguous
		return res, nil
	}
}

// resolveNamePath converts a qualified name like "auth.Login" or
// "auth/Login" into a stable node id. Two-stage resolution:
//
//  1. Try `id.Parse` directly — an id-shaped string ("fn:auth.Login",
//     "meth:auth.Session.Attempt", ...) round-trips unchanged.
//  2. Fall back to a (pkg, name) lookup against the symbol index. Split the
//     arg on the LAST '.' or '/' so "a.b.C" → pkg "a.b", name "C". Then
//     try the lookup at that key. If it misses (common for methods: the
//     index stores `meth:auth.User.Greet` under pkg="auth", not pkg="auth.User"),
//     drop the trailing segment from pkg and retry. The resolver keeps a
//     mirror bucket under ("", name) so a pkg-less retry still hits.
//
// Returns the first match's id and the number of matches (zero is the error
// path; >1 is the ambiguity hint).
func resolveNamePath(repo *store.Repo, namePath string) (id.ID, int, error) {
	if nodeID, err := id.Parse(namePath); err == nil {
		return nodeID, 0, nil
	}

	pkg, name := splitNamePath(namePath)
	if name == "" {
		return id.ID{}, 0, fmt.Errorf("get_code_snippet: cannot resolve %q (no name component after split)", namePath)
	}

	// Try the split as-is first, then progressively shorten pkg.
	candidates := []string{pkg, ""}
	if idx := strings.LastIndexAny(pkg, "./"); idx > 0 {
		candidates = append([]string{pkg[:idx]}, candidates...)
	}

	var first *store.SymbolLookup
	var matchCount int
	for _, tryPkg := range candidates {
		hits := repo.Lookup(tryPkg, name)
		if len(hits) == 0 {
			continue
		}
		first = &hits[0]
		matchCount = len(hits)
		break
	}
	if first == nil {
		return id.ID{}, 0, fmt.Errorf("get_code_snippet: cannot locate %q", namePath)
	}
	nodeIDStr := symbolID(first.File, first.Sym, repo.Root())
	nodeID, err := id.Parse(nodeIDStr)
	if err != nil {
		return id.ID{}, 0, fmt.Errorf("get_code_snippet: resolved %q to %q but id is invalid: %w", namePath, nodeIDStr, err)
	}
	return nodeID, matchCount, nil
}

// splitNamePath splits "auth.Login" or "auth/Login" into ("auth", "Login").
// Pure splitting — no symbol resolution. Empty parts are skipped so
// ".Login" → ("", "Login") falls through to the name-only lookup.
func splitNamePath(s string) (pkg, name string) {
	for i := len(s) - 1; i >= 0; i-- {
		if c := s[i]; c == '.' || c == '/' {
			return strings.TrimPrefix(s[:i], "./"), s[i+1:]
		}
	}
	return "", s
}

// readSource materializes the source slice for a resolved id. Shared by
// the id branch and the name_path branch. Bounds the line range to the
// symbol when the caller asks for a slice outside it (so a typo in
// `range` can't escape the symbol).
func readSource(repo *store.Repo, nodeID id.ID, rng []int, includeTrivia bool) (*GetCodeSnippetResult, error) {
	file, sym, ok, lerr := repo.LocateSymbol(nodeID)
	if lerr != nil || !ok {
		return nil, fmt.Errorf("get_code_snippet: cannot locate %s", nodeID)
	}
	f, ferr := repo.CachedFile(file)
	if ferr != nil {
		return nil, ferr
	}
	startLine, endLine := sym.StartRow, sym.EndRow
	if len(rng) == 2 {
		startLine, endLine = rng[0], rng[1]
		if startLine < sym.StartRow {
			startLine = sym.StartRow
		}
		if endLine > sym.EndRow {
			endLine = sym.EndRow
		}
	}
	r := domain.LineRange{Start: startLine, End: endLine}
	var body string
	var err error
	if includeTrivia {
		body, err = f.SliceWithTrivia(r)
	} else {
		body, err = f.Slice(r)
	}
	if err != nil {
		return nil, err
	}
	return &GetCodeSnippetResult{
		ID:         nodeID.String(),
		Kind:       symbolKind(sym),
		Text:       body,
		LineRange:  r,
		Encoding:   "utf-8",
		Provenance: *prov(),
	}, nil
}