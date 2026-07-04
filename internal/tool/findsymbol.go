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
    "name_path": { "type": "string", "description": "Slash-separated path. Globs allowed (e.g. 'class/User*/method/*')." },
    "scope":     { "type": "string" },
    "kind":      { "type": "array", "items": { "enum": ["function","method","class","module"] } },
    "include_body": { "type": "boolean", "default": false },
    "limit":     { "type": "integer", "default": 20 }
  },
  "required": ["name_path"],
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
		var pkgPrefix string
		var namePattern string
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
		// Glob → match.
		matches := matchByNamePattern(repo, pkgPrefix, namePattern)
		out := make([]FindSymbolResult, 0, len(matches))
		for _, m := range matches {
			nid := symbolID(m.File, m.Sym, repo.Root())
			entry := FindSymbolResult{}
			if a.IncludeBody {
				body, err := store.BodyFor(repo, nid)
				if err == nil {
					entry.Body = body
				}
			}
			n, err := store.QuickNode(repo, nid)
			if err != nil || n == nil {
				n = &domain.Node{ID: nid, Kind: symbolKind(m.Sym), Name: m.Sym.Name}
			}
			entry.Node = n
			out = append(out, entry)
			if len(out) >= a.Limit {
				break
			}
		}
		return out, nil
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
