package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/kellenff/yactt/internal/registry"
)

// IndexStatusArgs is the typed boundary input for index_status.
// `path` is required; we key on absolute path on disk so two
// callers pointing at the same repo (via different relative
// paths) collapse onto one entry.
type IndexStatusArgs struct {
	Path string `json:"path"`
}

// IndexStatusResult is the structuredContent envelope for
// index_status. Three independent flags:
//
//   - `entry` is the persisted registry row (absent when the
//     repo has never been indexed via this CLI build).
//   - `cacheExists` is whether the per-repo cache directory
//     still has any bytes on disk. False on a fresh install.
//   - `cacheFresh` is whether the source on disk has been
//     touched since the recorded IndexedAt. False means
//     re-running index_repository would write new bytes; true
//     means a no-op is safe.
//
// The freshness check is intentionally mtime-light: we use the
// filesystem's own newest-mtime walk instead of a content-hash
// over every file. Same trade-off the existing tree_overview
// token-truncate slice made — keep the status call to a single
// stat pass per file at most.
type IndexStatusResult struct {
	Path        string          `json:"path"`
	Entry       *registry.Entry `json:"entry,omitempty"`
	CacheExists bool            `json:"cacheExists"`
	CacheFresh  bool            `json:"cacheFresh"`
	CacheKey    string          `json:"cacheKey"`
}

// IndexStatusSchema is the JSON Schema for index_status.
var IndexStatusSchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["path"],
  "properties": {
    "path": { "type": "string", "description": "Absolute or cwd-relative path to the repo root." }
  },
  "additionalProperties": false
}`)

// IndexStatusOutputSchema declares the structuredContent shape
// of index_status.
var IndexStatusOutputSchema = json.RawMessage(`{
  "type": "object",
  "required": ["path", "cacheExists", "cacheFresh", "cacheKey"],
  "properties": {
    "path":        { "type": "string" },
    "entry":       { "type": "object" },
    "cacheExists": { "type": "boolean" },
    "cacheFresh":  { "type": "boolean" },
    "cacheKey":    { "type": "string" }
  },
  "additionalProperties": false
}`)

// IndexStatus returns a Handler that reports the registry row +
// per-repo cache freshness for `args.Path`. Does not require a
// *store.Repo, which is why it ships in both serve modes.
func IndexStatus(reg *registry.Registry) func(ctx context.Context, args json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		var a IndexStatusArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("invalid index_status args: %w", err)
		}
		if a.Path == "" {
			return nil, errors.New("index_status: path is required")
		}
		abs, err := filepath.Abs(a.Path)
		if err != nil {
			return nil, fmt.Errorf("index_status: resolve path: %w", err)
		}
		cacheKey := cacheKeyForRoot(abs)
		cacheDir := registry.CacheDirForRoot(abs)
		entry, hasEntry := reg.GetByPath(abs)

		out := &IndexStatusResult{
			Path:     abs,
			CacheKey: cacheKey,
		}
		if hasEntry {
			cp := entry
			out.Entry = &cp
		}
		exists, fresh, ferr := checkCacheState(abs, cacheDir, entry, hasEntry)
		if ferr != nil {
			return nil, ferr
		}
		out.CacheExists = exists
		out.CacheFresh = fresh
		return out, nil
	}
}

// checkCacheState answers "does a per-repo cache directory
// exist, and is the on-disk source older than what the registry
// last saw?". Missing pieces are reported as false/false, not as
// errors — a fresh install has neither and shouldn't look
// broken to the agent.
func checkCacheState(root, cacheDir string, entry registry.Entry, hasEntry bool) (exists, fresh bool, err error) {
	if _, serr := os.Stat(cacheDir); serr != nil {
		if errors.Is(serr, os.ErrNotExist) {
			return false, false, nil
		}
		return false, false, fmt.Errorf("index_status: stat cache: %w", serr)
	}
	exists = true
	if !hasEntry {
		// Cache directory exists but no registry row: we can't
		// compare freshness against nothing, so report stale.
		// The agent's expected remediation is to index the repo.
		return true, false, nil
	}
	newest, werr := newestMTime(root)
	if werr != nil {
		// Cache exists; freshness is best-effort. Lie on the
		// conservative side (not fresh) so the agent reindexes
		// rather than trusting a stale tree.
		return true, false, nil
	}
	return true, !newest.After(entry.IndexedAt), nil
}

// newestMTime walks `root` to find the newest mtime among its
// files. Skips noisy dirs (.git, node_modules) so a typical
// repo finishes in O(seconds-of-files-touched). Returns the
// zero time when the walk finds nothing — which collides with
// "all files untouched since 1970", not a real distinction
// worth defending.
func newestMTime(root string) (time.Time, error) {
	var newest time.Time
	werr := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // skip unreadable entries
		}
		if d.IsDir() {
			base := d.Name()
			if base == ".git" || base == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		info, ferr := d.Info()
		if ferr != nil {
			return nil
		}
		if mt := info.ModTime(); mt.After(newest) {
			newest = mt
		}
		return nil
	})
	if werr != nil {
		return time.Time{}, werr
	}
	return newest, nil
}

// cacheKeyForRoot returns the per-repo cache subdirectory name
// (sha256(root)[:16]) that store.Load writes to when the
// disk cache is configured. Thin wrapper for clarity at the
// call site.
func cacheKeyForRoot(root string) string { return registry.CacheKeyForRoot(root) }
