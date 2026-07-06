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
	"strings"
	"syscall"

	"github.com/kellenff/yactt/internal/audit"
	"github.com/kellenff/yactt/internal/mcp"
	"github.com/kellenff/yactt/internal/parser"
	"github.com/kellenff/yactt/internal/persisted"
	"github.com/kellenff/yactt/internal/registry"
	"github.com/kellenff/yactt/internal/store"
	"github.com/kellenff/yactt/internal/tool"
)

const usage = `yactt — federated code intelligence for AI agents

Usage:
  yactt overview <path>                   Print the top of the tree for a repo.
  yactt mcp serve [path] [--audit-log=F]  Run the MCP server on stdio.
                                          With a path: serves that repo's tools.
                                          Without: serves the registry (list_projects,
                                          index_repository, index_status, delete_project).
                                          --audit-log=F writes one JSON line per tool call to F.
  yactt version                           Print version info.
  yactt help                              Show this message.

When path is omitted from "mcp serve", the server runs in registry mode
- useful for agents that need to discover or manage which repos are
indexed before drilling into one.
`

// version is stamped onto the binary at build time via
// -ldflags="-X main.version=<tag>". Default "dev" covers `go build`
// outside CI; release CI overrides it with the git tag.
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "help", "-h", "--help":
		fmt.Print(usage)
	case "version", "-v", "--version":
		// `version` is overridden at build time via
		// -ldflags="-X main.version=<tag>" so release CI can stamp
		// the actual tag onto the binary. Default "dev" covers
		// `go build` outside CI.
		fmt.Printf("yactt %s\n", version)
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
	abs, err := filepath.Abs(args[0])
	if err != nil {
		return err
	}
	repo, errs, err := store.Load(abs, loadOptsWithDiskCache(abs)...)
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

// runMCPServe starts the MCP server on stdio. Two modes:
//
//   - Single-repo mode (one positional path): the server loads that
//     repo into memory and exposes the 14 code-intelligence tools
//     against it, plus the registry tools.
//   - Registry mode (no positional path): the server skips the repo
//     load and exposes only the four registry tools — useful for
//     agents that need to discover or manage which repos are indexed
//     before drilling into one.
//
// Flags (parsed positionally so the path argument stays free-form):
//
//	--audit-log=<path>   Write one JSON audit line per tools/call dispatch
//	                     to <path>. The file is created with mode 0600 and
//	                     appended on subsequent invocations. Omit to disable
//	                     per-tool audit; the startup line still goes to stderr
//	                     in single-repo mode.
func runMCPServe(args []string) error {
	var (
		repoPath    string // "" → registry mode; otherwise single-repo mode
		repoPathSet bool
		auditPath   string
	)
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "--audit-log="):
			auditPath = strings.TrimPrefix(a, "--audit-log=")
			if auditPath == "" {
				return errors.New("--audit-log=<path> requires a non-empty path")
			}
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag: %s", a)
		default:
			// First non-flag positional wins; subsequent positions are
			// rejected so a typo (e.g. two paths) doesn't silently
			// shadow the first.
			if repoPathSet {
				return fmt.Errorf("unexpected positional argument: %s", a)
			}
			repoPath = a
			repoPathSet = true
		}
	}

	// Registry handle is constructed up-front in both modes — the
	// 4 registry tools are useful in single-repo mode too (admin
	// agents need to add/remove neighbours).
	regPath := registry.DefaultPath()
	if regPath == "" {
		return errors.New("mcp serve: cannot resolve $XDG_CACHE_HOME or $HOME; set XDG_CACHE_HOME")
	}
	reg := registry.New(regPath)

	var repo *store.Repo
	if repoPathSet {
		abs, err := filepath.Abs(repoPath)
		if err != nil {
			return err
		}
		loadOpts := loadOptsWithDiskCache(abs)
		r, errs, err := store.Load(abs, loadOpts...)
		if err != nil {
			return fmt.Errorf("load: %w", err)
		}
		defer func() { _ = r.Close() }()
		if len(errs) > 0 {
			fmt.Fprintf(os.Stderr, "warning: %d file errors during load\n", len(errs))
		}

		// Startup audit line on stderr. Always emitted — it carries
		// the binary's SHA-256 (so a downstream host can cross-check
		// against the published SHA256SUMS), the resolved root,
		// MaxFiles cap, grammar set, and per-language LSP status. The
		// host-visible audit trail is the design's AST09 mitigation.
		binSHA, _ := audit.BinarySHA256(binaryPath())
		if err := audit.EmitStartup(os.Stderr, buildStartupInfo(r, loadOpts, binSHA)); err != nil {
			fmt.Fprintf(os.Stderr, "warning: startup audit emit: %v\n", err)
		}
		// Install-hook TOFU assertion. Logs a warning when the
		// running binary's SHA-256 differs from the (version,
		// sha256) recorded at install time. Missing TOFU is a no-op
		// (dev installs don't write one).
		warnInstallTrustChain(version, binSHA)
		repo = r
	} else {
		fmt.Fprintf(os.Stderr, "yactt mcp serve: registry mode (path: %s)\n", regPath)
	}

	// Optional per-tool audit logger. nil disables emission; the
	// dispatch path checks for nil before calling.
	var (
		auditLogger *audit.Logger
		auditCloser io.Closer
	)
	if auditPath != "" {
		var lerr error
		auditLogger, auditCloser, lerr = audit.NewFileLogger(auditPath)
		if lerr != nil {
			return fmt.Errorf("open audit log %s: %w", auditPath, lerr)
		}
		defer func() {
			if auditCloser != nil {
				_ = auditCloser.Close()
			}
		}()
	}

	srv := mcp.NewServer(
		"yactt",
		version,
		"2024-11-05",
		os.Stdout,
		func() (io.Reader, error) { return os.Stdin, nil },
	)
	if auditLogger != nil {
		srv.WithAudit(auditLogger, audit.ExtractPaths)
	}
	registerAllTools(srv, repo, reg)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	return srv.Serve(ctx)
}

// buildStartupInfo snapshots the load-time state into the audit
// startup record. Extracted from runMCPServe so the audit shape can
// be unit-tested without spawning an MCP server.
//
// ponytail: MaxFiles uses the package default rather than peeking
// the closure-supplied override. WithMaxFiles captures into an
// unexported field, and the only clean alternatives (exposing
// loadOptions or a Probe() interface on LoadOption) are heavier
// than the value: nobody today sets a non-default cap in production
// either. Lift this when the audit needs to faithfully report a
// CLI-supplied cap.
func buildStartupInfo(repo *store.Repo, opts []store.LoadOption, binSHA string) audit.Startup {
	_ = opts
	grammars := make([]string, 0)
	for _, l := range parser.All() {
		grammars = append(grammars, string(l.Name()))
	}
	var lspEntries []audit.LSPEntry
	for _, l := range parser.All() {
		_, toolName, ver := repo.LSPForLang(l.Name())
		lspEntries = append(lspEntries, audit.LSPEntry{
			Language: string(l.Name()),
			Tool:     toolName,
			Version:  ver,
		})
	}
	return audit.Startup{
		Version:      version,
		RepoRoot:     repo.Root(),
		MaxFiles:     store.DefaultMaxFiles,
		LoadedFiles:  len(repo.Files()),
		Grammars:     grammars,
		LSP:          lspEntries,
		BinarySHA256: binSHA,
	}
}

// warnInstallTrustChain reads the TOFU file the install hook writes
// and emits a stderr warning when the running binary's SHA-256
// diverges from the recorded hash at the same version. Missing TOFU
// is a no-op; a version mismatch (legitimate upgrade) is also a no-op.
func warnInstallTrustChain(currentVersion, currentSHA string) {
	if currentVersion == "dev" {
		// Dev build — TOFU is meaningless (no release artifact
		// identity to compare against). Skip silently.
		return
	}
	if currentSHA == "" {
		return
	}
	res, err := audit.CheckKnownGood(audit.KnownGoodPath(), currentVersion, currentSHA)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: install trust chain: %v\n", err)
		return
	}
	switch res.Status {
	case "mismatch":
		fmt.Fprintf(os.Stderr,
			"WARNING: yactt binary SHA-256 does not match the install hook's TOFU record\n"+
				"  recorded: %s\n"+
				"  actual:   %s\n"+
				"  version:  %s\n"+
				"  likely a replay or compromised release — refusing to trust the install\n",
			res.Expected, res.Actual, res.Version)
	}
	// "match", "missing", "unknown_version" all silent — they're
	// legitimate states.
}

// binaryPath returns the absolute path of the running executable.
// Resolves /proc/self/exe on Linux, falls back to os.Args[0] (which
// is fine for our use: we hash the bytes that get executed, not the
// path string).
func binaryPath() string {
	if p, err := os.Executable(); err == nil && p != "" {
		return p
	}
	return os.Args[0]
}

// loadOptsWithDiskCache is a thin wrapper around
// registry.LoadOptsWithDiskCache. The cmd tree keeps its name to
// preserve the existing call sites; the canonical implementation
// lives in internal/registry so the index_repository tool can
// share it.
func loadOptsWithDiskCache(repoRoot string) []store.LoadOption {
	return registry.LoadOptsWithDiskCache(repoRoot)
}

// registerAllTools wires the tools onto the server. Behaviour
// depends on whether `repo` is nil:
//
//   - repo != nil (single-repo mode): the 14 code-intelligence
//     tools + persisted_query + the 4 registry tools. Useful for
//     agents that need to query a repo AND manage its
//     neighbours.
//   - repo == nil (registry mode): only the 4 registry tools +
//     persisted_query (which still works because it can fall
//     back to the registry-only toolFunc map). The 14 repo-bound
//     tools can't exist without a repo, so they aren't
//     registered — agents in registry mode call
//     `index_repository` first if they want to drill in.
//
// The order is the order in which the design's §5.1 table
// lists tools; it's also the order clients see in
// `tools/list`. Every tool declares both an InputSchema and an
// OutputSchema; the OutputSchema is validated by RegisterTool
// and must declare a top-level `type:"object"`, which is the MCP
// contract on `structuredContent`.
//
// ponytail: the persisted-query toolFunc map omits the registry
// tools today (they aren't `tool.ToolFunc`s — they don't take a
// *store.Repo). That's fine because persisted_query's job is to
// dispatch to repo-aware workflows; the four registry tools
// belong to a different lifecycle (manage-the-fleet) and the
// two never need to share an op ID namespace. Add them when an
// agent actually needs a persisted `list_all_projects` op.
func registerAllTools(srv *mcp.Server, repo *store.Repo, reg *registry.Registry) {
	// Four registry tools — available in BOTH modes.
	srv.RegisterTool(mcp.ToolDef{
		Name: "list_projects", Description: "Enumerate every project in the registry. Sorted by path.",
		InputSchema: tool.ListProjectsSchema, OutputSchema: tool.ListProjectsOutputSchema,
		Handler: tool.ListProjects(reg),
	})
	srv.RegisterTool(mcp.ToolDef{
		Name: "index_repository", Description: "Walk a repo at `path`, write an entry to the registry, return the row. Mode knob is accepted (only `full` is wired today).",
		InputSchema: tool.IndexRepositorySchema, OutputSchema: tool.IndexRepositoryOutputSchema,
		Handler: tool.IndexRepository(reg),
	})
	srv.RegisterTool(mcp.ToolDef{
		Name: "index_status", Description: "Registry row + per-repo cache freshness for `path`. cacheFresh=false means re-running index_repository would write new bytes.",
		InputSchema: tool.IndexStatusSchema, OutputSchema: tool.IndexStatusOutputSchema,
		Handler: tool.IndexStatus(reg),
	})
	srv.RegisterTool(mcp.ToolDef{
		Name: "delete_project", Description: "Evict `path` from the registry and remove its per-repo cache directory. Idempotent on missing rows.",
		InputSchema: tool.DeleteProjectSchema, OutputSchema: tool.DeleteProjectOutputSchema,
		Handler: tool.DeleteProject(reg),
	})

	if repo == nil {
		// Registry mode: stop here. The persisted_query tool is
		// registered below with an empty toolFunc map, which the
		// runner translates into "no such op id" errors — fine,
		// because the example ops all target the 13 code-intel
		// tools anyway.
		registerPersistedQuery(srv, nil)
		return
	}

	treeOverview := tool.TreeOverview(repo)
	nodeGet := tool.GetNode(repo)
	nodeSource := tool.NodeSource(repo)
	nodeEdges := tool.NodeEdges(repo)
	search := tool.Search(repo)
	editImpact := tool.EditImpact(repo)
	findSymbol := tool.FindSymbol(repo)
	getSymbolsOverview := tool.GetSymbolsOverview(repo)
	findCode := tool.FindCode(repo)
	findReferencingSymbols := tool.FindReferencingSymbols(repo)
	getGraphSchema := tool.GetGraphSchema(repo)
	getCodeSnippet := tool.GetCodeSnippet(repo)
	getArchitecture := tool.GetArchitecture(repo)
	queryGraph := tool.QueryGraph(repo)

	tools := []mcp.ToolDef{
		{Name: "tree_overview", Description: "Get the top of the repo tree (depth-limited).", InputSchema: tool.TreeOverviewSchema, OutputSchema: tool.TreeOverviewOutputSchema, Handler: treeOverview},
		{Name: "node_get", Description: "Get one or more layers of a node by its stable ID.", InputSchema: tool.GetNodeSchema, OutputSchema: tool.GetNodeOutputSchema, Handler: nodeGet},
		{Name: "node_source", Description: "Get the lossless source for a node, optionally bounded by a line range.", InputSchema: tool.NodeSourceSchema, OutputSchema: tool.NodeSourceOutputSchema, Handler: nodeSource},
		{Name: "node_edges", Description: "Get cross-references for a node.", InputSchema: tool.NodeEdgesSchema, OutputSchema: tool.NodeEdgesOutputSchema, Handler: nodeEdges},
		{Name: "search", Description: "Find symbols by name or doc-comment matching.", InputSchema: tool.SearchSchema, OutputSchema: tool.SearchOutputSchema, Handler: search},
		{Name: "edit_impact", Description: "Analyze the impact of a proposed set of renames. Does NOT apply them.", InputSchema: tool.EditImpactSchema, OutputSchema: tool.EditImpactOutputSchema, Handler: editImpact},
		{Name: "find_symbol", Description: "Locate symbols by qualified name path with glob support.", InputSchema: tool.FindSymbolSchema, OutputSchema: tool.FindSymbolOutputSchema, Handler: findSymbol},
		{Name: "get_symbols_overview", Description: "Get the top-level structural outline of a file.", InputSchema: tool.GetSymbolsOverviewSchema, OutputSchema: tool.GetSymbolsOverviewOutputSchema, Handler: getSymbolsOverview},
		{Name: "find_code", Description: "AST-aware or regex pattern search across files.", InputSchema: tool.FindCodeSchema, OutputSchema: tool.FindCodeOutputSchema, Handler: findCode},
		{Name: "find_referencing_symbols", Description: "Find all symbols that reference a given symbol.", InputSchema: tool.FindReferencingSymbolsSchema, OutputSchema: tool.FindReferencingSymbolsOutputSchema, Handler: findReferencingSymbols},
		{Name: "get_graph_schema", Description: "List the canonical node kinds, edge kinds, and layer names. Use to write graph queries without hardcoding.", InputSchema: tool.GetGraphSchemaSchema, OutputSchema: tool.GetGraphSchemaOutputSchema, Handler: getGraphSchema},
		{Name: "get_code_snippet", Description: "Source slice for a symbol by stable id OR qualified name path. One call replaces find_symbol+node_source.", InputSchema: tool.GetCodeSnippetSchema, OutputSchema: tool.GetCodeSnippetOutputSchema, Handler: getCodeSnippet},
		{Name: "get_architecture", Description: "Structural summary: languages, packages, hotspots, dead-code candidates, import cycles.", InputSchema: tool.GetArchitectureSchema, OutputSchema: tool.GetArchitectureOutputSchema, Handler: getArchitecture},
		{Name: "query_graph", Description: "Multi-hop graph traversal with edge-kind chains, depth cap, and kind/exclude filters. Composes node_edges across hops.", InputSchema: tool.QueryGraphSchema, OutputSchema: tool.QueryGraphOutputSchema, Handler: queryGraph},
	}
	for _, t := range tools {
		srv.RegisterTool(t)
	}

	// Persistent query registry (Phase 1.5). Each tool handler is
	// exposed under its MCP name so an op's Tool field can reference
	// it directly. The example ops land in persisted/example_ops.go.
	toolFuncs := map[string]persisted.ToolFunc{
		"tree_overview":            treeOverview,
		"node_get":                 nodeGet,
		"node_source":              nodeSource,
		"node_edges":               nodeEdges,
		"search":                   search,
		"edit_impact":              editImpact,
		"find_symbol":              findSymbol,
		"get_symbols_overview":     getSymbolsOverview,
		"find_code":                findCode,
		"find_referencing_symbols": findReferencingSymbols,
		"get_graph_schema":         getGraphSchema,
		"get_code_snippet":         getCodeSnippet,
		"get_architecture":         getArchitecture,
		"query_graph":              queryGraph,
	}
	registerPersistedQuery(srv, toolFuncs)
}

// registerPersistedQuery wires the persisted_query tool onto the
// server. Pulled out of registerAllTools because it's the one
// tool that ships in BOTH modes (single-repo and registry), and
// duplicating its ToolDef would invite drift.
//
// In registry mode (`toolFuncs == nil`), the runner's lookup
// table is empty — every op id resolves to "unknown tool", which
// is the right behaviour for agents that haven't drilled into a
// project yet.
func registerPersistedQuery(srv *mcp.Server, toolFuncs map[string]persisted.ToolFunc) {
	preg := persisted.NewRegistry()
	persisted.RegisterExampleOps(preg)
	runner := persisted.NewRunner(preg, toolFuncs)
	srv.RegisterTool(mcp.ToolDef{
		Name:         "persisted_query",
		Description:  "Run a registered persisted query by id. Curated workflows (onboarding, etc.) ship as named ops.",
		InputSchema:  tool.PersistedQuerySchema,
		OutputSchema: tool.PersistedQueryOutputSchema,
		Handler:      tool.PersistedQuery(runner),
	})
}
