package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kellenff/yactt/internal/registry"
	"github.com/kellenff/yactt/internal/store/repofixture"
)

// TestIndexRepository_FreshCacheShortCircuit pins the
// performance fix from PR-54 review: when a registry entry
// already exists AND the disk cache is fresh, index_repository
// returns the existing entry with Reloaded=false rather than
// re-walking the source tree.
//
// Without the short-circuit every index_repository call paid
// the store.Load cost (which scales with files walked + per-file
// grammar parsing). For an agent that calls index_repository
// defensively before every code-intel tool this was a measurable
// latency hit.
//
// Audit / TOFU hooks remain memoised by sync.Once so the
// short-circuit doesn't accidentally double-emit.
func TestIndexRepository_FreshCacheShortCircuit(t *testing.T) {
	fx := repofixture.New(t)
	regPath := filepath.Join(t.TempDir(), "projects.json")
	reg := registry.New(regPath)
	idx := IndexRepository(reg, nil, nil)

	// First call: cold cache. Must walk and persist a row.
	first, err := idx(context.Background(),
		json.RawMessage(`{"project":"file://`+fx.Root+`"}`))
	if err != nil {
		t.Fatalf("first index_repository: %v", err)
	}
	firstRes, ok := first.(*IndexRepositoryResult)
	if !ok {
		t.Fatalf("first: result type = %T; want *IndexRepositoryResult", first)
	}
	if !firstRes.Reloaded {
		t.Errorf("first call: Reloaded = false; want true (cold cache must reload)")
	}
	if firstRes.Entry.Files == 0 {
		t.Errorf("first call: Files = 0; want > 0 after a real walk")
	}

	// Second call: warm cache. Must short-circuit — same entry,
	// Reloaded=false, Files unchanged from the first call.
	second, err := idx(context.Background(),
		json.RawMessage(`{"project":"file://`+fx.Root+`"}`))
	if err != nil {
		t.Fatalf("second index_repository: %v", err)
	}
	secondRes, ok := second.(*IndexRepositoryResult)
	if !ok {
		t.Fatalf("second: result type = %T; want *IndexRepositoryResult", second)
	}
	if secondRes.Reloaded {
		t.Errorf("second call: Reloaded = true; want false (warm cache must short-circuit)")
	}
	if secondRes.Entry.Path != firstRes.Entry.Path {
		t.Errorf("second call: Path = %q; want %q", secondRes.Entry.Path, firstRes.Entry.Path)
	}
	if secondRes.Entry.Files != firstRes.Entry.Files {
		t.Errorf("second call: Files = %d; want %d (no reload = no recount)",
			secondRes.Entry.Files, firstRes.Entry.Files)
	}
}

// TestIndexRepository_StaleCacheReloads is the partner to
// FreshCacheShortCircuit: when the source on disk has changed
// since IndexedAt (mtime newer than the recorded time), the
// handler must re-walk and persist a new entry. The Reloaded
// flag must be true and Files must reflect the post-walk count
// (which may differ if files were added/removed).
func TestIndexRepository_StaleCacheReloads(t *testing.T) {
	fx := repofixture.New(t)
	regPath := filepath.Join(t.TempDir(), "projects.json")
	reg := registry.New(regPath)

	// Seed an entry whose IndexedAt is in the past, so any
	// file mtime is "after" it. The cache dir is created by
	// the seed.
	idx := IndexRepository(reg, nil, nil)
	if _, err := idx(context.Background(),
		json.RawMessage(`{"project":"file://`+fx.Root+`"}`)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Backdate the entry's IndexedAt so freshness check fails.
	entry, ok := reg.GetByPath(fx.Root)
	if !ok {
		t.Fatalf("entry not found after seed")
	}
	entry.IndexedAt = time.Now().Add(-1 * time.Hour)
	if err := reg.Upsert(entry); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	// Touch a file so newestMTime definitely exceeds IndexedAt.
	touchPath := filepath.Join(fx.Root, "auth", "login.go")
	stale := time.Now().Add(-30 * time.Minute)
	if err := os.Chtimes(touchPath, stale, stale); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	// Re-index — must reload because cache is stale.
	out, err := idx(context.Background(),
		json.RawMessage(`{"project":"file://`+fx.Root+`"}`))
	if err != nil {
		t.Fatalf("re-index: %v", err)
	}
	res, ok := out.(*IndexRepositoryResult)
	if !ok {
		t.Fatalf("result type = %T; want *IndexRepositoryResult", out)
	}
	if !res.Reloaded {
		t.Errorf("stale cache: Reloaded = false; want true (must re-walk)")
	}
	if !res.Entry.IndexedAt.After(entry.IndexedAt) {
		t.Errorf("stale cache: IndexedAt %v not after original %v",
			res.Entry.IndexedAt, entry.IndexedAt)
	}
}

// TestIndexRepository_NoRegistryRowBypassesShortCircuit pins
// the case where the on-disk cache exists but the registry has
// no entry (e.g. partial state from a half-deleted project).
// index_repository must reload so the row gets written back.
func TestIndexRepository_NoRegistryRowBypassesShortCircuit(t *testing.T) {
	fx := repofixture.New(t)
	regPath := filepath.Join(t.TempDir(), "projects.json")
	reg := registry.New(regPath)
	idx := IndexRepository(reg, nil, nil)

	// Seed the cache + registry.
	if _, err := idx(context.Background(),
		json.RawMessage(`{"project":"file://`+fx.Root+`"}`)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Drop the row from the registry while leaving the cache dir intact.
	// No API for delete-by-path on the Registry, so rebuild the file
	// from scratch via List+empty.
	if err := reg.Upsert(registry.Entry{}); err != nil {
		// Empty path upsert is rejected; instead reload the disk
		// image with no projects.
		t.Logf("could not clear via empty upsert (expected): %v", err)
	}
	// Workaround: load the existing entries and remove ours via the
	// file directly so the test stays focused on the cache logic.
	entries, err := reg.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	kept := make([]registry.Entry, 0, len(entries))
	for _, e := range entries {
		if e.Path != fx.Root {
			kept = append(kept, e)
		}
	}
	if err := rewriteRegistry(regPath, kept); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	// Reload reg from the same file so the in-memory view matches.
	reg = registry.New(regPath)

	// Now re-index — must reload because no registry row exists,
	// even though the cache dir does.
	out, err := IndexRepository(reg, nil, nil)(context.Background(),
		json.RawMessage(`{"project":"file://`+fx.Root+`"}`))
	if err != nil {
		t.Fatalf("re-index: %v", err)
	}
	res := out.(*IndexRepositoryResult)
	if !res.Reloaded {
		t.Errorf("no registry row: Reloaded = false; want true (must restore the row)")
	}
}

// rewriteRegistry writes the JSON envelope with the given entries
// directly so tests can simulate a "registry without our row" state.
// Uses the same on-disk shape registry.Registry writes (Version + Projects).
func rewriteRegistry(path string, entries []registry.Entry) error {
	type onDisk struct {
		Version  int              `json:"version"`
		Projects []registry.Entry `json:"projects"`
	}
	if entries == nil {
		entries = []registry.Entry{}
	}
	body, err := json.MarshalIndent(onDisk{Version: registry.Version, Projects: entries}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, body, 0o644)
}