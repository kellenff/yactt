// Package contract holds boundary tests for each MCP tool.
//
// Contract tests verify four things:
//
//  1. The JSON Schemas (input + output) returned in `tools/list`
//     round-trip through encoding/json.
//  2. Every input schema that declares properties requires at
//     least one of them. A genuinely zero-arg tool (empty
//     `properties` block) is allowed — list_projects is the
//     canonical example.
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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kellenff/yactt/internal/registry"
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
		"query_graph":              tool.QueryGraphSchema,
		"persisted_query":          tool.PersistedQuerySchema,
		"list_projects":            tool.ListProjectsSchema,
		"index_repository":         tool.IndexRepositorySchema,
		"index_status":             tool.IndexStatusSchema,
		"delete_project":           tool.DeleteProjectSchema,
		"detect_changes":           tool.DetectChangesSchema,
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
		"query_graph":              tool.QueryGraphOutputSchema,
		"persisted_query":          tool.PersistedQueryOutputSchema,
		"list_projects":            tool.ListProjectsOutputSchema,
		"index_repository":         tool.IndexRepositoryOutputSchema,
		"index_status":             tool.IndexStatusOutputSchema,
		"delete_project":           tool.DeleteProjectOutputSchema,
		"detect_changes":           tool.DetectChangesOutputSchema,
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

// TestSchemasRequiredFields guards the original "no all-optional tools"
// rule, but with one refinement: a tool that genuinely has no inputs
// (empty `properties` block) is allowed to declare an empty `required`
// array. Tools that declare properties are still required to require
// at least one — the original intent ("don't ship a tool that silently
// accepts any arg") is preserved.
//
// The "at least one required" check accepts either a top-level
// `required` array OR a top-level `anyOf` clause that contains at
// least one required array. The anyOf form is how tools that accept
// mutually-exclusive alternatives (e.g. `detect_changes` accepting
// either `base` or `since`) express their constraint without forcing
// a single canonical field.
func TestSchemasRequiredFields(t *testing.T) {
	for name, raw := range everySchema(t) {
		t.Run(name, func(t *testing.T) {
			var out map[string]any
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatalf("schema %s: %v", name, err)
			}
			props, _ := out["properties"].(map[string]any)
			req, _ := out["required"].([]any)
			anyOf, _ := out["anyOf"].([]any)
			if len(props) > 0 && len(req) == 0 && len(anyOf) == 0 {
				t.Fatalf("schema %s: declared %d properties but required[] (and anyOf[]) are empty", name, len(props))
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

// boundaryRegForTest creates a fresh *registry.Registry whose
// single entry points at a stable per-process path under
// os.TempDir. Boundary tests use the returned URI in the args
// to exercise the tool's own validation guards; the reg keeps
// project.Resolve from returning ErrNotIndexed AND the tempdir
// keeps store.Load from failing with "no such file or directory"
// before the tool's own id/query/symbol checks fire.
func boundaryRegForTest(t *testing.T) (*registry.Registry, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "boundary-repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dir := t.TempDir()
	reg := registry.New(filepath.Join(dir, "projects.json"))
	if err := reg.Upsert(registry.Entry{
		Name:      "repo",
		Path:      root,
		IndexedAt: time.Now().UTC(),
		Files:     0,
	}); err != nil {
		t.Fatalf("boundaryRegForTest: %v", err)
	}
	return reg, "file://" + root
}

// regForBoundary is a tiny convenience wrapper that builds a fresh
// registry+URI pair per call and registers a package-level
// boundaryURI var so perToolArgBoundary sub-tests can build
// their args via a closure. See TestGetNodeBoundary for the
// canonical pattern.
var boundaryURIJSON string

func regForBoundary(t *testing.T) *registry.Registry {
	t.Helper()
	reg, uri := boundaryRegForTest(t)
	b, _ := json.Marshal(uri)
	boundaryURIJSON = string(b)
	return reg
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
		// After the project-reference migration, tree_overview takes a
		// *registry.Registry (nil here) and resolves args.Project via
		// project.ParseRef. Empty project → ErrEmpty before the registry
		// is touched, so the boundary error is "project: empty reference"
		// rather than the legacy "no repo bound". Legacy `repo` field
		// is also rejected for the same reason (empty → empty reference).
		{name: "no_project", args: `{}`, want: "project: empty reference"},
		{name: "empty_project", args: `{"project":""}`, want: "project: empty reference"},
		{name: "legacy_empty_repo", args: `{"repo":""}`, want: "project: empty reference"},
	})
	// ponytail: scope validation lives behind a real repo, so it's covered
	// by TestBuildOverviewTree_ScopeOutsideRepoErrors in treeoverview_test.go
	// rather than this nil-repo boundary suite.
}

func TestGetNodeBoundary(t *testing.T) {
	perToolArgBoundary(t, "node_get", tool.GetNode(regForBoundary(t)), []boundaryTest{
		// After the project-reference migration, every repo-bound tool
		// requires `project` (file:// URI) before its own required-
		// field guards. The `empty_project` sub-test pins the boundary
		// on the new field; the rest use a placeholder project URI to
		// exercise the tool's own validation.
		{name: "empty_project", args: `{}`, want: "project: empty reference"},
		{name: "missing_id", args: `{"project":` + boundaryURIJSON + `}`, want: "id is required"},
		{name: "empty_id", args: `{"project":` + boundaryURIJSON + `,"id":""}`, want: "id is required"},
		{name: "bad_id_kind", args: `{"project":` + boundaryURIJSON + `,"id":"unknown:something"}`, want: "unknown kind"},
	})
}

func TestNodeSourceBoundary(t *testing.T) {
	perToolArgBoundary(t, "node_source", tool.NodeSource(regForBoundary(t)), []boundaryTest{
		{name: "empty_project", args: `{}`, want: "project: empty reference"},
		{name: "missing_id", args: `{"project":` + boundaryURIJSON + `}`, want: "id is required"},
	})
}

func TestNodeEdgesBoundary(t *testing.T) {
	perToolArgBoundary(t, "node_edges", tool.NodeEdges(regForBoundary(t)), []boundaryTest{
		{name: "empty_project", args: `{}`, want: "project: empty reference"},
		{name: "missing_id", args: `{"project":` + boundaryURIJSON + `}`, want: "id is required"},
	})
}

func TestSearchBoundary(t *testing.T) {
	perToolArgBoundary(t, "search", tool.Search(regForBoundary(t)), []boundaryTest{
		{name: "empty_project", args: `{}`, want: "project: empty reference"},
		{name: "missing_query", args: `{"project":` + boundaryURIJSON + `,"scope":"/"}`, want: "query is required"},
		{name: "empty_query", args: `{"project":` + boundaryURIJSON + `,"query":"","scope":"/"}`, want: "query is required"},
	})
}

func TestEditImpactBoundary(t *testing.T) {
	perToolArgBoundary(t, "edit_impact", tool.EditImpact(regForBoundary(t)), []boundaryTest{
		{name: "empty_project", args: `{}`, want: "project: empty reference"},
		{name: "empty_renames", args: `{"project":` + boundaryURIJSON + `,"renames":[]}`, want: "at least one"},
	})
}

func TestFindSymbolBoundary(t *testing.T) {
	perToolArgBoundary(t, "find_symbol", tool.FindSymbol(regForBoundary(t)), []boundaryTest{
		{name: "empty_project", args: `{}`, want: "project: empty reference"},
		{name: "missing_name_path", args: `{"project":` + boundaryURIJSON + `}`, want: "name_path is required"},
	})
}

func TestGetSymbolsOverviewBoundary(t *testing.T) {
	perToolArgBoundary(t, "get_symbols_overview", tool.GetSymbolsOverview(regForBoundary(t)), []boundaryTest{
		{name: "empty_project", args: `{}`, want: "project: empty reference"},
		{name: "missing_file", args: `{"project":` + boundaryURIJSON + `}`, want: "file is required"},
	})
}

func TestFindCodeBoundary(t *testing.T) {
	perToolArgBoundary(t, "find_code", tool.FindCode(regForBoundary(t)), []boundaryTest{
		{name: "empty_project", args: `{}`, want: "project: empty reference"},
		{name: "missing_pattern", args: `{"project":` + boundaryURIJSON + `}`, want: "pattern is required"},
		{name: "bad_regex", args: `{"project":` + boundaryURIJSON + `,"pattern":"("}`, want: "regex"},
		{name: "tree_sitter_unsupported", args: `{"project":` + boundaryURIJSON + `,"pattern":"foo","pattern_kind":"tree_sitter"}`, want: "tree-sitter"},
	})
}

func TestFindReferencingSymbolsBoundary(t *testing.T) {
	perToolArgBoundary(t, "find_referencing_symbols", tool.FindReferencingSymbols(regForBoundary(t)), []boundaryTest{
		{name: "empty_project", args: `{}`, want: "project: empty reference"},
		{name: "missing_symbol", args: `{"project":` + boundaryURIJSON + `}`, want: "symbol is required"},
	})
}

// TestQueryGraphBoundary pins the validation contract at the handler
// boundary (nil repo). The handler must reject malformed args BEFORE
// touching the repo. Multi-seed validation (mutually-exclusive from,
// max-50 seeds, weights 1:1) is exercised here too — the goal is to
// catch a future regression where validation drifts behind locateSeeds.
func TestQueryGraphBoundary(t *testing.T) {
	perToolArgBoundary(t, "query_graph", tool.QueryGraph(regForBoundary(t)), []boundaryTest{
		{name: "empty_project", args: `{}`, want: "project: empty reference"},
		{name: "missing_from_and_seeds", args: `{"project":` + boundaryURIJSON + `,"follow":["callees"]}`, want: "provide one of from or seeds"},
		{name: "both_from_and_seeds", args: `{"project":` + boundaryURIJSON + `,"from":"fn:a.Foo","seeds":["fn:a.Bar"],"follow":["callees"]}`, want: "mutually exclusive"},
		{name: "empty_follow", args: `{"project":` + boundaryURIJSON + `,"from":"fn:a.Foo","follow":[]}`, want: "follow must be a non-empty list"},
		{name: "weights_mismatch", args: `{"project":` + boundaryURIJSON + `,"seeds":["fn:a.Foo"],"weights":[0.5,0.5],"follow":["callers"]}`, want: "weights must align"},
	})
}

func TestDetectChangesBoundary(t *testing.T) {
	perToolArgBoundary(t, "detect_changes", tool.DetectChanges(regForBoundary(t)), []boundaryTest{
		// After the project-reference migration, detect_changes requires
		// `project` (file:// URI) as well. Tests below pass a placeholder
		// project URI to exercise the ref-validation guards; the empty-
		// project case is covered by the next sub-test.
		{name: "missing_refs", args: `{"project":` + boundaryURIJSON + `}`, want: "required"},
		{name: "both_base_and_since", args: `{"project":` + boundaryURIJSON + `,"base":"HEAD~1","since":"HEAD~1"}`, want: "mutually exclusive"},
		{name: "negative_limit", args: `{"project":` + boundaryURIJSON + `,"base":"HEAD~1","limit":-1}`, want: "limit must be >= 0"},
		{name: "base_starts_with_dash", args: `{"project":` + boundaryURIJSON + `,"base":"--upload-pack=evil"}`, want: "must not start with '-'"},
		{name: "since_starts_with_dash", args: `{"project":` + boundaryURIJSON + `,"since":"-x"}`, want: "must not start with '-'"},
		{name: "head_starts_with_dash", args: `{"project":` + boundaryURIJSON + `,"base":"HEAD~1","head":"--exec=evil"}`, want: "must not start with '-'"},
		{name: "empty_project", args: `{}`, want: "project: empty reference"},
	})
}
