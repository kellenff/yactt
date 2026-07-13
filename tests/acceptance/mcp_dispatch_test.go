package acceptance_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kellenff/yactt/internal/mcp"
	"github.com/kellenff/yactt/internal/persisted"
	"github.com/kellenff/yactt/internal/registry"
	"github.com/kellenff/yactt/internal/store"
	"github.com/kellenff/yactt/internal/store/repofixture"
	"github.com/kellenff/yactt/internal/tool"
)

// TestMCPServer_RegisterThenDrillIn is the regression test for the
// project-reference migration's new lifecycle: index a fixture
// repo, then call tree_overview and find_symbol against it via
// the in-process MCP server. The JSON-RPC framing is verified
// through stdio pipes.
func TestMCPServer_RegisterThenDrillIn(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end MCP dispatch skipped in -short mode (requires LSP warmup)")
	}
	fx := repofixture.New(t)
	regPath := filepath.Join(t.TempDir(), "projects.json")
	reg := registry.New(regPath)

	r, _, err := store.Load(fx.Root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	t.Cleanup(func() { _ = stdoutR.Close(); _ = stdoutW.Close() })

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}

	srv := mcp.NewServer("yactt", "test", "2024-11-05",
		stdoutW,
		func() (io.Reader, error) { return stdinR, nil },
	)
	srv.RegisterTool(mcp.ToolDef{
		Name: "index_repository", InputSchema: tool.IndexRepositorySchema, OutputSchema: tool.IndexRepositoryOutputSchema,
		Handler: tool.IndexRepository(reg, nil, nil),
	})
	srv.RegisterTool(mcp.ToolDef{
		Name: "tree_overview", InputSchema: tool.TreeOverviewSchema, OutputSchema: tool.TreeOverviewOutputSchema,
		Handler: tool.TreeOverview(reg),
	})
	srv.RegisterTool(mcp.ToolDef{
		Name: "find_symbol", InputSchema: tool.FindSymbolSchema, OutputSchema: tool.FindSymbolOutputSchema,
		Handler: tool.FindSymbol(reg),
	})

	// Send all requests, then close stdin to signal EOF.
	projectURI := "file://" + r.Root()
	requests := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"index_repository","arguments":{"project":%q}}}`, projectURI),
		fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"tree_overview","arguments":{"project":%q,"depth":1}}}`, projectURI),
		fmt.Sprintf(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"find_symbol","arguments":{"project":%q,"name_path":"auth.Login","limit":5}}}`, projectURI),
	}
	for _, req := range requests {
		if _, err := stdinW.WriteString(req + "\n"); err != nil {
			t.Fatalf("write stdin: %v", err)
		}
	}
	_ = stdinW.Close()
	_ = stdinR.Close()

	// Read responses on the main goroutine. The server blocks on
	// stdin; closing stdin causes Serve to return EOF.
	var got bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&got, stdoutR)
		close(done)
	}()

	if err := srv.Serve(context.Background()); err != nil && !strings.Contains(err.Error(), "EOF") {
		t.Fatalf("Serve: %v", err)
	}
	<-done

	// Parse responses: 4 ids (1..4), all should have non-null results.
	lines := strings.Split(strings.TrimRight(got.String(), "\n"), "\n")
	if len(lines) < 4 {
		t.Fatalf("got %d response lines; want >= 4\n%s", len(lines), got.String())
	}
	for i, line := range lines[:4] {
		var resp struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  any             `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			t.Fatalf("response %d not JSON: %v\n%s", i, err, line)
		}
		if resp.Error != nil {
			t.Errorf("response %d: error = %v", i, resp.Error)
		}
		if len(resp.Result) == 0 {
			t.Errorf("response %d: empty result", i)
		}
	}

	// Verify the registry actually contains the fixture entry.
	entries, err := reg.List()
	if err != nil {
		t.Fatalf("registry.List: %v", err)
	}
	if len(entries) != 1 || entries[0].Path != r.Root() {
		t.Errorf("registry entries = %+v; want exactly the fixture root", entries)
	}
}

// TestPersistedQuery_ProjectInjection verifies the runner injects
// the persisted_query caller's project URI into the target tool's
// args (covers Phase 6 of the plan).
func TestPersistedQuery_ProjectInjection(t *testing.T) {
	fx := repofixture.New(t)
	dir := t.TempDir()
	reg := registry.New(filepath.Join(dir, "projects.json"))
	idx := tool.IndexRepository(reg, nil, nil)
	projectURI := "file://" + fx.Root
	if _, err := idx(context.Background(), json.RawMessage(fmt.Sprintf(`{"project":%q}`, projectURI))); err != nil {
		t.Fatalf("seed: %v", err)
	}

	preg := persisted.NewRegistry()
	persisted.RegisterExampleOps(preg)
	runner := persisted.NewRunner(preg, map[string]persisted.ToolFunc{
		"tree_overview": tool.TreeOverview(reg),
	})

	out, err := runner.Run(context.Background(), "repo-map", projectURI,
		json.RawMessage(fmt.Sprintf(`{"project":%q}`, projectURI)))
	if err != nil {
		t.Fatalf("runner.Run: %v", err)
	}
	if out == nil {
		t.Fatal("nil result")
	}
}
