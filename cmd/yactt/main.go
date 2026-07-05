// Command yactt is the federated code intelligence CLI.
//
// Two subcommands ship in MVP:
//
//	yactt overview <path>   Print the top of the tree for `path`.
//	yactt mcp serve [path]  Run the MCP server on stdio, rooted at `path`.
//
// The CLI is intentionally thin: it loads a repo, then either prints a tree
// or wires tools into an mcp.Server. Anything with logic lives in internal/.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/kellenff/yactt/internal/mcp"
	"github.com/kellenff/yactt/internal/store"
	"github.com/kellenff/yactt/internal/tool"
)

const usage = `yactt — federated code intelligence for AI agents

Usage:
  yactt overview <path>    Print the top of the tree for a repo.
  yactt mcp serve [path]   Run the MCP server on stdio, rooted at path.
  yactt version            Print version info.
  yactt help               Show this message.

When path is omitted, mcp serve defaults to the current working directory.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "help", "-h", "--help":
		fmt.Print(usage)
	case "version", "-v", "--version":
		fmt.Println("yactt mvp")
	case "overview":
		if err := runOverview(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "mcp":
		if len(os.Args) < 3 || os.Args[2] != "serve" {
			fmt.Fprintln(os.Stderr, "usage: yactt mcp serve [path]")
			os.Exit(2)
		}
		if err := runMCPServe(os.Args[3:]); err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

// runOverview loads the repo at `path` and prints a tree representation of
// the top-2 levels in JSON. Future versions may accept a `--depth` flag.
func runOverview(args []string) error {
	if len(args) != 1 {
		return errors.New("overview: expected one path argument")
	}
	repo, errs, err := store.Load(args[0])
	if err != nil {
		return fmt.Errorf("load: %w", err)
	}
	defer func() { _ = repo.Close() }()
	if len(errs) > 0 {
		fmt.Fprintf(os.Stderr, "load: %d file errors\n", len(errs))
	}
	handler := tool.TreeOverview(repo)
	out, herr := handler(context.Background(), json.RawMessage(fmt.Sprintf(`{"repo":%q,"depth":2}`, repo.Root())))
	if herr != nil {
		return herr
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
	return nil
}

// runMCPServe starts the MCP server on stdio, bound to the repo at `path`.
// When `path` is empty, the current working directory is used.
func runMCPServe(args []string) error {
	repoPath := "."
	if len(args) >= 1 {
		repoPath = args[0]
	}
	abs, err := filepath.Abs(repoPath)
	if err != nil {
		return err
	}
	repo, errs, err := store.Load(abs)
	if err != nil {
		return fmt.Errorf("load: %w", err)
	}
	defer func() { _ = repo.Close() }()
	if len(errs) > 0 {
		fmt.Fprintf(os.Stderr, "warning: %d file errors during load\n", len(errs))
	}

	srv := mcp.NewServer(
		"yactt",
		"mvp",
		"2024-11-05",
		os.Stdout,
		func() (io.Reader, error) { return os.Stdin, nil },
	)
	registerAllTools(srv, repo)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	return srv.Serve(ctx)
}

// registerAllTools wires the 10 tools from design §5.1 onto the server.
//
// The registry order is the order in which they appear in the design's §5.1
// table; it's also the order clients see in `tools/list`.
func registerAllTools(srv *mcp.Server, repo *store.Repo) {
	tools := []mcp.ToolDef{
		{Name: "tree_overview", Description: "Get the top of the repo tree (depth-limited).", InputSchema: tool.TreeOverviewSchema, Handler: tool.TreeOverview(repo)},
		{Name: "node_get", Description: "Get one or more layers of a node by its stable ID.", InputSchema: tool.GetNodeSchema, Handler: tool.GetNode(repo)},
		{Name: "node_source", Description: "Get the lossless source for a node, optionally bounded by a line range.", InputSchema: tool.NodeSourceSchema, Handler: tool.NodeSource(repo)},
		{Name: "node_edges", Description: "Get cross-references for a node.", InputSchema: tool.NodeEdgesSchema, Handler: tool.NodeEdges(repo)},
		{Name: "search", Description: "Find symbols by name or doc-comment matching.", InputSchema: tool.SearchSchema, Handler: tool.Search(repo)},
		{Name: "edit_impact", Description: "Analyze the impact of a proposed set of renames. Does NOT apply them.", InputSchema: tool.EditImpactSchema, Handler: tool.EditImpact(repo)},
		{Name: "find_symbol", Description: "Locate symbols by qualified name path with glob support.", InputSchema: tool.FindSymbolSchema, Handler: tool.FindSymbol(repo)},
		{Name: "get_symbols_overview", Description: "Get the top-level structural outline of a file.", InputSchema: tool.GetSymbolsOverviewSchema, Handler: tool.GetSymbolsOverview(repo)},
		{Name: "find_code", Description: "AST-aware or regex pattern search across files.", InputSchema: tool.FindCodeSchema, Handler: tool.FindCode(repo)},
		{Name: "find_referencing_symbols", Description: "Find all symbols that reference a given symbol.", InputSchema: tool.FindReferencingSymbolsSchema, Handler: tool.FindReferencingSymbols(repo)},
	}
	for _, t := range tools {
		srv.RegisterTool(t)
	}
}
