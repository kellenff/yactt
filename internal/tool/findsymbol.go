package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/store"
)

// FindSymbolArgs is the typed input for find_symbol. Mirrors design §4.7.
type FindSymbolArgs struct {
	NamePath    string   `json:"name_path"`
	Scope       string   `json:"scope"`
	Kind        []string `json:"kind"`
	IncludeBody bool     `json:"include_body"`
	Limit       int      `json:"limit"`
}

// FindSymbolResult is one resolved match.
type FindSymbolResult struct {
	Node *domain.Node         `json:"node"`
	Body *domain.FunctionBody `json:"body,omitempty"`
}

// FindSymbolSchema is the JSON Schema for find_symbol.
var FindSymbolSchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "name_path": { "type": "string", "description": "Qualified name using '/' or '.' as the package separator (e.g. 'class/User/method/validate' or 'auth.Login'). Globs allowed on the final segment when the slash form is used." },
    "scope":     { "type": "string" },
    "kind":      { "type": "array", "items": { "enum": ["function","method","class","module"] } },
    "include_body": { "type": "boolean", "default": false },
    "limit":     { "type": "integer", "default": 20 }
  },
  "required": ["name_path"],
  "additionalProperties": false
}`)

// FindSymbolOutputSchema declares the structuredContent shape of
// find_symbol. The list of matches is wrapped in an envelope object so the
// wire frame satisfies the MCP spec's "object" requirement on
// structuredContent. Field name `symbols` matches get_symbols_overview.
// `suggestions` and `truncated` surface recovery hints and result-cap
// status (issue #33).
var FindSymbolOutputSchema = json.RawMessage(`{
  "type": "object",
  "required": ["symbols"],
  "properties": {
    "symbols": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["node"],
        "properties": {
          "node": { "type": "object", "properties": { "kind": { "type": "string", "description": "Domain kind (FUNCTION/METHOD/CLASS/MODULE). For the grammar-form and id-prefix mapping, call get_graph_schema and inspect kindMap." } } },
          "body": { "type": ["object", "null"] }
        }
      }
    },
    "suggestions": {
      "type": "array",
      "description": "When 'symbols' is empty, edit-distance matches against the index so the agent can self-correct. Omitted when the result set is non-empty.",
      "items": { "type": "string" }
    },
    "truncated":  { "type": "boolean", "description": "True when more matches existed than the requested limit." },
    "totalCount": { "type": "integer", "description": "Total matches before capping. Compare to len(symbols) to know how many were dropped." }
  },
  "additionalProperties": false
}`)

// FindSymbol returns a Handler that locates symbols by qualified name path.
// Glob patterns are translated to a small matching routine against the symbol
// index built at repo load.
func FindSymbol(repo *store.Repo) func(ctx context.Context, args json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		var a FindSymbolArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("invalid find_symbol args: %w", err)
		}
		if a.NamePath == "" {
			return nil, fmt.Errorf("find_symbol: name_path is required")
		}
		if a.Limit <= 0 {
			a.Limit = 20
		}
		segments := strings.Split(a.NamePath, "/")
		// Drop trailing empty from a trailing slash.
		if len(segments) > 0 && segments[len(segments)-1] == "" {
			segments = segments[:len(segments)-1]
		}
		// For MVP we treat the last segment as the symbol name pattern; prior
		// segments are package-path qualifiers we use as a scope hint.
		// Phase 2: walk the qualifier chain through receiver types.
		// ponytail: dotted form (e.g. "auth.Login") is the canonical Go name
		// and routes through splitNamePath so models don't have to know about
		// the slash form. Slash form keeps the kind-prefix + glob support.
		var pkgPrefix string
		var namePattern string
		if !strings.Contains(a.NamePath, "/") && strings.Contains(a.NamePath, ".") {
			pkgPrefix, namePattern = splitNamePath(a.NamePath)
		} else {
			if len(segments) > 1 {
				packageSegments := segments[:len(segments)-1]
				// Trim the leading "kind/type" segment if present (e.g. "class/User"
				// should match the "User" type without the "class" prefix).
				if len(packageSegments) > 0 && isKindSegment(packageSegments[0]) {
					packageSegments = packageSegments[1:]
				}
				pkgPrefix = strings.Join(packageSegments, ".")
			}
			namePattern = segments[len(segments)-1]
		}
		// Glob → match.
		matches := matchByNamePattern(repo, pkgPrefix, namePattern)
		totalFound := len(matches)
		hitLimit := totalFound > a.Limit
		out := make([]FindSymbolResult, 0, len(matches))
		for _, m := range matches {
			ent := entityFromSymbol(m.Sym, m.File, repo)
			nid := ent.ID()
			entry := FindSymbolResult{}
			if a.IncludeBody {
				body, err := store.BodyFor(repo, nid)
				if err == nil {
					entry.Body = body
				}
			}
			n, err := store.QuickNode(repo, nid)
			if err != nil || n == nil {
				n = &domain.Node{ID: nid, Kind: ent.DomainKind(), Name: m.Sym.Name}
			}
			entry.Node = n
			out = append(out, entry)
			if len(out) >= a.Limit {
				break
			}
		}
		// Wrap the slice in an envelope object so structuredContent on the
		// wire is a JSON object (the MCP contract). Matches are surfaced
		// under the `symbols` key, declared in FindSymbolOutputSchema.
		// On empty result, surface a "did you mean" suggestion list so
		// the agent can recover from a misspelling (issue #33).
		resp := map[string]any{
			"symbols":    out,
			"truncated":  hitLimit,
			"totalCount": totalFound,
		}
		if len(out) == 0 {
			if sugg := suggestNames(repo, a.NamePath, 3); len(sugg) > 0 {
				resp["suggestions"] = sugg
			}
		}
		return resp, nil
	}
}

// isKindSegment recognises the trailing kind segment in a name_path (e.g.
// "class", "fn", "meth", "module"). Used to strip it from the package hint.
func isKindSegment(s string) bool {
	switch s {
	case "fn", "class", "meth", "module", "function", "method", "type":
		return true
	}
	return false
}

// matchByNamePattern is a small matcher against the symbol index. Supports
// exact match, prefix/suffix globs (User*), and * wildcards.
func matchByNamePattern(repo *store.Repo, pkgPrefix, pattern string) []store.SymbolLookup {
	if pkgPrefix != "" {
		// First try the exact package; fall back to wildcard.
		entries := repo.Lookup(pkgPrefix, "")
		return filterByName(entries, pattern)
	}
	entries := repo.Lookup("", "")
	return filterByName(entries, pattern)
}

func filterByName(entries []store.SymbolLookup, pattern string) []store.SymbolLookup {
	out := []store.SymbolLookup{}
	for _, e := range entries {
		if matchName(e.Sym.Name, pattern) {
			out = append(out, e)
		}
	}
	return out
}

func matchName(name, pattern string) bool {
	ok, err := path.Match(pattern, name)
	if err != nil {
		// Bad pattern — fall back to equality.
		return name == pattern
	}
	return ok
}
