// Package cache is the layered cache per design §5.2.
//
// Layered cache policy:
//   - The hot layer is the parsed `source.File` per file path; on disk the file
//     content is the source of truth, so a key collision is detected via the
//     file's mtime. Stale entries drop without explicit invalidation.
//   - Symbol-level layers (summary / signature / body / tokens) are derived
//     from the parsed file and re-derive on miss; they're cached behind a
//     fixed-size LRU so a deep traversal doesn't grow without bound.
//
// The cache is in-memory only for MVP. Disk backing is a Phase 1.5 item.
package cache

import (
	"container/list"
	"errors"
	"sync"
	"time"

	"github.com/kellenff/yactt/internal/source"
)

// ErrMiss is returned by lookup functions when no live entry exists.
var ErrMiss = errors.New("cache: miss")

// DefaultSizes favour warm-path hit rate over a tight RSS budget.
// Memory is cheap relative to tree-sitter reparse: a parsed source.File
// is typically tens to hundreds of KiB, so tens of thousands of entries
// still fit comfortably in a persistent HTTP daemon. DefaultFileCap is
// aligned with store.DefaultMaxFiles (50_000) so one fully-loaded
// monorepo's working set fits in the hot LRU; DefaultLayerCap is 4×
// that for signature/body/source layers derived from those files.
const (
	DefaultFileCap     = 50_000
	DefaultLayerCap    = 200_000
	DefaultSemanticTTL = 5 * time.Minute
	DefaultSummaryTTL  = 24 * time.Hour
)

// Cache is the layered cache for parsed files and derived symbol layers.
type Cache struct {
	mu sync.Mutex

	fileCap     int
	layerCap    int
	semanticTTL time.Duration
	summaryTTL  time.Duration

	// now returns the current wall-clock time. Production callers see
	// time.Now; tests in the same package can override it to drive TTL
	// behaviour without sleeping.
	now func() time.Time

	files     map[string]*list.Element // path -> entry
	fileOrder *list.List               // LRU; front = most-recently-used

	layers     map[layerKey]*list.Element // (node_id, layer) -> entry
	layerOrder *list.List                 // LRU; front = most-recently-used
}

type fileEntry struct {
	path string
	file *source.File
}

type layerKey struct {
	nodeID string
	layer  string
}

type layerEntry struct {
	key   layerKey
	value []byte
	ts    time.Time
}

// New constructs a Cache with default sizes / TTLs.
func New() *Cache {
	return NewSized(DefaultFileCap, DefaultLayerCap)
}

// NewSized constructs a Cache with custom capacities and the default TTLs.
func NewSized(fileCap, layerCap int) *Cache {
	if fileCap <= 0 {
		fileCap = DefaultFileCap
	}
	if layerCap <= 0 {
		layerCap = DefaultLayerCap
	}
	return &Cache{
		fileCap:     fileCap,
		layerCap:    layerCap,
		semanticTTL: DefaultSemanticTTL,
		summaryTTL:  DefaultSummaryTTL,
		now:         time.Now,
		files:       make(map[string]*list.Element),
		fileOrder:   list.New(),
		layers:      make(map[layerKey]*list.Element),
		layerOrder:  list.New(),
	}
}

// GetFile returns the cached parsed file for `path`. Returns ErrMiss if the
// file isn't cached or its mtime has changed (caller should re-load and Put).
func (c *Cache) GetFile(path string, currentMTime int64) (*source.File, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.files[path]
	if !ok {
		return nil, ErrMiss
	}
	entry := el.Value.(*fileEntry)
	if entry.file.MTime != currentMTime {
		// mtime changed — drop the stale entry and signal miss.
		c.fileOrder.Remove(el)
		delete(c.files, path)
		return nil, ErrMiss
	}
	c.fileOrder.MoveToFront(el)
	return entry.file, nil
}

// PutFile inserts (or refreshes) the cached parsed file for `path`.
func (c *Cache) PutFile(f *source.File) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.files[f.Path]; ok {
		el.Value.(*fileEntry).file = f
		c.fileOrder.MoveToFront(el)
		return
	}
	el := c.fileOrder.PushFront(&fileEntry{path: f.Path, file: f})
	c.files[f.Path] = el
	if c.fileOrder.Len() > c.fileCap {
		// Drop the least-recently-used file.
		back := c.fileOrder.Back()
		if back != nil {
			c.fileOrder.Remove(back)
			delete(c.files, back.Value.(*fileEntry).path)
		}
	}
}

// GetLayer returns a cached layer value for (nodeID, layer) if one is live.
// "Live" means it hasn't exceeded its TTL — semantic layers (signature,
// body, tokens, source) age out after SemanticTTL; summary ages after
// SummaryTTL. Values are opaque bytes so the cache doesn't import `json`.
//
// Returns ErrMiss on miss or expiry.
func (c *Cache) GetLayer(nodeID, layer string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.layers[layerKey{nodeID, layer}]
	if !ok {
		return nil, ErrMiss
	}
	entry := el.Value.(*layerEntry)
	ttl := c.semanticTTL
	if layer == "summary" {
		ttl = c.summaryTTL
	}
	if c.now().Sub(entry.ts) > ttl {
		// Expired.
		c.layerOrder.Remove(el)
		delete(c.layers, entry.key)
		return nil, ErrMiss
	}
	c.layerOrder.MoveToFront(el)
	return entry.value, nil
}

// PutLayer inserts (or refreshes) a layer cache entry. The bytes are stored
// verbatim and returned on the next miss; the caller is responsible for
// marshalling in/out.
func (c *Cache) PutLayer(nodeID, layer string, value []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := layerKey{nodeID, layer}
	if el, ok := c.layers[key]; ok {
		entry := el.Value.(*layerEntry)
		entry.value = value
		entry.ts = c.now()
		c.layerOrder.MoveToFront(el)
		return
	}
	el := c.layerOrder.PushFront(&layerEntry{key: key, value: value, ts: c.now()})
	c.layers[key] = el
	if c.layerOrder.Len() > c.layerCap {
		back := c.layerOrder.Back()
		if back != nil {
			c.layerOrder.Remove(back)
			delete(c.layers, back.Value.(*layerEntry).key)
		}
	}
}

// Stats is a small observability helper; used by tests and by an eventual /stats
// endpoint. Best-effort, not intended for high-precision dashboards.
type Stats struct {
	Files  int
	Layers int
}

// Snapshot returns the current cardinality of both LRU tiers.
func (c *Cache) Snapshot() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Stats{Files: c.fileOrder.Len(), Layers: c.layerOrder.Len()}
}
