// Package registry is the on-disk book-keeping for "which repos have
// been indexed, and where are they".
//
// The file lives at $XDG_CACHE_HOME/yactt/projects.json (falling
// back to $HOME/.cache/yactt/projects.json). The format is
// intentionally simple: a JSON object with a version stamp and a
// list of entries. Read-modify-write is serialised under an
// in-process mutex; the MCP server is single-process today, so a
// file lock would be premature.
//
// ponytail: a corrupt projects.json is renamed to
// `.corrupt-<unix-seconds>` rather than silently reset. Same
// recovery shape as cache.DiskCache refuses to overwrite a
// half-written entry. A future upgrade that bumps Version gets a
// place to land its migration.
package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Version is the on-disk format version. Bump when the Entry
// schema changes incompatibly.
const Version = 1

// Entry is one indexed project. Path is the unique key — the same
// project may have multiple Names (display labels) but only one
// canonical Path per registry.
type Entry struct {
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	IndexedAt time.Time `json:"indexedAt"`
	Files     int       `json:"files"`
	Languages []string  `json:"languages"`
	Mode      string    `json:"mode"`
}

// onDisk is the wire shape. Indirection keeps future migrations
// (drop a field, rename a field) localised.
type onDisk struct {
	Version  int     `json:"version"`
	Projects []Entry `json:"projects"`
}

// Registry is the in-process handle to the file. Construct with
// New; pass to the MCP tool constructors. The zero value is not
// usable.
type Registry struct {
	path string
	mu   sync.Mutex // serialises Load → mutate → Store cycles
}

// New returns a Registry whose on-disk file lives at `path`. The
// file need not exist; the first Upsert creates it.
func New(path string) *Registry {
	return &Registry{path: path}
}

// DefaultPath returns the platform-default registry file path:
// $XDG_CACHE_HOME/yactt/projects.json, falling back to
// $HOME/.cache/yactt/projects.json. Returns "" when neither
// variable resolves (rare — usable only for tests).
//
// Mirrors cmd/yactt/main.go's diskCacheDir resolver so the
// registry and the per-repo cache live under the same root.
func DefaultPath() string {
	var base string
	if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" {
		base = xdg
	} else if home, err := os.UserHomeDir(); err == nil && home != "" {
		base = filepath.Join(home, ".cache")
	} else {
		return ""
	}
	return filepath.Join(base, "yactt", "projects.json")
}

// Path returns the file path this Registry reads and writes.
func (r *Registry) Path() string { return r.path }

// List returns every entry in the registry, sorted by Path for
// deterministic output. Returns an empty slice (not nil) when the
// file doesn't exist yet — a brand-new install shouldn't look
// like an error.
func (r *Registry) List() ([]Entry, error) {
	disk, err := r.load()
	if err != nil {
		return nil, err
	}
	out := make([]Entry, len(disk.Projects))
	copy(out, disk.Projects)
	// Stable order so callers (and tests) can rely on it.
	sortByPath(out)
	return out, nil
}

// GetByPath returns the entry whose Path matches `path` exactly.
// The bool reports presence; the second return is the entry, which
// is the zero value when missing.
func (r *Registry) GetByPath(path string) (Entry, bool) {
	entries, err := r.List()
	if err != nil {
		return Entry{}, false
	}
	for _, e := range entries {
		if e.Path == path {
			return e, true
		}
	}
	return Entry{}, false
}

// Upsert writes `e` to the registry. If an entry with the same
// Path already exists, it's replaced in place; the rest of the
// list is preserved. Upsert is the only mutation that succeeds
// when the file is missing (it creates the file).
func (r *Registry) Upsert(e Entry) error {
	if e.Path == "" {
		return errors.New("registry: entry path is empty")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	disk, err := r.load()
	if err != nil {
		return err
	}
	replaced := false
	for i, existing := range disk.Projects {
		if existing.Path == e.Path {
			disk.Projects[i] = e
			replaced = true
			break
		}
	}
	if !replaced {
		disk.Projects = append(disk.Projects, e)
	}
	return r.store(disk)
}

// Delete removes the entry keyed by `path`. The bool reports
// whether anything was deleted. A no-op when the file is missing
// or the entry isn't found; the latter is reported via bool=false
// so callers can distinguish "nothing to do" from "I deleted it".
func (r *Registry) Delete(path string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	disk, err := r.load()
	if err != nil {
		return false, err
	}
	for i, e := range disk.Projects {
		if e.Path == path {
			disk.Projects = append(disk.Projects[:i], disk.Projects[i+1:]...)
			return true, r.store(disk)
		}
	}
	return false, nil
}

// load reads the file. Returns an empty registry (Version 1, no
// entries) when the file is missing. A corrupt file is renamed
// aside so the next mutation starts clean without losing the
// original bytes (helpful when triaging field reports).
func (r *Registry) load() (onDisk, error) {
	data, err := os.ReadFile(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return onDisk{Version: Version}, nil
	}
	if err != nil {
		return onDisk{}, fmt.Errorf("registry: read %s: %w", r.path, err)
	}
	var disk onDisk
	if err := json.Unmarshal(data, &disk); err != nil {
		// Quarantine the corrupt file. We rename-before-return so
		// the caller sees a clean slate on retry and the field
		// engineer can still inspect the original bytes.
		quarantine := r.path + fmt.Sprintf(".corrupt-%d", time.Now().Unix())
		if rerr := os.Rename(r.path, quarantine); rerr != nil {
			return onDisk{}, fmt.Errorf("registry: parse %s: %w (also failed to quarantine: %v)", r.path, err, rerr)
		}
		return onDisk{}, fmt.Errorf("registry: parse %s (quarantined to %s): %w", r.path, quarantine, err)
	}
	if disk.Version != Version {
		return onDisk{}, fmt.Errorf("registry: unsupported version %d (want %d) at %s", disk.Version, Version, r.path)
	}
	return disk, nil
}

// store writes `disk` atomically: marshal → temp file in the same
// directory → rename. Same-directory rename is atomic on POSIX
// (and on Windows when the target doesn't exist).
func (r *Registry) store(disk onDisk) error {
	if disk.Version == 0 {
		disk.Version = Version
	}
	data, err := json.MarshalIndent(disk, "", "  ")
	if err != nil {
		return fmt.Errorf("registry: marshal: %w", err)
	}
	if dir := filepath.Dir(r.path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("registry: mkdir %s: %w", dir, err)
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(r.path), ".projects-*.json.tmp")
	if err != nil {
		return fmt.Errorf("registry: temp: %w", err)
	}
	tmpPath := tmp.Name()
	// Best-effort cleanup on any failure below; the rename either
	// landed (good) or it didn't (we delete the orphan).
	defer func() {
		if tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("registry: write tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("registry: close tmp: %w", err)
	}
	if err := os.Rename(tmpPath, r.path); err != nil {
		return fmt.Errorf("registry: rename %s → %s: %w", tmpPath, r.path, err)
	}
	tmpPath = "" // rename consumed it; suppress the cleanup
	return nil
}

// sortByPath orders `in` in place by Path. Tiny inline sort —
// the registry is small enough that pulling in sort.Slice
// everywhere we iterate would dominate the call site.
func sortByPath(in []Entry) {
	for i := 1; i < len(in); i++ {
		j := i
		for j > 0 && strings.Compare(in[j-1].Path, in[j].Path) > 0 {
			in[j-1], in[j] = in[j], in[j-1]
			j--
		}
	}
}
