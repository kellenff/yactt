package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/search"
	"github.com/kellenff/yactt/internal/store"
)

// SearchArgs is the typed input for the search tool. Mirrors design §4.5.
type SearchArgs struct {
	Query string   `json:"query"`
	Scope string   `json:"scope"`
	Kind  []string `json:"kind"`
	Limit int      `json:"limit"`
}

// SearchSchema is the JSON Schema for `search` (MCP name `search`).
//
// We name the schema variable after the function so an external registry can
// iterate over them in lexicographic order without collision.
var SearchSchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "query": { "type": "string" },
    "scope": { "type": "string", "description": "Absolute path or repo alias" },
    "kind": {
      "type": "array",
      "items": { "enum": ["function", "method", "class", "module"] }
    },
    "limit": { "type": "integer", "default": 10 }
  },
  "required": ["query", "scope"],
  "additionalProperties": false
}`)

// SearchOutputSchema declares the structuredContent shape of search. The
// list of results is wrapped in an envelope object so the wire frame
// satisfies the MCP spec's "object" requirement on structuredContent.
// Truncation fields mirror the query_graph / detect_changes pattern
// (issue #33).
var SearchOutputSchema = json.RawMessage(`{
  "type": "object",
  "required": ["results"],
  "properties": {
    "results": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["score", "node"],
        "properties": {
          "score": { "type": "number" },
          "node":  { "type": "object" }
        }
      }
    },
    "truncated":  { "type": "boolean", "description": "True when more ranked matches existed than the requested limit." },
    "totalCount": { "type": "integer", "description": "Total ranked matches in the overscan window (limit*4). True corpus total may be higher when truncated=true." }
  },
  "additionalProperties": false
}`)

// Search returns a Handler that emits ranked (id, summary) results.
func Search(repo *store.Repo) func(ctx context.Context, args json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		var a SearchArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("invalid search args: %w", err)
		}
		if a.Query == "" {
			return nil, fmt.Errorf("search: query is required")
		}
		// Default the user-facing limit FIRST, then derive the
		// internal overscan. If the defaulting runs after the
		// overscan, `q.Limit = 0 * 4 = 0` triggers the search
		// package's own default and `overscanned[:a.Limit]`
		// truncates to [:0]. Issue #33 — order matters.
		if a.Limit <= 0 {
			a.Limit = 10
		}
		terms, err := splitQuery(a.Query)
		if err != nil {
			return nil, err
		}
		q := search.Query{
			Terms: terms,
			Limit: a.Limit * 4, // overscan so we can report truncation
			Scope: a.Scope,
		}
		if len(a.Kind) > 0 {
			q.Kind = make([]domain.NodeKind, len(a.Kind))
			for i, k := range a.Kind {
				q.Kind[i] = domain.NodeKind(strings.ToUpper(k))
			}
		}
		overscanned := search.Search(repo, q)
		truncated := len(overscanned) > a.Limit
		results := overscanned
		if truncated {
			results = results[:a.Limit]
		}
		// Wrap the slice in an envelope object so structuredContent on the
		// wire is a JSON object (the MCP contract). Matches are surfaced
		// under the `results` key, declared in SearchOutputSchema.
		// Truncation fields surface the cap signal so the agent knows
		// whether the list was capped (issue #33).
		return map[string]any{
			"results":    results,
			"truncated":  truncated,
			"totalCount": len(overscanned),
		}, nil
	}
}

// splitQuery tokenizes a search query into terms. Whitespace outside
// double-quoted spans separates terms; a double-quoted span becomes a
// single term verbatim. An unclosed quote is rejected with an error —
// the previous lenient behaviour silently folded the trailing span into
// one term and obscured typos like `foo "bar` (the user's intent was
// ambiguous between "one phrase" and "forgot the close"). The strict
// boundary makes the error visible at the handler, not in the ranking.
func splitQuery(s string) ([]string, error) {
	out := []string{}
	cur := strings.Builder{}
	inQuote := false
	for _, r := range s {
		switch {
		case r == '"':
			if inQuote {
				// Closing quote — flush a non-empty span.
				if cur.Len() > 0 {
					out = append(out, cur.String())
					cur.Reset()
				}
				inQuote = false
				continue
			}
			inQuote = true
		case r == ' ':
			if inQuote {
				cur.WriteRune(r)
				continue
			}
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if inQuote {
		return nil, fmt.Errorf("search: unclosed \" in query")
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out, nil
}
