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
      "items": { "enum": ["calls","mentions","tests","overrides","all"] },
      "default": ["calls"]
    },
    "limit": { "type": "integer", "default": 100 }
  },
  "required": ["symbol"],
  "additionalProperties": false
}`)

// FindReferencingSymbolsOutputSchema declares the structuredContent shape
// of find_referencing_symbols. The list of references is wrapped in an
// envelope object so the wire frame satisfies the MCP spec's "object"
// requirement on structuredContent.
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
    }
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
		for _, k := range ekinds {
			switch k {
			case "callees":
				out = append(out, cast(scanCallees(repo, file, sym, a.Limit, p))...)
			case "callers":
				out = append(out, cast(scanCallers(repo, file, sym, a.Limit, p))...)
			case "tests":
				out = append(out, cast(scanTests(repo, file, sym, a.Limit, p))...)
			}
		}
		// Wrap the slice in an envelope object so structuredContent on the
		// wire is a JSON object (the MCP contract). References are
		// surfaced under the `references` key, declared in
		// FindReferencingSymbolsOutputSchema.
		return map[string]any{"references": out}, nil
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
	nid := symbolID(first.File, first.Sym, repo.Root())
	return id.Parse(nid)
}

// mapNodeEdgesKinds translates the symbol-friendly kinds to node_edges ones.
func mapNodeEdgesKinds(kinds []string) []string {
	if len(kinds) == 0 {
		return []string{"callers", "callees", "tests"}
	}
	out := []string{}
	for _, k := range kinds {
		switch k {
		case "calls":
			out = append(out, "callers")
		case "mentions":
			out = append(out, "callers")
		case "tests":
			out = append(out, "tests")
		case "overrides":
			// No MVP equivalent.
		case "all":
			return []string{"callers", "callees", "tests"}
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
