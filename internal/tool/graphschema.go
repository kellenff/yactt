package tool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/store"
)

// GetGraphSchemaArgs is the typed boundary input for get_graph_schema. The
// tool currently takes no arguments; the struct exists so future knobs
// (e.g. `includeDeprecated`) can be added without breaking the wire shape.
type GetGraphSchemaArgs struct{}

// GraphSchemaResult is the structuredContent envelope for get_graph_schema.
// Every list is sourced from the domain package at handler time — the values
// are constants today, but routing through this envelope means a future
// version that derives them from a manifest file still produces the same
// shape on the wire.
type GraphSchemaResult struct {
	NodeKinds    []domain.NodeKind    `json:"nodeKinds"`
	EdgeKinds    []domain.EdgeKind    `json:"edgeKinds"`
	Layers       []domain.LayerName   `json:"layers"`
	DefaultEdges []domain.EdgeKind    `json:"defaultEdges"`
	CodeKinds    []domain.NodeKind    `json:"codeKinds"`
	Provenance   domain.Provenance    `json:"provenance"`
}

// GetGraphSchemaSchema is the JSON Schema for get_graph_schema. Empty args
// today; the explicit `additionalProperties:false` keeps the wire contract
// closed for the day we add a knob.
var GetGraphSchemaSchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {},
  "additionalProperties": false
}`)

// GetGraphSchemaOutputSchema declares the structuredContent shape of
// get_graph_schema.
var GetGraphSchemaOutputSchema = json.RawMessage(`{
  "type": "object",
  "required": ["nodeKinds", "edgeKinds", "layers", "defaultEdges", "codeKinds", "provenance"],
  "properties": {
    "nodeKinds":    { "type": "array", "items": { "type": "string" } },
    "edgeKinds":    { "type": "array", "items": { "type": "string" } },
    "layers":       { "type": "array", "items": { "type": "string" } },
    "defaultEdges": { "type": "array", "items": { "type": "string" } },
    "codeKinds":    { "type": "array", "items": { "type": "string" } },
    "provenance":   { "type": "object" }
  },
  "additionalProperties": false
}`)

// allNodeKinds mirrors the constants in the domain package as an explicit
// slice. There is no `AllNodeKinds` in domain (the constants are used
// piecemeal in the resolver), so we list them once here. Keeping the slice
// explicit makes "add a new NodeKind" a one-line edit in two places
// (domain + this slice) — no reflection, no codegen.
func allNodeKinds() []domain.NodeKind {
	return []domain.NodeKind{
		domain.KindRepo,
		domain.KindPackage,
		domain.KindFile,
		domain.KindFunction,
		domain.KindMethod,
		domain.KindClass,
		domain.KindModule,
	}
}

// allEdgeKinds mirrors domain.AllEdges but includes every edge kind, not
// just the default three. Some tools surface TESTS_OF / IMPORTS / OVERRIDES
// explicitly; agents writing graph queries need to know they exist.
func allEdgeKinds() []domain.EdgeKind {
	return []domain.EdgeKind{
		domain.EdgeCallers,
		domain.EdgeCallees,
		domain.EdgeTests,
		domain.EdgeOverrides,
		domain.EdgeImports,
		domain.EdgeTestsOf,
	}
}

func codeKinds() []domain.NodeKind {
	out := make([]domain.NodeKind, 0, 4)
	for _, k := range allNodeKinds() {
		if k.IsCode() {
			out = append(out, k)
		}
	}
	return out
}

// GetGraphSchema returns a Handler that emits the graph schema. The
// constructor takes *store.Repo to keep the wire-shape-test registration
// uniform across tools, even though the answer is repo-independent.
func GetGraphSchema(repo *store.Repo) func(ctx context.Context, args json.RawMessage) (any, error) {
	_ = repo // explicitly unused; reserved for future per-repo schema diffs.
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		var a GetGraphSchemaArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("invalid get_graph_schema args: %w", err)
		}
		return &GraphSchemaResult{
			NodeKinds:    allNodeKinds(),
			EdgeKinds:    allEdgeKinds(),
			Layers:       append([]domain.LayerName(nil), domain.AllLayerNames...),
			DefaultEdges: append([]domain.EdgeKind(nil), domain.AllEdges...),
			CodeKinds:    codeKinds(),
			Provenance:   domain.YacttProvenance(),
		}, nil
	}
}