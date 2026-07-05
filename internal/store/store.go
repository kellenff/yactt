// Package store is the per-repo orchestration layer.
//
// It owns:
//   - the file index (paths → *source.File), populated by Load and stale-checked
//     against mtime via the layered cache.
//   - the symbol index (per-file symbol declarations), used for `tree_overview`,
//     `get_symbols_overview`, and the search/find_symbol backends.
//   - node materialization on demand: given a Node ID, layer materializers pull
//     the parsed file from the cache, walk the AST to locate the symbol, and
//     emit the requested layer.
//
// The store is intentionally narrow — a Repo is a value object, not a service.
// Higher layers (search, tool handlers) own their own logic and ask the store
// for parsed files and symbol lists.
package store

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kellenff/yactt/internal/cache"
	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/lsp"
	"github.com/kellenff/yactt/internal/parser"
	"github.com/kellenff/yactt/internal/source"
)

// ErrNotFound is returned when a requested node / file / symbol doesn't exist.
var ErrNotFound = errors.New("store: not found")

// Repo is the per-root load of a code repository. It is safe for concurrent
// use — symbol/file accessors take the internal mutex.
//
// When the load can locate one or more language servers (gopls for Go,
// typescript-language-server for TS/JS) on PATH, the repo carries live
// *lsp.Client entries in `lsp`, keyed by parser.Name. Materializers consult
// the right client per file; tree-sitter is the unconditional floor.
//
// A nil entry (or absent key) for a language means "no server for this
// language at Load time" — materializers fall through to tree-sitter with
// the existing `no-lsp-installed` marker. The single-slot `LSP()` /
// `LSPVersion()` accessors preserve the original Go-only contract and are
// the legacy shortcut to `lsp[parser.LangGo]`.
type Repo struct {
	root          string
	cache         *cache.Cache
	mu            sync.RWMutex
	files         map[string]*source.File
	symbolsByPath map[string][]parser.Symbol
	index         *symIndex
	rootPkg       string
	prov          domain.Provenance

	// LSP subgraph (Tier 1). Each map is keyed by parser.Name; the keys
	// present at any time are the languages whose server started
	// successfully. Nil keys (or absent entries) mean "no server for
	// that language" — materializers fall through to tree-sitter.
	lsp         map[parser.Name]*lsp.Client
	lspTools    map[parser.Name]string // "gopls" / "typescript-language-server" / ...
	lspVersions map[parser.Name]string
}

// Load scans root, parses each source file matching a known language, and
// returns a *Repo. Files are parsed in parallel; failures on a single file
// are skipped and surface in the returned errors list.
func Load(root string) (*Repo, []error, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, nil, fmt.Errorf("store: %w", err)
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, nil, fmt.Errorf("store: %w", err)
	}
	if !st.IsDir() {
		return nil, nil, fmt.Errorf("store: %s is not a directory", abs)
	}

	r := &Repo{
		root:          abs,
		cache:         cache.New(),
		files:         make(map[string]*source.File),
		symbolsByPath: make(map[string][]parser.Symbol),
		prov:          domain.NewProvenance("tree-sitter", "v0.0.0-20240827"),
		lsp:           make(map[parser.Name]*lsp.Client),
		lspTools:      make(map[parser.Name]string),
		lspVersions:   make(map[parser.Name]string),
	}

	var (
		errsMu sync.Mutex
		errs   []error
	)
	appendErr := func(e error) {
		errsMu.Lock()
		defer errsMu.Unlock()
		errs = append(errs, e)
	}

	walk := func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			name := d.Name()
			// Skip hidden / common-noise dirs quickly.
			if path != abs && (name == ".git" || name == "vendor" || name == "node_modules" || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		lang, lerr := parser.Detect(path)
		if lerr != nil {
			// Not a source file we can parse — skip silently.
			return nil
		}
		f, ferr := source.LoadFile(path, lang)
		if ferr != nil {
			appendErr(ferr)
			return nil
		}
		syms, serr := parser.ExtractSymbols(lang, f.Root, f.Bytes)
		if serr != nil {
			// Language not wired up — record the file but no symbols.
			syms = nil
		}
		r.mu.Lock()
		r.files[path] = f
		r.symbolsByPath[path] = syms
		r.mu.Unlock()
		return nil
	}

	if err := filepath.WalkDir(abs, walk); err != nil {
		appendErr(err)
	}

	if r.detectGoModule() {
		_ = r.inferRootPackage()
	}

	r.rebuildIndex()

	// Opportunistic LSP startup. Each supported server is tried in turn;
	// successful starts populate the per-language maps, failures leave
	// the corresponding slot nil so materializers fall through to
	// tree-sitter with the existing `no-lsp-installed` marker. We use
	// a short overall timeout because a hung server (slow indexing,
	// etc.) shouldn't stall Load.
	startCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	logf := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "lsp: "+format+"\n", args...)
	}

	tryStart := func(lang parser.Name, toolName string, start func(context.Context, string, lsp.Options) (*lsp.Client, error)) {
		client, err := start(startCtx, abs, lsp.Options{
			Timeout:      500 * time.Millisecond,
			Concurrency:  8,
			CloseTimeout: 5 * time.Second,
			Logf:         logf,
		})
		if err != nil {
			logf("startup declined for %s: %v", toolName, err)
			return
		}
		r.lsp[lang] = client
		r.lspTools[lang] = toolName
		r.lspVersions[lang] = client.Version()
	}

	tryStart(parser.LangGo, "gopls", lsp.Start)
	// typescript-language-server speaks both TypeScript and JavaScript
	// from a single workspace — register both keys against the same
	// client so a `.ts` file and a `.js` file both route to it.
	if tsClient, err := lsp.StartTypeScript(startCtx, abs, lsp.Options{
		Timeout:      500 * time.Millisecond,
		Concurrency:  8,
		CloseTimeout: 5 * time.Second,
		Logf:         logf,
	}); err != nil {
		logf("startup declined for typescript-language-server: %v", err)
	} else {
		r.lsp[parser.LangTypeScript] = tsClient
		r.lsp[parser.LangJavaScript] = tsClient
		r.lspTools[parser.LangTypeScript] = "typescript-language-server"
		r.lspTools[parser.LangJavaScript] = "typescript-language-server"
		r.lspVersions[parser.LangTypeScript] = tsClient.Version()
		r.lspVersions[parser.LangJavaScript] = tsClient.Version()
	}

	// Eagerly open every parsed file in the server that knows about its
	// language. Without this warm-up, both gopls and typescript-language-
	// server lazily index on the first request and that request can
	// exceed the 500ms per-request budget. We use a 5-second per-file
	// deadline: the first request against an unindexed file routinely
	// takes 1–3 s while the server parses and type-checks, well past the
	// production 500 ms budget.
	warmCtx, warmCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer warmCancel()
	warmFilesFor := func(client *lsp.Client, accept func(string) (lspLang string, ok bool)) {
		if client == nil {
			return
		}
		var files []lsp.OpenFile
		for p, f := range r.files {
			langID, ok := accept(p)
			if !ok {
				continue
			}
			files = append(files, lsp.OpenFile{
				Path:     p,
				Language: langID,
				Text:     string(f.Bytes),
			})
		}
		if n := lsp.OpenWorkspace(warmCtx, client, files, lsp.OpenWorkspaceOptions{
			PerFileTimeout: 5 * time.Second,
			Logf:           logf,
		}); n > 0 {
			logf("warmed %d files", n)
		}
	}
	warmFilesFor(r.lsp[parser.LangGo], func(p string) (string, bool) {
		if strings.HasSuffix(p, ".go") {
			return "go", true
		}
		return "", false
	})
	if tsClient := r.lsp[parser.LangTypeScript]; tsClient != nil {
		warmFilesFor(tsClient, func(p string) (string, bool) {
			switch ext := strings.ToLower(filepath.Ext(p)); ext {
			case ".ts", ".tsx", ".mts", ".cts":
				return "typescript", true
			case ".js", ".jsx", ".mjs", ".cjs":
				return "javascript", true
			}
			return "", false
		})
	}

	return r, errs, nil
}

// LSP returns the live *lsp.Client for Go (gopls), or nil when the
// server was not available at Load time. Legacy single-language
// accessor preserved for backward compatibility — tests that inject a
// stub gopls use this to confirm the Tier-1 path.
func (r *Repo) LSP() *lsp.Client { return r.lsp[parser.LangGo] }

// LSPVersion returns the gopls version pinned during the LSP initialize
// handshake, or "" when no gopls is wired. Legacy accessor.
func (r *Repo) LSPVersion() string { return r.lspVersions[parser.LangGo] }

// LSPForLang returns the (client, tool, version) triple for the named
// language, or (nil, "", "") when no server is wired for that language.
// The tool name ("gopls", "typescript-language-server", ...) is what
// downstream consumers stamp into `Provenance.Tool`.
func (r *Repo) LSPForLang(lang parser.Name) (*lsp.Client, string, string) {
	return r.lsp[lang], r.lspTools[lang], r.lspVersions[lang]
}

// LSPForFile looks up the LSP client by the file's detected language.
// Returns (nil, "", "") for files outside any supported language or
// when no server is wired. Used by materializers and edge scanners that
// route per-file.
func (r *Repo) LSPForFile(path string) (*lsp.Client, string, string) {
	lang, err := parser.Detect(path)
	if err != nil {
		return nil, "", ""
	}
	return r.LSPForLang(lang.Name())
}

// Close shuts every wired LSP client down cleanly (sends LSP shutdown +
// exit notifications, kills the child on timeout). Idempotent. Safe to
// call when no clients are wired.
//
// Callers should defer `r.Close()` right after `store.Load` so server
// children are always reaped, regardless of the exit path.
func (r *Repo) Close() error {
	var firstErr error
	for _, c := range r.lsp {
		if c == nil {
			continue
		}
		if err := c.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	r.lsp = nil
	r.lspTools = nil
	r.lspVersions = nil
	return firstErr
}

// DetachLSPForTest removes the gopls LSP client from the repo. Used
// only in acceptance tests that need to assert the tree-sitter fallback
// path independent of whether gopls happens to be on PATH.
//
// Not thread-safe — callers should serialize. Production code should
// use Close().
func (r *Repo) DetachLSPForTest() {
	if c := r.lsp[parser.LangGo]; c != nil {
		_ = c.Close()
	}
	delete(r.lsp, parser.LangGo)
	delete(r.lspTools, parser.LangGo)
	delete(r.lspVersions, parser.LangGo)
}

// AttachLSPForTest attaches a pre-built gopls stub to the repo. The
// client is owned by the repo afterwards (Close will shut it down).
func (r *Repo) AttachLSPForTest(c *lsp.Client) {
	if c == nil {
		return
	}
	if old := r.lsp[parser.LangGo]; old != nil {
		_ = old.Close()
	}
	r.lsp[parser.LangGo] = c
	r.lspTools[parser.LangGo] = "gopls"
	r.lspVersions[parser.LangGo] = c.Version()
}

// Root returns the absolute path of the repository root.
func (r *Repo) Root() string { return r.root }

// PackagePath returns the dotted directory path for an absolute file path
// under root. Empty string when `p` is at the repo root (no directory).
// Single source of truth for the package-path convention.
func PackagePath(root, p string) string {
	rel := strings.TrimPrefix(p, root)
	rel = strings.TrimPrefix(rel, "/")
	parts := strings.Split(rel, "/")
	if len(parts) <= 1 {
		return ""
	}
	return strings.Join(parts[:len(parts)-1], ".")
}

// CachedFile returns the parsed source.File for path, taking a fresh read if
// the on-disk content has changed. Returns ErrNotFound for files outside the
// repo.
func (r *Repo) CachedFile(path string) (*source.File, error) {
	if !strings.HasPrefix(path, r.root) {
		return nil, ErrNotFound
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, ErrNotFound
	}
	mtime := st.ModTime().UnixNano()
	if f, err := r.cache.GetFile(path, mtime); err == nil {
		return f, nil
	}
	lang, lerr := parser.Detect(path)
	if lerr != nil {
		return nil, lerr
	}
	f, err := source.LoadFile(path, lang)
	if err != nil {
		return nil, err
	}
	r.cache.PutFile(f)
	r.mu.Lock()
	r.files[path] = f
	if syms, serr := parser.ExtractSymbols(lang, f.Root, f.Bytes); serr == nil {
		r.symbolsByPath[path] = syms
	}
	r.mu.Unlock()
	return f, nil
}

// Symbols returns the cached top-level symbols for path.
func (r *Repo) Symbols(path string) []parser.Symbol {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.symbolsByPath[path]
}

// DocComment returns the contiguous `//` doc block immediately above the
// symbol's declaration, or "" if there isn't one. Callers that need docs for
// every symbol in a file should prefer DocComments — it amortizes the per-file
// load across the whole declaration list.
func (r *Repo) DocComment(path string, sym parser.Symbol) string {
	f, err := r.CachedFile(path)
	if err != nil {
		return ""
	}
	return extractDocComment(f, sym)
}

// DocComments returns the doc block for every declaration in decls, keyed by
// symbol name. Performs one CachedFile load for the whole batch so callers
// iterating many symbols per path don't pay one os.Stat per symbol.
//
// Returns an empty map when the file can't be loaded; callers can treat the
// result as "no doc available" without a separate error path. The map is
// freshly allocated and safe for the caller to retain.
func (r *Repo) DocComments(path string, decls []parser.Symbol) map[string]string {
	out := make(map[string]string, len(decls))
	f, err := r.CachedFile(path)
	if err != nil {
		return out
	}
	for _, sym := range decls {
		if sym.Name == "" {
			continue
		}
		out[sym.Name] = extractDocComment(f, sym)
	}
	return out
}

// SymbolsByPath returns a snapshot of every file's symbols.
func (r *Repo) SymbolsByPath() map[string][]parser.Symbol {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string][]parser.Symbol, len(r.symbolsByPath))
	for k, v := range r.symbolsByPath {
		out[k] = v
	}
	return out
}

// Files returns the absolute paths of every parsed file.
func (r *Repo) Files() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.files))
	for p := range r.files {
		out = append(out, p)
	}
	return out
}

// Provenance returns the store-wide provenance for the latest load.
func (r *Repo) Provenance() domain.Provenance { return r.prov }

// detectGoModule reports whether a go.mod exists at the root.
func (r *Repo) detectGoModule() bool {
	_, err := os.Stat(filepath.Join(r.root, "go.mod"))
	return err == nil
}

// inferRootPackage reads module name from go.mod if present.
func (r *Repo) inferRootPackage() error {
	raw, err := os.ReadFile(filepath.Join(r.root, "go.mod"))
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			r.rootPkg = strings.TrimSpace(strings.TrimPrefix(line, "module "))
			return nil
		}
	}
	return nil
}

// RootPackage returns the module path declared in go.mod, or "" if absent.
func (r *Repo) RootPackage() string { return r.rootPkg }

// ReloadInvalidate forces a fresh parse of one file path, ignoring the cache.
// Useful when a tool wants to ensure the next read sees on-disk truth.
func (r *Repo) ReloadInvalidate(path string) error {
	if !strings.HasPrefix(path, r.root) {
		return ErrNotFound
	}
	lang, lerr := parser.Detect(path)
	if lerr != nil {
		return lerr
	}
	f, err := source.LoadFile(path, lang)
	if err != nil {
		return err
	}
	r.cache.PutFile(f)
	r.mu.Lock()
	r.files[path] = f
	if syms, serr := parser.ExtractSymbols(lang, f.Root, f.Bytes); serr == nil {
		r.symbolsByPath[path] = syms
	}
	r.mu.Unlock()
	r.rebuildIndex()
	return nil
}
