// In-process warm index of loaded projects.
//
// The on-disk parsed-file cache ($XDG_CACHE_HOME/yactt/<root-hash>/)
// survives process restarts but still pays rebuildIndex + LSP startup
// on every project.Resolve. The Index keeps a pinned *store.Repo
// alive for the lifetime of the MCP process so subsequent tool calls
// against the same file:// URI reuse the symbol table, call-edge
// graph, and live LSP clients — the "parser-warm" path the persistent
// HTTP transport was designed for.
//
// Binding is per-*registry.Registry pointer (see BindIndex). Tools
// keep calling project.Resolve(reg, uri); Resolve consults the bound
// Index when one is present. Tests that never BindIndex keep the
// cold per-call Load path unchanged.
package project

import (
	"sync"

	"github.com/kellenff/yactt/internal/registry"
	"github.com/kellenff/yactt/internal/store"
)

// Index is a process-lifetime cache of loaded *store.Repo values,
// keyed by absolute filesystem path. Entries are Pin()'d so
// callers' defer repo.Close() is a no-op; eviction and Close
// ForceClose the underlying repo.
type Index struct {
	mu    sync.Mutex
	byPath map[string]*store.Repo
}

// NewIndex returns an empty warm index.
func NewIndex() *Index {
	return &Index{byPath: make(map[string]*store.Repo)}
}

// Get returns the cached repo for absPath, if any.
func (idx *Index) Get(absPath string) (*store.Repo, bool) {
	if idx == nil {
		return nil, false
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	repo, ok := idx.byPath[absPath]
	return repo, ok
}

// Put pins repo and stores it under repo.Root(), replacing any
// previous entry for that path (ForceClose'd). Nil idx / nil repo
// are no-ops.
func (idx *Index) Put(repo *store.Repo) {
	if idx == nil || repo == nil {
		return
	}
	repo.Pin()
	abs := repo.Root()
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if old, ok := idx.byPath[abs]; ok && old != repo {
		_ = old.ForceClose()
	}
	idx.byPath[abs] = repo
}

// Delete removes the entry for absPath, ForceClose'ing it. Missing
// entries are a no-op.
func (idx *Index) Delete(absPath string) {
	if idx == nil {
		return
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if old, ok := idx.byPath[absPath]; ok {
		delete(idx.byPath, absPath)
		_ = old.ForceClose()
	}
}

// Close ForceClose's every cached repo and clears the map.
func (idx *Index) Close() error {
	if idx == nil {
		return nil
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	var first error
	for path, repo := range idx.byPath {
		delete(idx.byPath, path)
		if err := repo.ForceClose(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Len reports how many projects are currently warm. Tests use this;
// production code shouldn't need it.
func (idx *Index) Len() int {
	if idx == nil {
		return 0
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	return len(idx.byPath)
}

// boundIndexes associates a *registry.Registry with its warm Index
// for the lifetime of the MCP server process. Keyed by pointer so
// two Registry values pointing at the same projects.json file stay
// independent (tests create many registries).
var boundIndexes sync.Map // map[*registry.Registry]*Index

// BindIndex attaches idx to reg so Resolve / Evict / PutFor consult
// it. Passing a nil idx unbinds. Safe to call more than once; the
// previous Index is NOT closed — callers that replace an Index must
// Close the old one themselves.
func BindIndex(reg *registry.Registry, idx *Index) {
	if reg == nil {
		return
	}
	if idx == nil {
		boundIndexes.Delete(reg)
		return
	}
	boundIndexes.Store(reg, idx)
}

// IndexFor returns the Index bound to reg, or nil when unbound.
func IndexFor(reg *registry.Registry) *Index {
	if reg == nil {
		return nil
	}
	v, ok := boundIndexes.Load(reg)
	if !ok {
		return nil
	}
	return v.(*Index)
}

// PutFor stores repo in the Index bound to reg (no-op when unbound).
func PutFor(reg *registry.Registry, repo *store.Repo) {
	IndexFor(reg).Put(repo)
}

// EvictFor removes absPath from the Index bound to reg.
func EvictFor(reg *registry.Registry, absPath string) {
	IndexFor(reg).Delete(absPath)
}
