package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kellenff/yactt/internal/id"
	"github.com/kellenff/yactt/internal/parser"
	"github.com/kellenff/yactt/internal/store"
)

// FindReferencingSymbolsArgs is the typed input for find_referencing_symbols.
// Mirrors design §4.10.
type FindReferencingSymbolsArgs struct {
	Symbol string   `json:"symbol"`
	Kinds  []string `json:"kinds"`
	Limit  int      `json:"limit"`
}

// FindReferencingSymbolsResult is one typed edge similar to node_edges.
type FindReferencingSymbolsResult NodeEdgesResult

// FindReferencingSymbolsSchema is the JSON Schema for find_referencing_symbols.
var FindReferencingSymbolsSchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "symbol": { "type": "string", "description": "Node ID or name_path." },
    "kinds": {
      "type": "array",
      "items": { "enum": ["callers","callees","tests","overrides","imports"] },
      "default": ["callers","callees","tests"],
      "description": "Edge kinds to follow. Same vocabulary as node_edges.kinds. For transitive (>1 hop) chains use query_graph instead."
    },
    "limit": { "type": "integer", "default": 100 }
  },
  "required": ["symbol"],
  "additionalProperties": false
}`)

// FindReferencingSymbolsOutputSchema declares the structuredContent shape
// of find_referencing_symbols. The list of references is wrapped in an
// envelope object so the wire frame satisfies the MCP spec's "object"
// requirement on structuredContent. Truncation fields mirror the
// query_graph / detect_changes pattern (issue #33).
var FindReferencingSymbolsOutputSchema = json.RawMessage(`{
  "type": "object",
  "required": ["references"],
  "properties": {
    "references": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["edgeKind", "targetId", "location", "confidence", "provenance"],
        "properties": {
          "edgeKind":      { "type": "string" },
          "targetId":      { "type": "string" },
          "targetKind":    { "type": "string" },
          "targetSummary": { "type": "string" },
          "location":      { "type": "object" },
          "confidence":    { "type": "number" },
          "provenance":    { "type": "object" }
        }
      }
    },
    "truncated":   { "type": "boolean", "description": "True when one or more requested kinds hit the per-kind limit and more references existed." },
    "totalCount":  { "type": "integer", "description": "Total references found across all requested kinds before any capping. Compare to len(references) to know how many were dropped." }
  },
  "additionalProperties": false
}`)

// FindReferencingSymbols returns a Handler that resolves `symbol` to an ID
// and forwards to node_edges. Per design §4.10 this is the symbol-addressed
// alias for node_edges.
func FindReferencingSymbols(repo *store.Repo) func(ctx context.Context, args json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		var a FindReferencingSymbolsArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("invalid find_referencing_symbols args: %w", err)
		}
		if a.Symbol == "" {
			return nil, fmt.Errorf("find_referencing_symbols: symbol is required")
		}
		if a.Limit <= 0 {
			a.Limit = 100
		}
		nodeID, err := resolveSymbolToID(repo, a.Symbol)
		if err != nil {
			return nil, err
		}
		// Map internal kinds to the node_edges kinds.
		ekinds := mapNodeEdgesKinds(a.Kinds)
		// Invoke the node_edges handler logic directly (avoid JSON
		// round-tripping).
		file, sym, ok, lerr := repo.LocateSymbol(nodeID)
		if lerr != nil || !ok {
			return nil, fmt.Errorf("find_referencing_symbols: cannot locate %s", nodeID.String())
		}
		p := prov()
		out := []FindReferencingSymbolsResult{}
		totalFound := 0
		hitLimit := false
		for _, k := range ekinds {
			// Larger per-kind limit (4x the user cap) so the
			// truncation flag is well-defined: if a kind produced
			// more than a.Limit references, we report truncated=true
			// AND totalCount reflects the pre-cap count.
			scanLimit := a.Limit * 4
			var raw []NodeEdgesResult
			switch k {
			case "callees":
				raw = scanCallees(repo, file, sym, scanLimit, p)
			case "callers":
				raw = scanCallers(repo, file, sym, scanLimit, p)
			case "tests":
				raw = scanTests(repo, file, sym, scanLimit, p)
			case "imports":
				raw = scanImports(repo, file, sym, scanLimit, p)
			case "overrides":
				raw = scanOverrides(repo, file, sym, scanLimit, p)
			}
			totalFound += len(raw)
			if len(raw) > a.Limit {
				hitLimit = true
				raw = raw[:a.Limit]
			}
			out = append(out, cast(raw)...)
		}
		// Wrap the slice in an envelope object so structuredContent on the
		// wire is a JSON object (the MCP contract). References are
		// surfaced under the `references` key, declared in
		// FindReferencingSymbolsOutputSchema. Truncation fields
		// mirror the query_graph / detect_changes pattern.
		return map[string]any{
			"references": out,
			"truncated":  hitLimit,
			"totalCount": totalFound,
		}, nil
	}
}

// resolveSymbolToID accepts either a node ID or a name_path. When given a
// name_path, we delegate to the find_symbol machinery and pick the first
// match.
func resolveSymbolToID(repo *store.Repo, symbol string) (id.ID, error) {
	if strings.Contains(symbol, ":") {
		return id.Parse(symbol)
	}
	matches := matchByNamePattern(repo, "", symbol)
	if len(matches) == 0 {
		return id.ID{}, fmt.Errorf("no symbol matches %q", symbol)
	}
	first := matches[0]
	nid := entityFromSymbol(first.Sym, first.File, repo).ID()
	return id.Parse(nid)
}

// mapNodeEdgesKinds is a thin pass-through — find_referencing_symbols now
// shares the same vocabulary as node_edges.kinds (callers, callees, tests,
// overrides, imports). Kept as a function so any future translation lives
// in one place.
func mapNodeEdgesKinds(kinds []string) []string {
	if len(kinds) == 0 {
		return []string{"callers", "callees", "tests"}
	}
	out := []string{}
	for _, k := range kinds {
		switch k {
		case "callers", "callees", "tests", "overrides", "imports":
			out = append(out, k)
		}
	}
	return out
}

// cast converts []NodeEdgesResult → []FindReferencingSymbolsResult. We use a
// dedicated alias so the JSON wire shape can evolve independently.
func cast(in []NodeEdgesResult) []FindReferencingSymbolsResult {
	out := make([]FindReferencingSymbolsResult, len(in))
	for i, r := range in {
		out[i] = FindReferencingSymbolsResult(r)
	}
	return out
}

// Compile-time guarantee that we use the parser import (Phase 2 will need it
// for typed symbol dispatch).
var _ = (*parser.Symbol)(nil)
