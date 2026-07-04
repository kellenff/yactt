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
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/kellenff/yactt/internal/cache"
	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/parser"
	"github.com/kellenff/yactt/internal/source"
)

// ErrNotFound is returned when a requested node / file / symbol doesn't exist.
var ErrNotFound = errors.New("store: not found")

// Repo is the per-root load of a code repository. It is safe for concurrent
// use — symbol/file accessors take the internal mutex.
type Repo struct {
	root          string
	cache         *cache.Cache
	mu            sync.RWMutex
	files         map[string]*source.File
	symbolsByPath map[string][]parser.Symbol
	index         *symIndex
	rootPkg       string
	prov          domain.Provenance
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

	return r, errs, nil
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
