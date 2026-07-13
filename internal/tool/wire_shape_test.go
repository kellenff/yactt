package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/kellenff/yactt/internal/mcp"
	"github.com/kellenff/yactt/internal/registry"
	"github.com/kellenff/yactt/internal/store"
	"github.com/kellenff/yactt/internal/store/repofixture"
)

// seededReg returns a fresh registry containing one entry for the
// given absolute root path. The wire-shape tests need both a
// *registry.Registry (for migrated tools that resolve project URIs)
// and a *store.Repo (for unmigrated tools that take a closure repo);
// this helper produces the first from the same source as the second.
func seededReg(t *testing.T, root string) *registry.Registry {
	t.Helper()
	dir := t.TempDir()
	reg := registry.New(filepath.Join(dir, "projects.json"))
	if err := reg.Upsert(registry.Entry{
		Name:      filepath.Base(root),
		Path:      root,
		IndexedAt: time.Now().UTC(),
		Files:     0,
	}); err != nil {
		t.Fatalf("seededReg: %v", err)
	}
	return reg
}

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
//
// reg is the registry the migrated tools resolve project URIs against;
// repo is the in-memory repo used by the unmigrated tools that still
// take a *store.Repo closure parameter. As the migration progresses
// (Phases 3-4 of the plan) the repo argument becomes vestigial and
// can be removed.
func runWireShapeCases(t *testing.T, reg *registry.Registry, repo *store.Repo, cases []wireShapeCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.toolName, func(t *testing.T) {
			stdin := &bytes.Buffer{}
			stdout := &bytes.Buffer{}
			s := mcp.NewServer("wire-shape-test", "0.0.0-test", "2024-11-05", stdout,
				func() (io.Reader, error) { return stdin, nil },
			)
			def := wireShapeDefs[tc.toolName]
			s.RegisterTool(def(reg, repo))

			t.Logf("argsJSON for %s: %s", tc.toolName, tc.argsJSON)

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

// wireShapeDefs maps each tool name to a function that builds its
// ToolDef from a registry (and an unused repo for legacy wire-shape
// cases — every repo-bound tool now resolves project URIs via the
// registry). After Phase 4 the repo parameter can be removed; until
// then it is accepted but unused.
//
// ponytail: kept the (reg, repo) signature for the same reason as
// runWireShapeCases — it lets the wire-shape test stay green while
// individual tool migrations land, without a coordinated flag day.
var wireShapeDefs = map[string]func(*registry.Registry, *store.Repo) mcp.ToolDef{
	"tree_overview": func(reg *registry.Registry, _ *store.Repo) mcp.ToolDef {
		return mcp.ToolDef{Name: "tree_overview", InputSchema: TreeOverviewSchema, OutputSchema: TreeOverviewOutputSchema, Handler: TreeOverview(reg)}
	},
	"node_get": func(reg *registry.Registry, _ *store.Repo) mcp.ToolDef {
		return mcp.ToolDef{Name: "node_get", InputSchema: GetNodeSchema, OutputSchema: GetNodeOutputSchema, Handler: GetNode(reg)}
	},
	"node_source": func(reg *registry.Registry, _ *store.Repo) mcp.ToolDef {
		return mcp.ToolDef{Name: "node_source", InputSchema: NodeSourceSchema, OutputSchema: NodeSourceOutputSchema, Handler: NodeSource(reg)}
	},
	"node_edges": func(reg *registry.Registry, _ *store.Repo) mcp.ToolDef {
		return mcp.ToolDef{Name: "node_edges", InputSchema: NodeEdgesSchema, OutputSchema: NodeEdgesOutputSchema, Handler: NodeEdges(reg)}
	},
	"search": func(reg *registry.Registry, _ *store.Repo) mcp.ToolDef {
		return mcp.ToolDef{Name: "search", InputSchema: SearchSchema, OutputSchema: SearchOutputSchema, Handler: Search(reg)}
	},
	"edit_impact": func(reg *registry.Registry, _ *store.Repo) mcp.ToolDef {
		return mcp.ToolDef{Name: "edit_impact", InputSchema: EditImpactSchema, OutputSchema: EditImpactOutputSchema, Handler: EditImpact(reg)}
	},
	"find_symbol": func(reg *registry.Registry, _ *store.Repo) mcp.ToolDef {
		return mcp.ToolDef{Name: "find_symbol", InputSchema: FindSymbolSchema, OutputSchema: FindSymbolOutputSchema, Handler: FindSymbol(reg)}
	},
	"get_symbols_overview": func(reg *registry.Registry, _ *store.Repo) mcp.ToolDef {
		return mcp.ToolDef{Name: "get_symbols_overview", InputSchema: GetSymbolsOverviewSchema, OutputSchema: GetSymbolsOverviewOutputSchema, Handler: GetSymbolsOverview(reg)}
	},
	"find_code": func(reg *registry.Registry, _ *store.Repo) mcp.ToolDef {
		return mcp.ToolDef{Name: "find_code", InputSchema: FindCodeSchema, OutputSchema: FindCodeOutputSchema, Handler: FindCode(reg)}
	},
	"search_code": func(reg *registry.Registry, _ *store.Repo) mcp.ToolDef {
		return mcp.ToolDef{Name: "search_code", InputSchema: SearchCodeSchema, OutputSchema: SearchCodeOutputSchema, Handler: SearchCode(reg)}
	},
	"find_referencing_symbols": func(reg *registry.Registry, _ *store.Repo) mcp.ToolDef {
		return mcp.ToolDef{Name: "find_referencing_symbols", InputSchema: FindReferencingSymbolsSchema, OutputSchema: FindReferencingSymbolsOutputSchema, Handler: FindReferencingSymbols(reg)}
	},
	"get_graph_schema": func(reg *registry.Registry, _ *store.Repo) mcp.ToolDef {
		return mcp.ToolDef{Name: "get_graph_schema", InputSchema: GetGraphSchemaSchema, OutputSchema: GetGraphSchemaOutputSchema, Handler: GetGraphSchema()}
	},
	"get_code_snippet": func(reg *registry.Registry, _ *store.Repo) mcp.ToolDef {
		return mcp.ToolDef{Name: "get_code_snippet", InputSchema: GetCodeSnippetSchema, OutputSchema: GetCodeSnippetOutputSchema, Handler: GetCodeSnippet(reg)}
	},
	"get_architecture": func(reg *registry.Registry, _ *store.Repo) mcp.ToolDef {
		return mcp.ToolDef{Name: "get_architecture", InputSchema: GetArchitectureSchema, OutputSchema: GetArchitectureOutputSchema, Handler: GetArchitecture(reg)}
	},
	"query_graph": func(reg *registry.Registry, _ *store.Repo) mcp.ToolDef {
		return mcp.ToolDef{Name: "query_graph", InputSchema: QueryGraphSchema, OutputSchema: QueryGraphOutputSchema, Handler: QueryGraph(reg)}
	},
	"detect_changes": func(reg *registry.Registry, _ *store.Repo) mcp.ToolDef {
		return mcp.ToolDef{Name: "detect_changes", InputSchema: DetectChangesSchema, OutputSchema: DetectChangesOutputSchema, Handler: DetectChanges(reg)}
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
		{toolName: "tree_overview", argsJSON: `{"project":"file://` + fx.Root + `","depth":2}`},
		{toolName: "node_get", argsJSON: `{"project":"file://` + fx.Root + `","id":"fn:auth.Login","layers":["signature"]}`},
		{toolName: "node_source", argsJSON: `{"project":"file://` + fx.Root + `","id":"fn:auth.Login"}`},
		{toolName: "node_edges", argsJSON: `{"project":"file://` + fx.Root + `","id":"fn:auth.Login","kinds":["callees"],"limit":10}`, wantKey: "edges"},
		{toolName: "search", argsJSON: `{"project":"file://` + fx.Root + `","query":"Login","scope":""}`, wantKey: "results"},
		{toolName: "edit_impact", argsJSON: `{"project":"file://` + fx.Root + `","renames":[{"id":"fn:auth.Login","new_name":"SignIn"}]}`, wantKey: "renames"},
		{toolName: "find_symbol", argsJSON: `{"project":"file://` + fx.Root + `","name_path":"auth/Login","limit":5}`, wantKey: "symbols"},
		{toolName: "get_symbols_overview", argsJSON: `{"project":"file://` + fx.Root + `","file":"auth/login.go"}`, wantKey: "symbols"},
		{toolName: "find_code", argsJSON: `{"project":"file://` + fx.Root + `","pattern":"Login","pattern_kind":"regex","limit":10}`, wantKey: "matches"},
		{toolName: "search_code", argsJSON: `{"project":"file://` + fx.Root + `","pattern":"Login","pattern_kind":"regex","limit":10}`, wantKey: "groups"},
		{toolName: "find_referencing_symbols", argsJSON: `{"project":"file://` + fx.Root + `","symbol":"fn:auth.Login","kinds":["tests"]}`, wantKey: "references"},
		{toolName: "get_graph_schema", argsJSON: `{}`},
		{toolName: "get_code_snippet", argsJSON: `{"project":"file://` + fx.Root + `","name_path":"auth.Login"}`},
		{toolName: "get_architecture", argsJSON: `{"project":"file://` + fx.Root + `"}`},
		{toolName: "query_graph", argsJSON: `{"project":"file://` + fx.Root + `","from":"meth:auth.Alpha.Ping","follow":["callees","callers"],"depth":2,"limit":10}`, wantKey: "rows"},
	}
	reg := seededReg(t, fx.Root)
	runWireShapeCases(t, reg, r, cases)
}

// TestWireShape_QueryGraphSeeds mirrors TestWireShape_DetectChanges: the
// multi-seed path of query_graph is a separate invocation shape, not just
// a different args string for the same tool. Each sub-cases registers
// query_graph on a fresh MCP server and asserts the wire-shape contract
// holds (structuredContent is a JSON object, the "rows" envelope key is
// present). Two flavours: plain multi-seed, and multi-seed with weights.
func TestWireShape_QueryGraphSeeds(t *testing.T) {
	fx := repofixture.New(t)
	r, _, err := store.Load(fx.Root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = r.Close() }()
	reg := seededReg(t, fx.Root)

	runOne := func(t *testing.T, args string) {
		t.Helper()
		stdin := &bytes.Buffer{}
		stdout := &bytes.Buffer{}
		s := mcp.NewServer("wire-shape-query-graph-seeds", "0.0.0-test", "2024-11-05", stdout,
			func() (io.Reader, error) { return stdin, nil },
		)
		s.RegisterTool(wireShapeDefs["query_graph"](reg, r))

		req := map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "tools/call",
			"params": map[string]any{
				"name":      "query_graph",
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
		if _, ok := sc["rows"]; !ok {
			t.Errorf("envelope key \"rows\" missing: structuredContent = %+v", sc)
		}
	}

	t.Run("multi_seed_fanout", func(t *testing.T) {
		// Two seeds reach overlapping but distinct trees. Verify the
		// response envelope still carries the rows key and that the
		// multi-seed-specific fields (seeds, weights, seedScores) are
		// all present in the structuredContent.
		stdin := &bytes.Buffer{}
		stdout := &bytes.Buffer{}
		s := mcp.NewServer("wire-shape-query-graph-seeds", "0.0.0-test", "2024-11-05", stdout,
			func() (io.Reader, error) { return stdin, nil },
		)
		s.RegisterTool(wireShapeDefs["query_graph"](reg, r))
		req := map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{
				"name": "query_graph",
				"arguments": json.RawMessage(`{"project":"file://` + fx.Root + `","seeds":["meth:auth.Alpha.Ping","fn:auth.Authenticate"],"follow":["callees","callers"],"depth":2,"limit":10}`),
			},
		}
		reqBytes, _ := json.Marshal(req)
		stdin.Write(reqBytes)
		stdin.Write([]byte("\n"))
		if err := s.Serve(context.Background()); err != nil {
			t.Fatalf("Serve: %v", err)
		}
		sc := assertStructuredContentIsObject(t, stdout)
		for _, k := range []string{"rows", "seeds", "weights", "seedScores"} {
			if _, ok := sc[k]; !ok {
				t.Errorf("multi-seed envelope key %q missing: structuredContent = %+v", k, sc)
			}
		}
	})

	t.Run("multi_seed_with_weights", func(t *testing.T) {
		runOne(t, `{"project":"file://`+fx.Root+`","seeds":["fn:auth.Login","fn:auth.Authenticate"],"weights":[0.8,0.2],"follow":["callees"],"depth":2,"limit":5}`)
	})
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
		{toolName: "find_code", argsJSON: `{"project":"file://` + fx.Root + `","pattern":"ZZZQQQ_no_such_thing","pattern_kind":"regex","limit":10}`, wantKey: "matches"},
		// Symbol that matches nothing.
		{toolName: "find_symbol", argsJSON: `{"project":"file://` + fx.Root + `","name_path":"ZZZQQQ_no_such_thing","limit":5}`, wantKey: "symbols"},
		// Search with no hits.
		{toolName: "search", argsJSON: `{"project":"file://` + fx.Root + `","query":"ZZZQQQ_no_such_thing","scope":""}`, wantKey: "results"},
		// node_edges with kinds that produce no edges.
		{toolName: "node_edges", argsJSON: `{"project":"file://` + fx.Root + `","id":"fn:auth.Login","kinds":["tests"],"limit":10}`, wantKey: "edges"},
	}
	reg := seededReg(t, fx.Root)
	runWireShapeCases(t, reg, r, cases)
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
	reg := seededReg(t, r.Root())

	runOne := func(t *testing.T, args string) {
		t.Helper()
		stdin := &bytes.Buffer{}
		stdout := &bytes.Buffer{}
		s := mcp.NewServer("wire-shape-detect", "0.0.0-test", "2024-11-05", stdout,
			func() (io.Reader, error) { return stdin, nil },
		)
		s.RegisterTool(wireShapeDefs["detect_changes"](reg, r))

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
		{Name: "index_repository", InputSchema: IndexRepositorySchema, OutputSchema: IndexRepositoryOutputSchema, Handler: IndexRepository(reg, nil, nil)},
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
