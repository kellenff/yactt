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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kellenff/yactt/internal/audit"
	"github.com/kellenff/yactt/internal/mcp"
	"github.com/kellenff/yactt/internal/parser"
	"github.com/kellenff/yactt/internal/persisted"
	"github.com/kellenff/yactt/internal/registry"
	"github.com/kellenff/yactt/internal/store"
	"github.com/kellenff/yactt/internal/tool"

	"github.com/kellenff/yactt/internal/chunker"
	"github.com/kellenff/yactt/internal/hybrid"
)

const usage = `yactt — federated code intelligence for AI agents

Usage:
  yactt overview <path>                   Print the top of the tree for a repo.
  yactt mcp serve [path] [--audit-log=F]  Run the MCP server on stdio.
                                          With a path: serves that repo's tools.
                                          Without: serves the registry (list_projects,
                                          index_repository, index_status, delete_project).
                                          --audit-log=F writes one JSON line per tool call to F.
  yactt chunk --repo <path> [options]     Emit AST-bounded NDJSON chunks to stdout.
                                          See "yactt chunk --help" for options.
  yactt hybrid --repo <path> --query Q    Run hybrid retrieval (structural + BM25 + vector,
                                          merged with RRF). See "yactt hybrid --help".
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
		Name: "list_projects", Description: "Enumerate every project in the registry, sorted by path. Call first when an agent joins an MCP session and doesn't yet know which repos are available.",
		InputSchema: tool.ListProjectsSchema, OutputSchema: tool.ListProjectsOutputSchema,
		Handler: tool.ListProjects(reg),
	})
	srv.RegisterTool(mcp.ToolDef{
		Name: "index_repository", Description: "Required first call in registry mode: walk a repo at `path`, write an entry to the registry, return the row. After this returns, the 16 repo-bound tools appear in `tools/list`. Mode knob is accepted (only `full` is wired today).",
		InputSchema: tool.IndexRepositorySchema, OutputSchema: tool.IndexRepositoryOutputSchema,
		Handler: tool.IndexRepository(reg),
	})
	srv.RegisterTool(mcp.ToolDef{
		Name: "index_status", Description: "Registry row + per-repo cache freshness for `path`. Use this to check whether a repo is already indexed (`cacheFresh=true`) or whether `index_repository` needs to run first (`cacheFresh=false`).",
		InputSchema: tool.IndexStatusSchema, OutputSchema: tool.IndexStatusOutputSchema,
		Handler: tool.IndexStatus(reg),
	})
	srv.RegisterTool(mcp.ToolDef{
		Name: "delete_project", Description: "Evict `path` from the registry and remove its per-repo cache directory. Idempotent on missing rows — safe to retry on a stale or half-deleted entry.",
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

	treeOverview := tool.TreeOverview(reg)
	nodeGet := tool.GetNode(reg)
	nodeSource := tool.NodeSource(reg)
	nodeEdges := tool.NodeEdges(reg)
	search := tool.Search(reg)
	editImpact := tool.EditImpact(reg)
	findSymbol := tool.FindSymbol(reg)
	getSymbolsOverview := tool.GetSymbolsOverview(reg)
	findCode := tool.FindCode(reg)
	findReferencingSymbols := tool.FindReferencingSymbols(reg)
	getGraphSchema := tool.GetGraphSchema()
	getCodeSnippet := tool.GetCodeSnippet(reg)
	getArchitecture := tool.GetArchitecture(reg)
	queryGraph := tool.QueryGraph(reg)
	searchCode := tool.SearchCode(reg)
	detectChanges := tool.DetectChanges(reg)

	tools := []mcp.ToolDef{
		{Name: "tree_overview", Description: "First call when orienting: map the repo structure (packages, files, top-level symbols). Tune `depth` (1–6, default 2); narrow with `scope` (absolute path under repo root) to drill into a package or subdir. Truncates at 16 KiB — start shallow, drill with `get_symbols_overview` once you know the path.", InputSchema: tool.TreeOverviewSchema, OutputSchema: tool.TreeOverviewOutputSchema, Handler: treeOverview},
		{Name: "node_get", Description: "After `tree_overview` / `find_symbol` / `search` returns a stable `id`, pull specific layers (`summary`, `signature`, `body`, `source`, `tokens`). Cheap → expensive: start with `summary`, escalate only when you need more. Don't call without an `id`.", InputSchema: tool.GetNodeSchema, OutputSchema: tool.GetNodeOutputSchema, Handler: nodeGet},
		{Name: "node_source", Description: "Lossless source text for a node (or a whole file via `id=file:<path>`). Use when you need the verbatim text, not a parsed layer. Pass `range=[start,end]` to bound.", InputSchema: tool.NodeSourceSchema, OutputSchema: tool.NodeSourceOutputSchema, Handler: nodeSource},
		{Name: "node_edges", Description: "Single-hop callers / callees / tests / overrides / imports for a node. For transitive (>1 hop) caller/callee chains, use `query_graph` instead — one `query_graph` call replaces a loop of `node_edges` calls.", InputSchema: tool.NodeEdgesSchema, OutputSchema: tool.NodeEdgesOutputSchema, Handler: nodeEdges},
		{Name: "search", Description: "BM25 over symbol name + doc-comment — best for fuzzy 'is there anything called *Foo*?' discovery. Not regex; use `find_code(pattern_kind=regex)` for line-shaped patterns. Lower limit than `find_symbol` (default 10) so prefer it for free-form search, `find_symbol` for exact lookup.", InputSchema: tool.SearchSchema, OutputSchema: tool.SearchOutputSchema, Handler: search},
		{Name: "edit_impact", Description: "Required before any rename: returns the blast radius (callers, tests, overrides) without applying. Pair with the Edit tool afterward. Pass renames as [{id:..., new_name:...}].", InputSchema: tool.EditImpactSchema, OutputSchema: tool.EditImpactOutputSchema, Handler: editImpact},
		{Name: "find_symbol", Description: "Glob over the name-path (`pkg.Name` or `pkg/Name`, with `*` allowed) when you know roughly what something is called. Default limit 20. On miss, the response includes a `suggestions` field with edit-distance matches — try those before falling back to `search`.", InputSchema: tool.FindSymbolSchema, OutputSchema: tool.FindSymbolOutputSchema, Handler: findSymbol},
		{Name: "get_symbols_overview", Description: "When you have a file path (not a symbol id): get the top-level symbols of that file. Cheaper than a loop of `node_get` calls. Use after `tree_overview(scope=...)` or `search` to drill into a known file.", InputSchema: tool.GetSymbolsOverviewSchema, OutputSchema: tool.GetSymbolsOverviewOutputSchema, Handler: getSymbolsOverview},
		{Name: "find_code", Description: "Line-shaped patterns: regex (`pattern_kind=regex`) for grep-ish work, or tree-sitter AST patterns (`pattern_kind=tree_sitter`) for code-shaped queries (e.g. 'all calls to Foo'). Use `scope` to limit to a directory. For 'which functions handle *X*?' use `search_code` instead.", InputSchema: tool.FindCodeSchema, OutputSchema: tool.FindCodeOutputSchema, Handler: findCode},
		{Name: "search_code", Description: "When you want 'which functions handle *X*?': wraps `find_code` and groups matches by enclosing function, deduped, ranked by structural importance. Best tool for 'where is X handled' questions. Use `find_code` when you need raw matches without the grouping.", InputSchema: tool.SearchCodeSchema, OutputSchema: tool.SearchCodeOutputSchema, Handler: searchCode},
		{Name: "find_referencing_symbols", Description: "Single-hop symbol-addressed alias of `node_edges`. Accepts a node ID OR a name_path. For multi-hop (transitive) callers/callees, use `query_graph` instead — `find_referencing_symbols` only returns direct neighbours.", InputSchema: tool.FindReferencingSymbolsSchema, OutputSchema: tool.FindReferencingSymbolsOutputSchema, Handler: findReferencingSymbols},
		{Name: "get_graph_schema", Description: "List the canonical node kinds (FUNCTION/METHOD/CLASS/MODULE/FILE/PACKAGE/REPO), edge kinds (callers/callees/tests/overrides/imports), and layer names. Call this first when you need to write a `query_graph` filter or any kind-aware query — no need to hardcode strings.", InputSchema: tool.GetGraphSchemaSchema, OutputSchema: tool.GetGraphSchemaOutputSchema, Handler: getGraphSchema},
		{Name: "get_code_snippet", Description: "Source slice for a symbol by stable id OR qualified name_path. One call replaces find_symbol+node_source. Use when you already know what you want and just need the body. On miss, the response includes a `suggestions` field with edit-distance matches.", InputSchema: tool.GetCodeSnippetSchema, OutputSchema: tool.GetCodeSnippetOutputSchema, Handler: getCodeSnippet},
		{Name: "get_architecture", Description: "Repo-level health at a glance: dead-code candidates, hot spots (high fan-out), import cycles, language breakdown, package list. Run once after `tree_overview` for a first-look snapshot. Capped per-section (dead-code ≤50, cycles ≤10) — re-call with filters if you need more.", InputSchema: tool.GetArchitectureSchema, OutputSchema: tool.GetArchitectureOutputSchema, Handler: getArchitecture},
		{Name: "query_graph", Description: "Trace reachability across multiple hops — the right tool for 'who transitively depends on X?' or 'what does X transitively reach?'. Single-seed (`from`) or multi-seed (`seeds`) — multi-seed is the GraphRAG shape that takes a vector-retriever's top-K hits as input and emits a ranked subgraph (per-row `score` = weight × 1/(1+depth)) ready for packing. One call replaces a loop of `node_edges` calls. `depth` 1–5 (default 2); cost-capped at 1000 rows / 5 s / 5000 visited nodes / 50 seeds so large traversals terminate safely. Pass `follow:[\"callers\"]` for transitive callers, `[\"callees\"]` for transitive callees, or alternate via `follow:[\"callees\",\"callers\"]`.", InputSchema: tool.QueryGraphSchema, OutputSchema: tool.QueryGraphOutputSchema, Handler: queryGraph},
		{Name: "detect_changes", Description: "Impact of a git-ref diff: changed files, hunks, enclosing function/method per hunk, and callers/tests/overrides per affected symbol. Accepts {base,head} or {since} (Issue #11).", InputSchema: tool.DetectChangesSchema, OutputSchema: tool.DetectChangesOutputSchema, Handler: detectChanges},
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
		"search_code":              searchCode,
		"find_referencing_symbols": findReferencingSymbols,
		"get_graph_schema":         getGraphSchema,
		"get_code_snippet":         getCodeSnippet,
		"get_architecture":         getArchitecture,
		"query_graph":              queryGraph,
		"detect_changes":           detectChanges,
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
