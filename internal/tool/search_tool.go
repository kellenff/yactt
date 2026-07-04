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
		q := search.Query{
			Terms: splitQuery(a.Query),
			Limit: a.Limit,
			Scope: a.Scope,
		}
		if len(a.Kind) > 0 {
			q.Kind = make([]domain.NodeKind, len(a.Kind))
			for i, k := range a.Kind {
				q.Kind[i] = domain.NodeKind(strings.ToUpper(k))
			}
		}
		results := search.Search(repo, q)
		return results, nil
	}
}

// splitQuery is a tiny whitespace + quoted-token splitter.
func splitQuery(s string) []string {
	out := []string{}
	cur := strings.Builder{}
	inQuote := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
		case r == ' ' && !inQuote:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}
