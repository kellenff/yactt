package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"

	"github.com/kellenff/yactt/internal/store"
)

// DefaultMaxDiskCacheBytes is the disk cache's default per-repo
// size cap, kept here so the cmd layer and the index_repository
// tool can't drift. 512 MiB fits a small/medium repo's parsed
// file cache comfortably; override via YACTT_DISK_CACHE_MAX_BYTES
// (set to 0 for unlimited growth).
const DefaultMaxDiskCacheBytes int64 = 512 * 1024 * 1024

// LoadOptsWithDiskCache returns the store.LoadOption slice that
// wires a per-repo disk cache rooted at the same
// $XDG_CACHE_HOME/yactt/<root-hash> path the rest of the code
// uses. Returns nil when no cache root can be resolved — callers
// should then fall back to in-memory-only loading.
//
// The cap is read from YACTT_DISK_CACHE_MAX_BYTES (or
// DefaultMaxDiskCacheBytes when the env is unset).
//
// ponytail: the env-var parsing here matches cmd/yactt/main.go's
// pre-existing behaviour byte-for-byte. The two implementations
// were duplicates; consolidation landed with this slice.
func LoadOptsWithDiskCache(repoRoot string) []store.LoadOption {
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

// CacheKeyForRoot returns the per-repo cache subdirectory name
// (`<root-hash>[:16]`, hex) that store.Load uses to key the
// disk cache. Tools that need to talk about the same key
// (index_status' diagnostic envelope, for example) call this
// so the calculation lives in one place.
func CacheKeyForRoot(root string) string {
	sum := sha256.Sum256([]byte(root))
	return hex.EncodeToString(sum[:16])
}

// CacheDirForRoot returns the absolute path of the per-repo
// cache directory for `root`. Returns "" when no cache root
// can be resolved ($XDG_CACHE_HOME and $HOME both unset) —
// callers should treat that as "nothing to do" rather than
// as an error.
//
// This is the public counterpart of the unexported
// diskCacheDir; the two diverge only in their audience.
func CacheDirForRoot(root string) string {
	return diskCacheDir(root)
}
