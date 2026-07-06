// Disk cache for parsed source files.
//
// The in-memory cache (cache.go) holds parsed files for the lifetime of
// the MCP server. The disk cache persists them across runs — same repo,
// second `yactt mcp serve` boot: file → disk hit → no tree-sitter parse,
// no LSP warm-up.
//
// The C-level parse root (tree-sitter's *sitter.Node) can't survive gob
// encoding (it holds C pointers). On hydrate we re-parse the bytes
// with the stored grammar; tree-sitter parse is ~1 ms per file. The
// disk format is therefore the raw source bytes plus the parser name
// plus the mtime at the time we cached it. Mismatched mtime → miss.
//
// ponytail: the disk format is intentionally simple. No version
// stamp, no compression, no LRU. A future yactt upgrade that changes
// the format silently invalidates older entries (gob decode fails →
// Get returns miss → caller re-parses). Acceptable for MVP; lift to
// a versioned format only if we start hitting real corruption.

package cache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/kellenff/yactt/internal/parser"
	"github.com/kellenff/yactt/internal/source"
)

// diskEntry is the on-disk shape. Kept minimal — only the fields
// needed to re-parse the file on hydrate.
type diskEntry struct {
	MTime   int64
	Grammar string // parser.Name; round-tripped via parser.ByName
	Bytes   []byte
}

// DefaultMaxDiskBytes is the disk cache's default size cap. 0 means
// "unlimited" — the disk cache grows unboundedly. Production callers
// (cmd/yactt/main.go) override this with a sensible per-repo cap;
// tests override with smaller caps to exercise the eviction path.
const DefaultMaxDiskBytes int64 = 0

// DiskCache persists parsed source.File entries to a directory. The
// directory layout is `<dir>/<sha256(path)[:32hex]>` — one file per
// source path, no subdirectories. Different repos must use different
// dirs (cmd/yactt/main.go keys the dir by repo root hash).
//
// An optional size cap (DefaultMaxDiskBytes, or the value supplied via
// WithMaxBytes) bounds total cache size on disk. After each Put, if
// the cap is set and the directory is over budget, the oldest entries
// (by mtime) are evicted until the total is under cap. mtime is
// updated by each Put's atomic write — it's a "last-written" signal,
// not a true LRU; reads do not bump mtime. Good enough for the MVP:
// stale entries get evicted eventually, hot files stay.
type DiskCache struct {
	dir      string
	maxBytes int64
}

// DiskCacheOption configures a DiskCache at construction time.
type DiskCacheOption func(*DiskCache)

// WithMaxBytes sets the size cap. A value of 0 disables the cap
// (unlimited growth). Negative values are clamped to 0.
func WithMaxBytes(n int64) DiskCacheOption {
	return func(c *DiskCache) {
		if n < 0 {
			n = 0
		}
		c.maxBytes = n
	}
}

// NewDiskCache creates (or opens) a disk cache rooted at dir. The
// directory is created if it doesn't already exist; existing entries
// are preserved.
func NewDiskCache(dir string, opts ...DiskCacheOption) (*DiskCache, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("cache: mkdir %s: %w", dir, err)
	}
	c := &DiskCache{
		dir:      dir,
		maxBytes: DefaultMaxDiskBytes,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// Dir returns the cache directory. Exposed for diagnostics; tests
// inspect this to confirm the CLI picked the right location.
func (d *DiskCache) Dir() string { return d.dir }

// Get returns the cached parse for path if (a) the entry exists, (b)
// its mtime matches currentMTime, and (c) re-parse succeeds with the
// stored grammar. Otherwise returns cache.ErrMiss.
//
// Re-parse failures and gob decode failures are signalled as miss
// (not error) — the caller's fallback path (in-memory cache or
// source.LoadFile) is the right place to handle a true miss.
func (d *DiskCache) Get(path string, currentMTime int64) (*source.File, error) {
	key := d.keyPath(path)
	raw, err := os.ReadFile(key)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrMiss
		}
		return nil, fmt.Errorf("cache: read %s: %w", key, err)
	}
	var entry diskEntry
	if err := gob.NewDecoder(bytes.NewReader(raw)).Decode(&entry); err != nil {
		// Corrupt entry — treat as miss. Caller re-parses.
		return nil, ErrMiss
	}
	if entry.MTime != currentMTime {
		return nil, ErrMiss
	}
	lang, err := parser.ByName(parser.Name(entry.Grammar))
	if err != nil {
		return nil, ErrMiss
	}
	root, err := sitter.ParseCtx(context.Background(), entry.Bytes, lang.Grammar())
	if err != nil {
		return nil, ErrMiss
	}
	return &source.File{
		Path:    path,
		Bytes:   entry.Bytes,
		MTime:   entry.MTime,
		Root:    root,
		Grammar: lang,
	}, nil
}

// Put writes the parsed file to disk. Atomic via temp-file + rename
// so a partial write (interrupted by signal or OOM) doesn't leave
// a half-decodable entry. Errors are returned but not categorised —
// the caller's policy decides whether to retry, log, or ignore.
//
// When a size cap is configured (WithMaxBytes), Put triggers
// eviction after the write: oldest entries (by mtime) are removed
// until total directory size is under cap. If the freshly-written
// file alone exceeds the cap, eviction can't help and the file
// stays — the cap is a soft target, not a hard per-entry limit.
func (d *DiskCache) Put(f *source.File) error {
	entry := diskEntry{
		MTime:   f.MTime,
		Grammar: string(f.Grammar.Name()),
		Bytes:   f.Bytes,
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(&entry); err != nil {
		return fmt.Errorf("cache: encode: %w", err)
	}
	key := d.keyPath(f.Path)
	tmp := key + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("cache: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, key); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("cache: rename %s: %w", tmp, err)
	}
	if d.maxBytes > 0 {
		if err := d.evictIfOverBudget(); err != nil {
			// Eviction is best-effort; a failure must not roll back
			// the Put. Log via the returned error so the caller can
			// surface it (typically stderr in production).
			return fmt.Errorf("cache: evict after put: %w", err)
		}
	}
	return nil
}

// diskEntryInfo is one entry's metadata for the eviction pass.
type diskEntryInfo struct {
	path  string
	mtime time.Time
	size  int64
}

// evictIfOverBudget removes oldest entries (by mtime) until total
// size is at or under d.maxBytes. Returns nil if no eviction needed.
//
// .tmp files are removed unconditionally — they're guaranteed stale
// (left over from a crash mid-rename). Without cleanup they would
// inflate the size calculation and waste cap budget.
func (d *DiskCache) evictIfOverBudget() error {
	entries, total, err := d.scanDir()
	if err != nil {
		return err
	}
	if total <= d.maxBytes {
		return nil
	}
	// Sort oldest first.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].mtime.Before(entries[j].mtime)
	})
	for _, e := range entries {
		if total <= d.maxBytes {
			break
		}
		if err := os.Remove(e.path); err == nil {
			total -= e.size
		}
	}
	return nil
}

// scanDir walks the cache directory and returns per-entry metadata
// plus the total size. .tmp files are skipped from the size tally
// but their stale-on-crash semantics mean they're rare in practice;
// the caller can decide to clean them via a separate sweep.
func (d *DiskCache) scanDir() ([]diskEntryInfo, int64, error) {
	names, err := readDirNames(d.dir)
	if err != nil {
		return nil, 0, fmt.Errorf("cache: scandir %s: %w", d.dir, err)
	}
	out := make([]diskEntryInfo, 0, len(names))
	var total int64
	for _, name := range names {
		full := filepath.Join(d.dir, name)
		if strings.HasSuffix(name, ".tmp") {
			continue
		}
		st, err := os.Stat(full)
		if err != nil {
			continue
		}
		out = append(out, diskEntryInfo{path: full, mtime: st.ModTime(), size: st.Size()})
		total += st.Size()
	}
	return out, total, nil
}

// readDirNames is a tiny helper that returns the entries in dir.
// Errors are returned verbatim; callers turn them into wrapped errors.
func readDirNames(dir string) ([]string, error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Readdirnames(-1)
}

// EvictOrphans removes cache entries whose source path is not in
// `validPaths`. The repo calls this at Load time with the paths it
// just indexed; anything left in the cache that isn't in that set
// is a file that's been deleted (or otherwise gone missing) and
// the cache entry is dead weight.
//
// Empty `validPaths` evicts everything — a fresh-empty repo
// legitimately has zero cache entries. Per-entry Remove errors are
// swallowed (best-effort); a directory-read error propagates.
//
// `.tmp` files are skipped — they're crash artefacts, not part of
// the regular entry lifecycle. Non-hex filenames are also skipped
// as defence-in-depth: the cache directory is owned by DiskCache
// and shouldn't contain anything else, but a stray file (manual
// intervention, future format change, sibling-file name collision)
// shouldn't be auto-deleted by this pass.
func (d *DiskCache) EvictOrphans(validPaths []string) error {
	valid := make(map[string]bool, len(validPaths))
	for _, p := range validPaths {
		sum := sha256.Sum256([]byte(p))
		valid[hex.EncodeToString(sum[:16])] = true
	}
	names, err := readDirNames(d.dir)
	if err != nil {
		return fmt.Errorf("cache: evict orphans scandir %s: %w", d.dir, err)
	}
	for _, name := range names {
		if !isHexName(name) || strings.HasSuffix(name, ".tmp") {
			continue
		}
		if !valid[name] {
			_ = os.Remove(filepath.Join(d.dir, name))
		}
	}
	return nil
}

// isHexName returns true when name is exactly 32 lowercase hex
// chars — the shape of a sha256[:16] cache key. Used by
// EvictOrphans to avoid touching non-cache files.
func isHexName(name string) bool {
	if len(name) != 32 {
		return false
	}
	for _, c := range name {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// keyPath maps an absolute source path to its cache file. The hash
// is sha256 of the path truncated to 16 bytes (32 hex chars). The
// hash avoids filesystem-unsafe characters and keeps filenames
// uniform length regardless of source path.
func (d *DiskCache) keyPath(path string) string {
	sum := sha256.Sum256([]byte(path))
	return filepath.Join(d.dir, hex.EncodeToString(sum[:16]))
}
