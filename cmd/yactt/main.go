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
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kellenff/yactt/internal/audit"
	"github.com/kellenff/yactt/internal/mcp"
	httptransport "github.com/kellenff/yactt/internal/mcp/transport/http"
	"github.com/kellenff/yactt/internal/parser"
	"github.com/kellenff/yactt/internal/registry"
	"github.com/kellenff/yactt/internal/store"
	"github.com/kellenff/yactt/internal/tool"

	"github.com/kellenff/yactt/internal/chunker"
	"github.com/kellenff/yactt/internal/hybrid"
)

const usage = `yactt — federated code intelligence for AI agents

Usage:
  yactt overview <path>                   Print the top of the tree for a repo.
  yactt mcp serve [--audit-log=F]         Run the MCP server on stdio.
                                          The server is stateless across tool calls; tools
                                          identify their target project by a file:// URI
                                          passed in each tool's args (e.g. "file:///abs/path").
                                          Call list_projects to discover indexed repos,
                                          index_repository ({"project": "file:///abs/path"})
                                          to load a new one, then any code-intel tool
                                          with {"project": "file:///abs/path", ...}.
                                          --audit-log=F writes one JSON line per tool call to F.
  yactt chunk --repo <path> [options]     Emit AST-bounded NDJSON chunks to stdout.
                                          See "yactt chunk --help" for options.
  yactt hybrid --repo <path> --query Q    Run hybrid retrieval (structural + BM25 + vector,
                                          merged with RRF). See "yactt hybrid --help".
  yactt version                           Print version info.
  yactt help                              Show this message.

Project references on the wire
------------------------------
Every targeting tool (tree_overview, node_get, find_symbol,
search_code, query_graph, detect_changes, etc.) requires a
` + "`project`" + ` field shaped as a file:// URI:

  {"project": "file:///abs/path", ...}

The URI must be absolute and must point at a directory that has
been registered via index_repository. Relative paths and
schemes other than file:// are rejected with a clear error.

tree_overview retains a deprecated ` + "`repo`" + ` field as an alias
for ` + "`project`" + ` (a one-release grace period). Stderr emits a
deprecation notice every time it fires; remove it before v0.2.0.
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
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: yactt mcp <serve|serve-http>")
			os.Exit(2)
		}
		switch os.Args[2] {
		case "serve":
			if err := runMCPServe(os.Args[3:]); err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(1)
			}
		case "serve-http":
			if err := runMCPServeHTTP(os.Args[3:]); err != nil && !errors.Is(err, http.ErrServerClosed) {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(1)
			}
		default:
			fmt.Fprintln(os.Stderr, "usage: yactt mcp <serve|serve-http>")
			os.Exit(2)
		}
	case "chunk":
		if err := runChunk(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "hybrid":
		if err := runHybrid(os.Args[2:]); err != nil {
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
	if len(errs) > 0 {
		fmt.Fprintf(os.Stderr, "load: %d file errors\n", len(errs))
	}
	// The tree_overview tool now takes a *registry.Registry and a
	// project (file:// URI) in args. The CLI seeds an ephemeral
	// registry so the handler can resolve the project the same way
	// it does in the MCP server. Future cleanup: route this
	// through `yactt overview` as a thin wrapper around the MCP
	// `index_repository` + `tree_overview` chain.
	regPath := filepath.Join(os.TempDir(), "yactt-overview-registry.json")
	reg := registry.New(regPath)
	if err := reg.Upsert(registry.Entry{
		Name:      filepath.Base(abs),
		Path:      abs,
		IndexedAt: time.Now().UTC(),
		Files:     len(repo.Files()),
	}); err != nil {
		_ = repo.Close()
		return fmt.Errorf("overview: seed registry: %w", err)
	}
	handler := tool.TreeOverview(reg)
	out, herr := handler(context.Background(), json.RawMessage(fmt.Sprintf(`{"project":%q,"depth":2}`, "file://"+abs)))
	_ = repo.Close()
	if herr != nil {
		return herr
	}
	if herr != nil {
		return herr
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
	return nil
}

const chunkUsage = `yactt chunk — emit AST-bounded NDJSON chunks for vector-store ingest.

Usage:
  yactt chunk --repo <path> [options]

Options:
  --repo <path>            Path to the repository root (required).
  --policy <name>          Chunking policy: function (default), class, module.
  --languages <csv>        Comma-separated language filter (e.g. "go,typescript").
  --include <glob>         filepath.Match glob; can be repeated.
  --exclude <glob>         filepath.Match glob; can be repeated. Applied after --include.
  --with-tests             Include *_test.go and per-language test files.
  -o, --output <file>      Output file; default stdout.

Output is one Chunk per line in NDJSON. The shape is documented on
internal/chunker.Chunk. Errors during the load step are reported on
stderr; per-chunk decode errors are skipped (partial output is
preferred to no output for ingest pipelines).
`

// runChunk parses `yactt chunk` flags, loads the repo, and writes
// NDJSON chunks to the chosen writer. Mirrors runOverview's flag
// parsing style (positional + "--key=value"/"--key value") so the CLI
// is internally consistent.
func runChunk(args []string) error {
	var (
		opts       chunker.Options
		outputPath string
		repoPath   string
		repoSet    bool
	)
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--help" || a == "-h":
			fmt.Print(chunkUsage)
			return nil
		case a == "--repo":
			if i+1 >= len(args) {
				return errors.New("chunk: --repo requires a path argument")
			}
			repoPath = args[i+1]
			repoSet = true
			i++
		case strings.HasPrefix(a, "--repo="):
			repoPath = strings.TrimPrefix(a, "--repo=")
			repoSet = true
		case a == "--policy":
			if i+1 >= len(args) {
				return errors.New("chunk: --policy requires a value (function|class|module)")
			}
			opts.Policy = chunker.Policy(args[i+1])
			i++
		case strings.HasPrefix(a, "--policy="):
			opts.Policy = chunker.Policy(strings.TrimPrefix(a, "--policy="))
		case a == "--languages":
			if i+1 >= len(args) {
				return errors.New("chunk: --languages requires a comma-separated value")
			}
			for _, l := range strings.Split(args[i+1], ",") {
				l = strings.TrimSpace(l)
				if l == "" {
					continue
				}
				opts.Languages = append(opts.Languages, parser.Name(l))
			}
			i++
		case strings.HasPrefix(a, "--languages="):
			for _, l := range strings.Split(strings.TrimPrefix(a, "--languages="), ",") {
				l = strings.TrimSpace(l)
				if l == "" {
					continue
				}
				opts.Languages = append(opts.Languages, parser.Name(l))
			}
		case a == "--include":
			if i+1 >= len(args) {
				return errors.New("chunk: --include requires a glob")
			}
			opts.Include = append(opts.Include, args[i+1])
			i++
		case strings.HasPrefix(a, "--include="):
			opts.Include = append(opts.Include, strings.TrimPrefix(a, "--include="))
		case a == "--exclude":
			if i+1 >= len(args) {
				return errors.New("chunk: --exclude requires a glob")
			}
			opts.Exclude = append(opts.Exclude, args[i+1])
			i++
		case strings.HasPrefix(a, "--exclude="):
			opts.Exclude = append(opts.Exclude, strings.TrimPrefix(a, "--exclude="))
		case a == "--with-tests":
			opts.WithTests = true
		case a == "-o" || a == "--output":
			if i+1 >= len(args) {
				return errors.New("chunk: --output requires a path")
			}
			outputPath = args[i+1]
			i++
		case strings.HasPrefix(a, "--output="):
			outputPath = strings.TrimPrefix(a, "--output=")
		case strings.HasPrefix(a, "-o="):
			outputPath = strings.TrimPrefix(a, "-o=")
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("chunk: unknown flag: %s", a)
		default:
			// Positional args aren't accepted by chunk; everything
			// is a flag.
			return fmt.Errorf("chunk: unexpected positional argument: %s", a)
		}
	}
	if !repoSet {
		return errors.New("chunk: --repo <path> is required\n\n" + chunkUsage)
	}
	if opts.Policy != "" {
		switch opts.Policy {
		case chunker.PolicyFunction, chunker.PolicyClass, chunker.PolicyModule:
		default:
			return fmt.Errorf("chunk: unknown policy %q (want function|class|module)", opts.Policy)
		}
	}

	abs, err := filepath.Abs(repoPath)
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

	out := io.Writer(os.Stdout)
	if outputPath != "" {
		f, ferr := os.Create(outputPath)
		if ferr != nil {
			return fmt.Errorf("chunk: create output: %w", ferr)
		}
		defer func() { _ = f.Close() }()
		out = f
	}

	return runChunkPipeline(repo, opts, out, os.Stderr)
}

// runChunkPipeline is the testable core of runChunk: load the repo,
// run the chunker, write to stdout, log to stderr. Splitting it out
// keeps the CLI's flag-parsing code out of the unit-test path while
// letting tests assert on the exact NDJSON bytes emitted.
func runChunkPipeline(r *store.Repo, opts chunker.Options, stdout, stderr io.Writer) error {
	n, err := chunker.Run(context.Background(), r, opts, stdout)
	if err != nil {
		return fmt.Errorf("chunk: %w", err)
	}
	fmt.Fprintf(stderr, "chunk: %d chunks written\n", n)
	return nil
}

const hybridUsage = `yactt hybrid — fan a query out to structural + BM25 + vector channels and merge via RRF.

Usage:
  yactt hybrid --repo <path> --query "<query>" [options]

Options:
  --repo <path>            Repository root (required).
  --query "<string>"       Query string (required). Tokenized on whitespace;
                           quoted phrases become single terms.
  --limit <n>              Final top-K; default 10.
  --channels <csv>         Comma-separated channel list to enable. Default
                           "structural,bm25,vector". Use to benchmark single-
                           channel vs hybrid (e.g. "bm25" alone).
  --with-tests             Include test files in the BM25 + vector corpus.
  --explain                Print per-channel rankings alongside the merged list,
                           so you can see WHY hybrid wins (or loses) on a query.
  -h, --help               Show this message.

Output:
  Default: JSON object {"results": [...]}. Each hit carries id, rrf-score,
  channel, and (for bm25/vector) the underlying chunk payload.
  With --explain: JSON object {"structural":[...],"bm25":[...],"vector":[...],
  "rrf":[...]}, so the per-channel and merged rankings are visible side by side.

Notes:
  - The vector channel uses a stdlib bag-of-tokens reference backend. It's
    good enough to demo the merge and run the benchmark, not competitive
    with a real embedding model. See docs/hybrid-retrieval.md for the
    plug-in shape (LangChain, LlamaIndex, pgvector, Chroma, ...).
  - Per-channel failures degrade gracefully: a failing vector backend
    doesn't kill the merge; the structural + BM25 hits still surface.
`

// runHybrid parses `yactt hybrid` flags, loads the repo, and emits
// the merged retrieval result. Mirrors runChunk's flag-parsing
// style (positional + "--key value"/"--key=value") so the CLI is
// internally consistent.
func runHybrid(args []string) error {
	var (
		opts       hybrid.Options
		repoPath   string
		repoSet    bool
		querySet   bool
		explain    bool
		limitSet   bool
		channelsCS string // empty means "all"
	)
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--help" || a == "-h":
			fmt.Print(hybridUsage)
			return nil
		case a == "--repo":
			if i+1 >= len(args) {
				return errors.New("hybrid: --repo requires a path argument")
			}
			repoPath = args[i+1]
			repoSet = true
			i++
		case strings.HasPrefix(a, "--repo="):
			repoPath = strings.TrimPrefix(a, "--repo=")
			repoSet = true
		case a == "--query":
			if i+1 >= len(args) {
				return errors.New("hybrid: --query requires a string argument")
			}
			opts.Query = args[i+1]
			querySet = true
			i++
		case strings.HasPrefix(a, "--query="):
			opts.Query = strings.TrimPrefix(a, "--query=")
			querySet = true
		case a == "--limit":
			if i+1 >= len(args) {
				return errors.New("hybrid: --limit requires a value")
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil {
				return fmt.Errorf("hybrid: --limit: %w", err)
			}
			opts.Limit = n
			limitSet = true
			i++
		case strings.HasPrefix(a, "--limit="):
			n, err := strconv.Atoi(strings.TrimPrefix(a, "--limit="))
			if err != nil {
				return fmt.Errorf("hybrid: --limit: %w", err)
			}
			opts.Limit = n
			limitSet = true
		case a == "--channels":
			if i+1 >= len(args) {
				return errors.New("hybrid: --channels requires a comma-separated value")
			}
			channelsCS = args[i+1]
			i++
		case strings.HasPrefix(a, "--channels="):
			channelsCS = strings.TrimPrefix(a, "--channels=")
		case a == "--with-tests":
			opts.ChunkerOpts.WithTests = true
		case a == "--explain":
			explain = true
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("hybrid: unknown flag: %s", a)
		default:
			return fmt.Errorf("hybrid: unexpected positional argument: %s", a)
		}
	}
	if !repoSet {
		return errors.New("hybrid: --repo <path> is required\n\n" + hybridUsage)
	}
	if !querySet {
		return errors.New("hybrid: --query <string> is required\n\n" + hybridUsage)
	}
	if !limitSet {
		opts.Limit = 10
	}

	channels, err := parseChannels(channelsCS)
	if err != nil {
		return err
	}
	opts.Channels = channels
	// The CLI ships the stdlib reference vector backend. Production
	// users embed yactt as a Go library (cmd/yactt-hybrid would be
	// the dedicated binary form once we have a build-time injection
	// point — see docs/plans/issue-37-hybrid-retrieval.md). The CLI
	// stays zero-dep so it works out of the box.
	if opts.Channels.Vector {
		opts.Vector = hybrid.NewBagOfTokens()
	}

	abs, err := filepath.Abs(repoPath)
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
	opts.Repo = repo

	out := io.Writer(os.Stdout)
	return runHybridPipeline(repo, opts, explain, out, os.Stderr)
}

// parseChannels parses the comma-separated channel list. Empty
// string → all three on (production default). Unknown channels are
// rejected with a clear error so typos surface immediately.
func parseChannels(s string) (hybrid.Channels, error) {
	if s == "" {
		return hybrid.AllChannels(), nil
	}
	c := hybrid.Channels{}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		switch p {
		case "":
			continue
		case "structural":
			c.Structural = true
		case "bm25":
			c.BM25 = true
		case "vector":
			c.Vector = true
		default:
			return c, fmt.Errorf("hybrid: unknown channel %q (want structural|bm25|vector)", p)
		}
	}
	if !c.Structural && !c.BM25 && !c.Vector {
		return c, errors.New("hybrid: --channels must include at least one of structural|bm25|vector")
	}
	return c, nil
}

// runHybridPipeline is the testable core of runHybrid. It runs the
// orchestrator and writes either the merged JSON or the per-channel
// explain view. Splitting it out keeps the CLI's flag-parsing out
// of the unit-test path.
func runHybridPipeline(r *store.Repo, opts hybrid.Options, explain bool, stdout, stderr io.Writer) error {
	if explain {
		out, err := hybrid.Explain(context.Background(), opts)
		if err != nil {
			return fmt.Errorf("hybrid: %w", err)
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Fprintln(stdout, string(b))
		fmt.Fprintf(stderr, "hybrid: %d channels, %d merged hits\n", len(out)-1, len(out["rrf"]))
		return nil
	}
	hits, err := hybrid.Run(context.Background(), opts)
	if err != nil {
		return fmt.Errorf("hybrid: %w", err)
	}
	b, _ := json.MarshalIndent(map[string]any{"results": hits}, "", "  ")
	fmt.Fprintln(stdout, string(b))
	fmt.Fprintf(stderr, "hybrid: %d hits\n", len(hits))
	return nil
}

// runMCPServe starts the MCP server on stdio. After the
// project-reference migration, the server runs in registry
// mode only — there is no single-repo boot path. Agents call
// `index_repository` with a file:// URI to load a project
// before invoking any code-intel tool.
//
// Flags:
//
//	--audit-log=<path>   Write one JSON audit line per tools/call dispatch
//	                     to <path>. The file is created with mode 0600 and
//	                     appended on subsequent invocations. Omit to disable
//	                     per-tool audit; the startup line still goes to stderr
//	                     on the first index_repository success per process.
func runMCPServe(args []string) error {
	var auditPath string
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
			return fmt.Errorf("mcp serve: no longer takes a positional path; tools accept a file:// project URI in their args (got %q)", a)
		}
	}

	// Registry handle is the only long-lived state. Code-intel
	// tools resolve project URIs through it on every call.
	regPath := registry.DefaultPath()
	if regPath == "" {
		return errors.New("mcp serve: cannot resolve $XDG_CACHE_HOME or $HOME; set XDG_CACHE_HOME")
	}
	reg := registry.New(regPath)

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
		mcp.ProtocolVersion,
		os.Stdout,
		func() (io.Reader, error) { return os.Stdin, nil },
	)
	if auditLogger != nil {
		srv.WithAudit(auditLogger, audit.ExtractPaths)
	}

	// Build the audit + TOFU hooks for index_repository. They run
	// at most once per process (memoised inside IndexRepository).
	binSHA, _ := audit.BinarySHA256(binaryPath())
	emitStartup := func(info audit.Startup) error {
		info.Version = version
		info.BinarySHA256 = binSHA
		return audit.EmitStartup(os.Stderr, info)
	}
	warnTrust := func() { warnInstallTrustChain(version, binSHA) }

	tool.RegisterAllTools(srv, reg, emitStartup, warnTrust)

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


const mcpServeHTTPUsage = `yactt mcp serve-http — run the MCP server as a persistent HTTP daemon.

Usage:
  yactt mcp serve-http [options]

Options:
  --port=<n>              TCP port to listen on (default 8080; 0 lets the kernel pick).
  --bind=<addr>           Bind address (default 127.0.0.1; use 0.0.0.0 for non-loopback).
  --auth-token=<token>    Require "Authorization: Bearer <token>" on every request.
  --audit-log=<path>      Append one JSON line per tool call to <path> (mode 0600).
  --registry=<path>       Registry file location (default $XDG_CACHE_HOME/yactt/projects.json).
  --shutdown-grace=<dur>  Grace window for in-flight requests on SIGTERM (default 10s).
  --max-sessions=<n>      Cap on concurrent sessions (default 256).
  --idle-timeout=<dur>    Idle reap threshold (default 5m).
  -h, --help              Show this message.

Endpoints:
  POST   /mcp                  — JSON-RPC request (tools resolve project via file:// URI in args)
  GET    /mcp                  — open SSE stream (requires session)
  DELETE /mcp                  — terminate session
  GET    /healthz              — liveness probe (unauthenticated)

By default the daemon binds to 127.0.0.1 and requires no auth. Use --bind=0.0.0.0
together with --auth-token to expose to a network; without --auth-token on a
non-loopback bind the daemon refuses all requests with 403 Forbidden.
`

// runMCPServeHTTP parses `yactt mcp serve-http` flags, builds the
// daemon, and blocks until SIGINT/SIGTERM or a fatal error.
func runMCPServeHTTP(args []string) error {
	cfg := httptransport.ServerConfig{
		ProtocolName: "yactt",
		Version:      version,
		ProtocolVer:  httptransport.ExpectedProtocol,
		Port:         8080,
		Bind:         "127.0.0.1",
		MaxSessions:  256,
		IdleTimeout:  5 * time.Minute,
		ShutdownGrace: 10 * time.Second,
	}
	var registryPath string
	var auditPath string

	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--help" || a == "-h":
			fmt.Print(mcpServeHTTPUsage)
			return nil
		case a == "--port":
			if i+1 >= len(args) {
				return errors.New("--port requires a value")
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil {
				return fmt.Errorf("--port: %w", err)
			}
			cfg.Port = n
			i++
		case strings.HasPrefix(a, "--port="):
			n, err := strconv.Atoi(strings.TrimPrefix(a, "--port="))
			if err != nil {
				return fmt.Errorf("--port: %w", err)
			}
			cfg.Port = n
		case a == "--bind":
			if i+1 >= len(args) {
				return errors.New("--bind requires an address")
			}
			cfg.Bind = args[i+1]
			i++
		case strings.HasPrefix(a, "--bind="):
			cfg.Bind = strings.TrimPrefix(a, "--bind=")
		case a == "--auth-token":
			if i+1 >= len(args) {
				return errors.New("--auth-token requires a value")
			}
			cfg.Token = args[i+1]
			i++
		case strings.HasPrefix(a, "--auth-token="):
			cfg.Token = strings.TrimPrefix(a, "--auth-token=")
		case a == "--registry":
			if i+1 >= len(args) {
				return errors.New("--registry requires a path")
			}
			registryPath = args[i+1]
			i++
		case strings.HasPrefix(a, "--registry="):
			registryPath = strings.TrimPrefix(a, "--registry=")
		case a == "--audit-log":
			if i+1 >= len(args) {
				return errors.New("--audit-log requires a path")
			}
			auditPath = args[i+1]
			i++
		case strings.HasPrefix(a, "--audit-log="):
			auditPath = strings.TrimPrefix(a, "--audit-log=")
		case a == "--shutdown-grace":
			if i+1 >= len(args) {
				return errors.New("--shutdown-grace requires a duration")
			}
			d, err := time.ParseDuration(args[i+1])
			if err != nil {
				return fmt.Errorf("--shutdown-grace: %w", err)
			}
			cfg.ShutdownGrace = d
			i++
		case strings.HasPrefix(a, "--shutdown-grace="):
			d, err := time.ParseDuration(strings.TrimPrefix(a, "--shutdown-grace="))
			if err != nil {
				return fmt.Errorf("--shutdown-grace: %w", err)
			}
			cfg.ShutdownGrace = d
		case a == "--max-sessions":
			if i+1 >= len(args) {
				return errors.New("--max-sessions requires a number")
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil {
				return fmt.Errorf("--max-sessions: %w", err)
			}
			cfg.MaxSessions = n
			i++
		case strings.HasPrefix(a, "--max-sessions="):
			n, err := strconv.Atoi(strings.TrimPrefix(a, "--max-sessions="))
			if err != nil {
				return fmt.Errorf("--max-sessions: %w", err)
			}
			cfg.MaxSessions = n
		case a == "--idle-timeout":
			if i+1 >= len(args) {
				return errors.New("--idle-timeout requires a duration")
			}
			d, err := time.ParseDuration(args[i+1])
			if err != nil {
				return fmt.Errorf("--idle-timeout: %w", err)
			}
			cfg.IdleTimeout = d
			i++
		case strings.HasPrefix(a, "--idle-timeout="):
			d, err := time.ParseDuration(strings.TrimPrefix(a, "--idle-timeout="))
			if err != nil {
				return fmt.Errorf("--idle-timeout: %w", err)
			}
			cfg.IdleTimeout = d
		default:
			return fmt.Errorf("unknown flag: %s", a)
		}
	}

	if registryPath == "" {
		registryPath = registry.DefaultPath()
		if registryPath == "" {
			return errors.New("mcp serve-http: cannot resolve registry path; set XDG_CACHE_HOME or pass --registry")
		}
	}
	reg := registry.New(registryPath)

	// Bind/auth warning — operator owns the decision.
	if cfg.Bind != "" && cfg.Bind != "127.0.0.1" && cfg.Bind != "::1" && cfg.Bind != "localhost" && cfg.Token == "" {
		fmt.Fprintln(os.Stderr,
			"WARNING: bound to non-loopback address without --auth-token; the daemon is unauthenticated. "+
				"Set --auth-token or reverse-proxy through an authenticated gateway.")
	}

	// Optional per-tool audit
	var auditLogger *audit.Logger
	var auditCloser io.Closer
	if auditPath != "" {
		var err error
		auditLogger, auditCloser, err = audit.NewFileLogger(auditPath)
		if err != nil {
			return fmt.Errorf("open audit log %s: %w", auditPath, err)
		}
		defer func() {
			if auditCloser != nil {
				_ = auditCloser.Close()
			}
		}()
	}

	// Startup emit on stderr — always.
	fmt.Fprintf(os.Stderr,
		"yactt mcp serve-http listening on %s:%d protocol=%s registry=%s sessions=%d idle=%s grace=%s\n",
		cfg.Bind, cfg.Port, cfg.ProtocolVer, registryPath, cfg.MaxSessions, cfg.IdleTimeout, cfg.ShutdownGrace,
	)

	daemon := httptransport.NewServer(cfg, reg)
	if auditLogger != nil {
		daemon.WithAudit(auditLogger)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- daemon.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), cfg.ShutdownGrace)
		defer cancelShutdown()
		return daemon.Shutdown(shutdownCtx)
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
