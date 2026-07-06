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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/kellenff/yactt/internal/audit"
	"github.com/kellenff/yactt/internal/mcp"
	"github.com/kellenff/yactt/internal/parser"
	"github.com/kellenff/yactt/internal/persisted"
	"github.com/kellenff/yactt/internal/store"
	"github.com/kellenff/yactt/internal/tool"
)

const usage = `yactt — federated code intelligence for AI agents

Usage:
  yactt overview <path>                   Print the top of the tree for a repo.
  yactt mcp serve [path] [--audit-log=F]  Run the MCP server on stdio, rooted at path.
                                          --audit-log=F writes one JSON line per tool call to F.
  yactt version                           Print version info.
  yactt help                              Show this message.

When path is omitted, mcp serve defaults to the current working directory.
`

// version is stamped onto the binary at build time via
// -ldflags="-X main.version=<tag>". Default "dev" covers `go build`
// outside CI; release CI overrides it with the git tag.
var version = "dev"

// DefaultMaxDiskCacheBytes is the disk cache's per-repo size cap.
// 512 MiB fits a small/medium repo's parsed-file cache comfortably
// without filling disk. Override via YACTT_DISK_CACHE_MAX_BYTES
// (set to 0 for unlimited growth).
const DefaultMaxDiskCacheBytes int64 = 512 * 1024 * 1024

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

// runMCPServe starts the MCP server on stdio, bound to the repo at `path`.
// When `path` is empty, the current working directory is used.
//
// Flags (parsed positionally so the path argument stays free-form):
//
//	--audit-log=<path>   Write one JSON audit line per tools/call dispatch
//	                     to <path>. The file is created with mode 0600 and
//	                     appended on subsequent invocations. Omit to disable
//	                     per-tool audit; the startup line still goes to stderr.
func runMCPServe(args []string) error {
	var (
		repoPath  = "."
		auditPath string
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
			if repoPath != "." {
				return fmt.Errorf("unexpected positional argument: %s", a)
			}
			repoPath = a
		}
	}
	abs, err := filepath.Abs(repoPath)
	if err != nil {
		return err
	}
	loadOpts := loadOptsWithDiskCache(abs)
	repo, errs, err := store.Load(abs, loadOpts...)
	if err != nil {
		return fmt.Errorf("load: %w", err)
	}
	defer func() { _ = repo.Close() }()
	if len(errs) > 0 {
		fmt.Fprintf(os.Stderr, "warning: %d file errors during load\n", len(errs))
	}

	// Startup audit line on stderr. Always emitted — it carries the
	// binary's SHA-256 (so a downstream host can cross-check against
	// the published SHA256SUMS), the resolved root, MaxFiles cap,
	// grammar set, and per-language LSP status. The host-visible
	// audit trail is the design's AST09 mitigation.
	binSHA, _ := audit.BinarySHA256(binaryPath())
	if err := audit.EmitStartup(os.Stderr, buildStartupInfo(repo, loadOpts, binSHA)); err != nil {
		fmt.Fprintf(os.Stderr, "warning: startup audit emit: %v\n", err)
	}
	// Install-hook TOFU assertion. Logs a warning when the running
	// binary's SHA-256 differs from the (version, sha256) recorded
	// at install time. Missing TOFU is a no-op (dev installs don't
	// write one).
	warnInstallTrustChain(version, binSHA)

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
	registerAllTools(srv, repo)

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

// loadOptsWithDiskCache returns the LoadOption slice that wires the
// per-repo disk cache when a usable cache dir can be resolved. Cap
// defaults to DefaultMaxDiskCacheBytes; override via
// YACTT_DISK_CACHE_MAX_BYTES (set 0 for unlimited growth).
func loadOptsWithDiskCache(repoRoot string) []store.LoadOption {
	dir := diskCacheDir(repoRoot)
	if dir == "" {
		return nil
	}
	maxBytes := DefaultMaxDiskCacheBytes
	if env := os.Getenv("YACTT_DISK_CACHE_MAX_BYTES"); env != "" {
		if n, err := strconv.ParseInt(env, 10, 64); err == nil && n >= 0 {
			maxBytes = n
		}
	}
	return []store.LoadOption{
		store.WithDiskCache(dir),
		store.WithDiskCacheMaxBytes(maxBytes),
	}
}

// diskCacheDir returns the per-repo disk cache directory under
// $XDG_CACHE_HOME/yactt/<root-hash>, or $HOME/.cache/yactt/<root-hash>
// when XDG_CACHE_HOME is unset. Returns "" when neither can be
// resolved (no cache is then configured).
//
// Per-repo subdirectory is keyed by sha256(repoRoot)[:16] so
// different repos don't collide and a stale entry can't be served
// against the wrong tree.
func diskCacheDir(repoRoot string) string {
	var base string
	if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" {
		base = xdg
	} else if home, err := os.UserHomeDir(); err == nil && home != "" {
		base = filepath.Join(home, ".cache")
	} else {
		return ""
	}
	sum := sha256.Sum256([]byte(repoRoot))
	return filepath.Join(base, "yactt", hex.EncodeToString(sum[:16]))
}

// registerAllTools wires the 10 tools from design §5.1 plus the
// persisted_query tool (Phase 1.5, persistent query registry) onto
// the server.
//
// The registry order is the order in which they appear in the design's §5.1
// table; it's also the order clients see in `tools/list`. Every tool
// declares both an InputSchema and an OutputSchema; the OutputSchema is
// validated by RegisterTool and must declare a top-level `type:"object"`,
// which is the MCP contract on `structuredContent`.
//
// Each tool handler is constructed once and referenced by both the
// MCP server's ToolDef and the persisted query registry's ToolFunc
// map — keeping the two registries in lockstep so an op that says
// "tool: tree_overview" always calls the same closure the agent would
// call directly.
func registerAllTools(srv *mcp.Server, repo *store.Repo) {
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
	}
	for _, t := range tools {
		srv.RegisterTool(t)
	}

	// Persistent query registry (Phase 1.5). Each tool handler is
	// exposed under its MCP name so an op's Tool field can reference
	// it directly. The example ops land in persisted/example_ops.go.
	reg := persisted.NewRegistry()
	persisted.RegisterExampleOps(reg)
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
	}
	runner := persisted.NewRunner(reg, toolFuncs)
	srv.RegisterTool(mcp.ToolDef{
		Name:         "persisted_query",
		Description:  "Run a registered persisted query by id. Curated workflows (onboarding, etc.) ship as named ops.",
		InputSchema:  tool.PersistedQuerySchema,
		OutputSchema: tool.PersistedQueryOutputSchema,
		Handler:      tool.PersistedQuery(runner),
	})
}
