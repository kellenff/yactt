package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/kellenff/yactt/internal/mcp"
	"github.com/kellenff/yactt/internal/registry"
	"github.com/kellenff/yactt/internal/store"
	"github.com/kellenff/yactt/internal/store/repofixture"
)

// wireShapeServer builds an MCP server pointed at in-memory buffers with
// `tools` registered. Returns the server plus the stdout buffer for the
// test to assert on the wire frame.
func wireShapeServer(t *testing.T, tools ...mcp.ToolDef) (*mcp.Server, *bytes.Buffer) {
	t.Helper()
	stdin := &bytes.Buffer{}
	stdout := &bytes.Buffer{}
	s := mcp.NewServer("wire-shape-test", "0.0.0-test", "2024-11-05", stdout,
		func() (io.Reader, error) { return stdin, nil },
	)
	for _, tc := range tools {
		s.RegisterTool(tc)
	}
	return s, stdout
}

// assertStructuredContentIsObject parses the server's stdout as a JSON-RPC
// response and asserts that structuredContent is a JSON object on the
// wire. This is the MCP contract — non-object shapes get rejected by the
// host-side schema validator.
func assertStructuredContentIsObject(t *testing.T, stdout *bytes.Buffer) map[string]any {
	t.Helper()
	var frame map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &frame); err != nil {
		t.Fatalf("unmarshal frame: %v\n%s", err, stdout.String())
	}
	if errObj, ok := frame["error"]; ok {
		t.Fatalf("RPC error: %v", errObj)
	}
	result, ok := frame["result"].(map[string]any)
	if !ok {
		t.Fatalf("result = %T, want object", frame["result"])
	}
	sc, ok := result["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("structuredContent = %T, want object (the MCP contract); full result = %+v", result["structuredContent"], result)
	}
	return sc
}

// wireShapeCase drives one tool end-to-end through the server wire and
// asserts the structuredContent shape. Every tool gets one of these —
// the assertion is the contract that the wire shape is always a JSON
// object, never an array.
type wireShapeCase struct {
	toolName string
	argsJSON string
	wantKey  string // the envelope key the handler returns its list under
}

// runWireShapeCases registers the real handler for each tool with the
// fixture repo, drives it through the server, and asserts the wire shape.
func runWireShapeCases(t *testing.T, repo *store.Repo, cases []wireShapeCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.toolName, func(t *testing.T) {
			stdin := &bytes.Buffer{}
			stdout := &bytes.Buffer{}
			s := mcp.NewServer("wire-shape-test", "0.0.0-test", "2024-11-05", stdout,
				func() (io.Reader, error) { return stdin, nil },
			)
			def := wireShapeDefs[tc.toolName]
			s.RegisterTool(def(repo))

			req := map[string]any{
				"jsonrpc": "2.0",
				"id":      1,
				"method":  "tools/call",
				"params": map[string]any{
					"name":      tc.toolName,
					"arguments": json.RawMessage(tc.argsJSON),
				},
			}
			reqBytes, _ := json.Marshal(req)
			stdin.Write(reqBytes)
			stdin.Write([]byte("\n"))

			if err := s.Serve(context.Background()); err != nil {
				t.Fatalf("Serve: %v", err)
			}

			sc := assertStructuredContentIsObject(t, stdout)
			// Most envelopes expose the list under a known key. Pin the
			// key as well, so a future "envelope name change" is caught
			// here too, not silently in client code.
			if tc.wantKey != "" {
				if _, ok := sc[tc.wantKey]; !ok {
					t.Errorf("envelope key %q missing: structuredContent = %+v", tc.wantKey, sc)
				}
			}
		})
	}
}

// wireShapeDefs maps each tool name to a function that builds its ToolDef
// from a repo. Adding a new tool means adding one row to this table —
// the wire-shape check is then mechanical.
var wireShapeDefs = map[string]func(*store.Repo) mcp.ToolDef{
	"tree_overview": func(r *store.Repo) mcp.ToolDef {
		return mcp.ToolDef{Name: "tree_overview", InputSchema: TreeOverviewSchema, OutputSchema: TreeOverviewOutputSchema, Handler: TreeOverview(r)}
	},
	"node_get": func(r *store.Repo) mcp.ToolDef {
		return mcp.ToolDef{Name: "node_get", InputSchema: GetNodeSchema, OutputSchema: GetNodeOutputSchema, Handler: GetNode(r)}
	},
	"node_source": func(r *store.Repo) mcp.ToolDef {
		return mcp.ToolDef{Name: "node_source", InputSchema: NodeSourceSchema, OutputSchema: NodeSourceOutputSchema, Handler: NodeSource(r)}
	},
	"node_edges": func(r *store.Repo) mcp.ToolDef {
		return mcp.ToolDef{Name: "node_edges", InputSchema: NodeEdgesSchema, OutputSchema: NodeEdgesOutputSchema, Handler: NodeEdges(r)}
	},
	"search": func(r *store.Repo) mcp.ToolDef {
		return mcp.ToolDef{Name: "search", InputSchema: SearchSchema, OutputSchema: SearchOutputSchema, Handler: Search(r)}
	},
	"edit_impact": func(r *store.Repo) mcp.ToolDef {
		return mcp.ToolDef{Name: "edit_impact", InputSchema: EditImpactSchema, OutputSchema: EditImpactOutputSchema, Handler: EditImpact(r)}
	},
	"find_symbol": func(r *store.Repo) mcp.ToolDef {
		return mcp.ToolDef{Name: "find_symbol", InputSchema: FindSymbolSchema, OutputSchema: FindSymbolOutputSchema, Handler: FindSymbol(r)}
	},
	"get_symbols_overview": func(r *store.Repo) mcp.ToolDef {
		return mcp.ToolDef{Name: "get_symbols_overview", InputSchema: GetSymbolsOverviewSchema, OutputSchema: GetSymbolsOverviewOutputSchema, Handler: GetSymbolsOverview(r)}
	},
	"find_code": func(r *store.Repo) mcp.ToolDef {
		return mcp.ToolDef{Name: "find_code", InputSchema: FindCodeSchema, OutputSchema: FindCodeOutputSchema, Handler: FindCode(r)}
	},
	"search_code": func(r *store.Repo) mcp.ToolDef {
		return mcp.ToolDef{Name: "search_code", InputSchema: SearchCodeSchema, OutputSchema: SearchCodeOutputSchema, Handler: SearchCode(r)}
	},
	"find_referencing_symbols": func(r *store.Repo) mcp.ToolDef {
		return mcp.ToolDef{Name: "find_referencing_symbols", InputSchema: FindReferencingSymbolsSchema, OutputSchema: FindReferencingSymbolsOutputSchema, Handler: FindReferencingSymbols(r)}
	},
	"get_graph_schema": func(r *store.Repo) mcp.ToolDef {
		return mcp.ToolDef{Name: "get_graph_schema", InputSchema: GetGraphSchemaSchema, OutputSchema: GetGraphSchemaOutputSchema, Handler: GetGraphSchema(r)}
	},
	"get_code_snippet": func(r *store.Repo) mcp.ToolDef {
		return mcp.ToolDef{Name: "get_code_snippet", InputSchema: GetCodeSnippetSchema, OutputSchema: GetCodeSnippetOutputSchema, Handler: GetCodeSnippet(r)}
	},
	"get_architecture": func(r *store.Repo) mcp.ToolDef {
		return mcp.ToolDef{Name: "get_architecture", InputSchema: GetArchitectureSchema, OutputSchema: GetArchitectureOutputSchema, Handler: GetArchitecture(r)}
	},
	"query_graph": func(r *store.Repo) mcp.ToolDef {
		return mcp.ToolDef{Name: "query_graph", InputSchema: QueryGraphSchema, OutputSchema: QueryGraphOutputSchema, Handler: QueryGraph(r)}
	},
	"detect_changes": func(r *store.Repo) mcp.ToolDef {
		return mcp.ToolDef{Name: "detect_changes", InputSchema: DetectChangesSchema, OutputSchema: DetectChangesOutputSchema, Handler: DetectChanges(r)}
	},
}

// TestWireShape_AllTools pins the wire-shape contract for every tool
// in one test. The fixture is loaded once (repofixture.New), then each
// tool is registered, driven, and asserted on. The previous bug — five
// tools returning slices and breaking the MCP "object" requirement —
// is caught here for any tool that re-introduces the pattern.
func TestWireShape_AllTools(t *testing.T) {
	fx := repofixture.New(t)
	r, _, err := store.Load(fx.Root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = r.Close() }()

	cases := []wireShapeCase{
		{toolName: "tree_overview", argsJSON: `{"repo":"","depth":2}`},
		{toolName: "node_get", argsJSON: `{"id":"fn:auth.Login","layers":["signature"]}`},
		{toolName: "node_source", argsJSON: `{"id":"fn:auth.Login"}`},
		{toolName: "node_edges", argsJSON: `{"id":"fn:auth.Login","kinds":["callees"],"limit":10}`, wantKey: "edges"},
		{toolName: "search", argsJSON: `{"query":"Login","scope":""}`, wantKey: "results"},
		{toolName: "edit_impact", argsJSON: `{"renames":[{"id":"fn:auth.Login","new_name":"SignIn"}]}`, wantKey: "renames"},
		{toolName: "find_symbol", argsJSON: `{"name_path":"auth/Login","limit":5}`, wantKey: "symbols"},
		{toolName: "get_symbols_overview", argsJSON: `{"file":"auth/login.go"}`, wantKey: "symbols"},
		{toolName: "find_code", argsJSON: `{"pattern":"Login","pattern_kind":"regex","limit":10}`, wantKey: "matches"},
		{toolName: "search_code", argsJSON: `{"pattern":"Login","pattern_kind":"regex","limit":10}`, wantKey: "groups"},
		{toolName: "find_referencing_symbols", argsJSON: `{"symbol":"fn:auth.Login","kinds":["tests"]}`, wantKey: "references"},
		{toolName: "get_graph_schema", argsJSON: `{}`},
		{toolName: "get_code_snippet", argsJSON: `{"name_path":"auth.Login"}`},
		{toolName: "get_architecture", argsJSON: `{}`},
		{toolName: "query_graph", argsJSON: `{"from":"meth:auth.Alpha.Ping","follow":["callees","callers"],"depth":2,"limit":10}`, wantKey: "rows"},
	}
	runWireShapeCases(t, r, cases)
}

// TestWireShape_EmptyResults guards the contract for the previously-
// broken edge case: when a list-returning handler produces zero matches,
// it returns an empty slice. Wrapped in an envelope, that's still a
// valid object on the wire (`{"matches":[]}`). A regression that
// unwraps the empty slice would send `[]` as structuredContent and
// break the contract.
func TestWireShape_EmptyResults(t *testing.T) {
	fx := repofixture.New(t)
	r, _, err := store.Load(fx.Root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = r.Close() }()

	cases := []wireShapeCase{
		// Pattern that matches nothing in the fixture.
		{toolName: "find_code", argsJSON: `{"pattern":"ZZZQQQ_no_such_thing","pattern_kind":"regex","limit":10}`, wantKey: "matches"},
		// Symbol that matches nothing.
		{toolName: "find_symbol", argsJSON: `{"name_path":"ZZZQQQ_no_such_thing","limit":5}`, wantKey: "symbols"},
		// Search with no hits.
		{toolName: "search", argsJSON: `{"query":"ZZZQQQ_no_such_thing","scope":""}`, wantKey: "results"},
		// node_edges with kinds that produce no edges.
		{toolName: "node_edges", argsJSON: `{"id":"fn:auth.Login","kinds":["tests"],"limit":10}`, wantKey: "edges"},
	}
	runWireShapeCases(t, r, cases)
}

// TestWireShape_ToolsListExposesOutputSchema confirms each registered
// tool surfaces its OutputSchema on `tools/list`. This is the consumer-
// facing half of the contract: clients (and IDE plugins) can introspect
// the structuredContent shape before calling.
func TestWireShape_ToolsListExposesOutputSchema(t *testing.T) {
	s, _ := wireShapeServer(t)
	// Register one tool to verify the round-trip.
	s.RegisterTool(mcp.ToolDef{
		Name:         "demo",
		InputSchema:  json.RawMessage(`{"type":"object"}`),
		OutputSchema: json.RawMessage(`{"type":"object","required":["ok"]}`),
		Handler:      func(context.Context, json.RawMessage) (any, error) { return map[string]any{"ok": true}, nil },
	})
	for _, td := range s.Tools() {
		if len(td.OutputSchema) == 0 {
			t.Errorf("tool %q: OutputSchema is empty", td.Name)
		}
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(td.OutputSchema, &probe); err != nil {
			t.Errorf("tool %q: OutputSchema not valid JSON: %v", td.Name, err)
		}
		if probe.Type != "object" {
			t.Errorf("tool %q: OutputSchema.type = %q, want object", td.Name, probe.Type)
		}
	}
}

// TestWireShape_DetectChanges mirrors TestWireShape_AllTools for the
// detect_changes tool. The standard repofixture is a fake `.git/` and
// `git diff` would surface an error against it, so this test uses a
// per-test real git repo (writeGitRepo is shared with the unit tests).
func TestWireShape_DetectChanges(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not on PATH: %v", err)
	}
	r := writeGitRepo(t,
		map[string]string{
			"x.go": `package x
func Use() int { return 1 }
`,
		},
		map[string]string{
			"x.go": `package x
func Use() int { return 99 }
`,
		},
	)

	runOne := func(t *testing.T, args string) {
		t.Helper()
		stdin := &bytes.Buffer{}
		stdout := &bytes.Buffer{}
		s := mcp.NewServer("wire-shape-detect", "0.0.0-test", "2024-11-05", stdout,
			func() (io.Reader, error) { return stdin, nil },
		)
		s.RegisterTool(wireShapeDefs["detect_changes"](r))

		req := map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "tools/call",
			"params": map[string]any{
				"name":      "detect_changes",
				"arguments": json.RawMessage(args),
			},
		}
		reqBytes, _ := json.Marshal(req)
		stdin.Write(reqBytes)
		stdin.Write([]byte("\n"))

		if err := s.Serve(context.Background()); err != nil {
			t.Fatalf("Serve: %v", err)
		}
		sc := assertStructuredContentIsObject(t, stdout)
		if _, ok := sc["changes"]; !ok {
			t.Errorf("envelope key \"changes\" missing: structuredContent = %+v", sc)
		}
	}

	t.Run("happy_path", func(t *testing.T) {
		runOne(t, `{"base":"HEAD~1","head":"HEAD"}`)
	})
	t.Run("since_shortcut", func(t *testing.T) {
		runOne(t, `{"since":"HEAD~1"}`)
	})
	t.Run("empty_diff", func(t *testing.T) {
		// base == head → no diff, but the envelope must still be an object.
		runOne(t, `{"base":"HEAD","head":"HEAD"}`)
	})
}
// TestWireShape_RegistryTools mirrors TestWireShape_AllTools for the
// four registry tools. Those tools aren't repo-bound — they take a
// *registry.Registry — so a separate test is cleaner than threading
// the registry through the repo-shaped wireShapeDefs table.
//
// Each subtest:
//
//  1. registers one registry tool on a fresh MCP server,
//  2. drives it via stdin with minimal-but-valid args,
//  3. asserts structuredContent is a JSON object.
//
// list_projects and index_status need only JSON plumbing;
// index_repository and delete_project also need a real on-disk
// repo to operate against (a tempdir fixture is enough).
func TestWireShape_RegistryTools(t *testing.T) {
	reg := registry.New(filepath.Join(t.TempDir(), "projects.json"))
	fx := repofixture.New(t)

	runOne := func(t *testing.T, name string, def mcp.ToolDef, args string) {
		t.Helper()
		stdin := &bytes.Buffer{}
		stdout := &bytes.Buffer{}
		s := mcp.NewServer("wire-shape-registry", "0.0.0-test", "2024-11-05", stdout,
			func() (io.Reader, error) { return stdin, nil },
		)
		s.RegisterTool(def)

		req := map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "tools/call",
			"params": map[string]any{
				"name":      name,
				"arguments": json.RawMessage(args),
			},
		}
		reqBytes, _ := json.Marshal(req)
		stdin.Write(reqBytes)
		stdin.Write([]byte("\n"))

		if err := s.Serve(context.Background()); err != nil {
			t.Fatalf("%s: Serve: %v", name, err)
		}
		assertStructuredContentIsObject(t, stdout)
	}

	tools := []mcp.ToolDef{
		{Name: "list_projects", InputSchema: ListProjectsSchema, OutputSchema: ListProjectsOutputSchema, Handler: ListProjects(reg)},
		{Name: "index_repository", InputSchema: IndexRepositorySchema, OutputSchema: IndexRepositoryOutputSchema, Handler: IndexRepository(reg)},
		{Name: "index_status", InputSchema: IndexStatusSchema, OutputSchema: IndexStatusOutputSchema, Handler: IndexStatus(reg)},
		{Name: "delete_project", InputSchema: DeleteProjectSchema, OutputSchema: DeleteProjectOutputSchema, Handler: DeleteProject(reg)},
	}
	args := map[string]string{
		"list_projects":    `{}`,
		"index_repository": `{"path": "` + fx.Root + `"}`,
		"index_status":     `{"path": "` + fx.Root + `"}`,
		"delete_project":   `{"path": "` + fx.Root + `"}`,
	}
	for _, def := range tools {
		def := def
		t.Run(def.Name, func(t *testing.T) {
			runOne(t, def.Name, def, args[def.Name])
		})
	}
}
