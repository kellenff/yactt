// Package contract holds boundary tests for each MCP tool.
//
// Contract tests verify four things:
//
//  1. The JSON Schemas (input + output) returned in `tools/list`
//     round-trip through encoding/json.
//  2. Each input schema declares a `required` array (no all-optional tools).
//  3. Required-field violations are rejected by the handler before any repo
//     access — the boundary catches missing args without ever reaching the
//     domain logic.
//  4. Each tool declares an OutputSchema with a top-level `type:"object"`,
//     satisfying the MCP "structuredContent is a JSON object" contract.
//
// Success-path coverage (handler against a real fixture) lives in
// tests/acceptance. Splitting the two keeps the contract test fast and free
// of repo load cost; keeps the acceptance test laser-focused on shape.
package contract_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kellenff/yactt/internal/tool"
)

// everySchema is the set of (name → input-schema) entries that the registry
// should publish. Keeping this list in one place guarantees that any new
// tool adds both a schema constant AND a contract test entry — there's no
// way to add a tool without touching this table.
func everySchema(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	return map[string]json.RawMessage{
		"tree_overview":            tool.TreeOverviewSchema,
		"node_get":                 tool.GetNodeSchema,
		"node_source":              tool.NodeSourceSchema,
		"node_edges":               tool.NodeEdgesSchema,
		"search":                   tool.SearchSchema,
		"edit_impact":              tool.EditImpactSchema,
		"find_symbol":              tool.FindSymbolSchema,
		"get_symbols_overview":     tool.GetSymbolsOverviewSchema,
		"find_code":                tool.FindCodeSchema,
		"find_referencing_symbols": tool.FindReferencingSymbolsSchema,
		"persisted_query":          tool.PersistedQuerySchema,
	}
}

// everyOutputSchema mirrors everySchema for the structuredContent shape.
// The contract that drives MCP's "object" requirement on structuredContent
// is checked here: every output schema must declare `type:"object"` at the
// root. A bare slice or non-object schema at this layer would be rejected
// at register time (see internal/mcp/server.go:validateOutputSchema) and
// is therefore impossible to ship — but this test catches accidental
// drops/typos in the schema constants themselves.
func everyOutputSchema(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	return map[string]json.RawMessage{
		"tree_overview":            tool.TreeOverviewOutputSchema,
		"node_get":                 tool.GetNodeOutputSchema,
		"node_source":              tool.NodeSourceOutputSchema,
		"node_edges":               tool.NodeEdgesOutputSchema,
		"search":                   tool.SearchOutputSchema,
		"edit_impact":              tool.EditImpactOutputSchema,
		"find_symbol":              tool.FindSymbolOutputSchema,
		"get_symbols_overview":     tool.GetSymbolsOverviewOutputSchema,
		"find_code":                tool.FindCodeOutputSchema,
		"find_referencing_symbols": tool.FindReferencingSymbolsOutputSchema,
		"persisted_query":          tool.PersistedQueryOutputSchema,
	}
}

// TestSchemasAreValidJSON verifies every schema round-trips through json.
func TestSchemasAreValidJSON(t *testing.T) {
	for name, raw := range everySchema(t) {
		t.Run(name, func(t *testing.T) {
			var out map[string]any
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatalf("schema %s: invalid JSON: %v", name, err)
			}
			if out["type"] != "object" {
				t.Fatalf("schema %s: top-level type should be 'object'", name)
			}
		})
	}
}

// TestSchemasRequiredFields ensures each schema has at least one required field
// in its `required` array — a tool that accepts no inputs is suspicious.
func TestSchemasRequiredFields(t *testing.T) {
	for name, raw := range everySchema(t) {
		t.Run(name, func(t *testing.T) {
			var out map[string]any
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatalf("schema %s: %v", name, err)
			}
			req, _ := out["required"].([]any)
			if len(req) == 0 {
				t.Fatalf("schema %s: required[] is empty", name)
			}
		})
	}
}

// TestOutputSchemasAreObjects pins the MCP contract on structuredContent:
// every tool's OutputSchema must declare a top-level `type:"object"`. If a
// new tool is added without an OutputSchema (or with a non-object one),
// this test fails before the server ever boots. Mirrors the in-server
// guard (server.go:validateOutputSchema) so a regression on either side
// is caught by the corresponding test.
func TestOutputSchemasAreObjects(t *testing.T) {
	for name, raw := range everyOutputSchema(t) {
		t.Run(name, func(t *testing.T) {
			if len(raw) == 0 {
				t.Fatalf("output schema %s: empty (every tool must declare an OutputSchema)", name)
			}
			var probe struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(raw, &probe); err != nil {
				t.Fatalf("output schema %s: invalid JSON: %v", name, err)
			}
			if probe.Type != "object" {
				t.Fatalf("output schema %s: top-level type = %q, want \"object\" (handlers must wrap slices in an envelope object)", name, probe.Type)
			}
		})
	}
}

// TestInputOutputSchemasCoverSameTools asserts that every name present
// in everySchema also appears in everyOutputSchema. The two tables must
// stay in lockstep — a tool with an input schema but no output schema
// would silently slip through TestSchemasAreValidJSON and blow up at
// registration. This test catches the drift cheaply.
func TestInputOutputSchemasCoverSameTools(t *testing.T) {
	in := everySchema(t)
	out := everyOutputSchema(t)
	if len(in) != len(out) {
		t.Fatalf("input schema count (%d) != output schema count (%d); keys: in=%v out=%v", len(in), len(out), keys(in), keys(out))
	}
	for name := range in {
		if _, ok := out[name]; !ok {
			t.Errorf("tool %q has input schema but no output schema", name)
		}
	}
}

// keys is a tiny helper to format a map[string]X for a t.Fatalf.
func keys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// boundaryTest rejects handlers that don't fail at the boundary when args are
// missing or malformed. We pass nil repos because every handler should reject
// bad args *before* touching the repo. Handlers that escape this check need
// to be fixed at the handler level, not the test.
type boundaryTest struct {
	name string
	args string
	want string // substring expected in the error message
}

// perToolArgBoundary verifies each tool refuses bad input without crashing
// when the repo is nil — i.e. validation happens at the boundary.
func perToolArgBoundary(t *testing.T, name string, h func(ctx context.Context, args json.RawMessage) (any, error), tests []boundaryTest) {
	t.Helper()
	for _, tc := range tests {
		t.Run(name+"/"+tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("%s/%s: handler panicked instead of returning error: %v", name, tc.name, r)
				}
			}()
			_, err := h(context.Background(), json.RawMessage(tc.args))
			if err == nil {
				t.Fatalf("%s/%s: expected boundary error, got nil", name, tc.name)
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s/%s: error %q doesn't contain %q", name, tc.name, err.Error(), tc.want)
			}
		})
	}
}

func TestTreeOverviewBoundary(t *testing.T) {
	perToolArgBoundary(t, "tree_overview", tool.TreeOverview(nil), []boundaryTest{
		{name: "no_repo_bound", args: `{}`, want: "no repo bound"},
		{name: "empty_repo_string", args: `{"repo":""}`, want: "no repo bound"},
	})
}

func TestGetNodeBoundary(t *testing.T) {
	perToolArgBoundary(t, "node_get", tool.GetNode(nil), []boundaryTest{
		{name: "missing_id", args: `{}`, want: "id is required"},
		{name: "empty_id", args: `{"id":""}`, want: "id is required"},
		{name: "bad_id_kind", args: `{"id":"unknown:something"}`, want: "unknown kind"},
	})
}

func TestNodeSourceBoundary(t *testing.T) {
	perToolArgBoundary(t, "node_source", tool.NodeSource(nil), []boundaryTest{
		{name: "missing_id", args: `{}`, want: "id is required"},
	})
}

func TestNodeEdgesBoundary(t *testing.T) {
	perToolArgBoundary(t, "node_edges", tool.NodeEdges(nil), []boundaryTest{
		{name: "missing_id", args: `{}`, want: "id is required"},
	})
}

func TestSearchBoundary(t *testing.T) {
	perToolArgBoundary(t, "search", tool.Search(nil), []boundaryTest{
		{name: "missing_query", args: `{"scope":"/"}`, want: "query is required"},
		{name: "empty_query", args: `{"query":"","scope":"/"}`, want: "query is required"},
	})
}

func TestEditImpactBoundary(t *testing.T) {
	perToolArgBoundary(t, "edit_impact", tool.EditImpact(nil), []boundaryTest{
		{name: "empty_renames", args: `{"renames":[]}`, want: "at least one"},
	})
}

func TestFindSymbolBoundary(t *testing.T) {
	perToolArgBoundary(t, "find_symbol", tool.FindSymbol(nil), []boundaryTest{
		{name: "missing_name_path", args: `{}`, want: "name_path is required"},
	})
}

func TestGetSymbolsOverviewBoundary(t *testing.T) {
	perToolArgBoundary(t, "get_symbols_overview", tool.GetSymbolsOverview(nil), []boundaryTest{
		{name: "missing_file", args: `{}`, want: "file is required"},
	})
}

func TestFindCodeBoundary(t *testing.T) {
	perToolArgBoundary(t, "find_code", tool.FindCode(nil), []boundaryTest{
		{name: "missing_pattern", args: `{}`, want: "pattern is required"},
		{name: "bad_regex", args: `{"pattern":"("}`, want: "regex"},
		{name: "tree_sitter_unsupported", args: `{"pattern":"foo","pattern_kind":"tree_sitter"}`, want: "tree-sitter"},
	})
}

func TestFindReferencingSymbolsBoundary(t *testing.T) {
	perToolArgBoundary(t, "find_referencing_symbols", tool.FindReferencingSymbols(nil), []boundaryTest{
		{name: "missing_symbol", args: `{}`, want: "symbol is required"},
	})
}
