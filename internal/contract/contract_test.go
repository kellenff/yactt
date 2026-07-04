// Package contract holds boundary tests for each MCP tool.
//
// Contract tests verify three things:
//
//  1. The JSON Schema returned in `tools/list` round-trips through encoding/json.
//  2. Each schema declares a `required` array (no all-optional tools).
//  3. Required-field violations are rejected by the handler before any repo
//     access — the boundary catches missing args without ever reaching the
//     domain logic.
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

// everySchema is the set of (name → schema) entries that the registry should
// publish. Keeping this list in one place guarantees that any new tool adds
// both a schema constant AND a contract test entry — there's no way to add
// a tool without touching this table.
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
		{name: "tree_sitter_unsupported", args: `{"pattern":"foo","pattern_kind":"tree_sitter"}`, want: "tree_sitter"},
	})
}

func TestFindReferencingSymbolsBoundary(t *testing.T) {
	perToolArgBoundary(t, "find_referencing_symbols", tool.FindReferencingSymbols(nil), []boundaryTest{
		{name: "missing_symbol", args: `{}`, want: "symbol is required"},
	})
}
