package project_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/kellenff/yactt/internal/project"
	"github.com/kellenff/yactt/internal/registry"
	"github.com/kellenff/yactt/internal/store"
	"github.com/kellenff/yactt/internal/store/repofixture"
)

func TestIndex_PutGetDelete(t *testing.T) {
	fx := repofixture.New(t)
	repo, _, err := store.Load(fx.Root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	idx := project.NewIndex()
	t.Cleanup(func() { _ = idx.Close() })

	if _, ok := idx.Get(fx.Root); ok {
		t.Fatal("Get on empty index returned a hit")
	}
	idx.Put(repo)
	if idx.Len() != 1 {
		t.Fatalf("Len = %d, want 1", idx.Len())
	}
	got, ok := idx.Get(fx.Root)
	if !ok || got != repo {
		t.Fatalf("Get miss or wrong repo: ok=%v got=%p want=%p", ok, got, repo)
	}
	// Close must be a no-op while pinned.
	if err := repo.Close(); err != nil {
		t.Fatalf("Close on pinned repo: %v", err)
	}
	got2, ok := idx.Get(fx.Root)
	if !ok || got2 != repo {
		t.Fatal("repo disappeared after Close; pin broken")
	}

	idx.Delete(fx.Root)
	if idx.Len() != 0 {
		t.Fatalf("Len after Delete = %d, want 0", idx.Len())
	}
	if _, ok := idx.Get(fx.Root); ok {
		t.Fatal("Get hit after Delete")
	}
}

func TestResolve_WarmHitReusesRepo(t *testing.T) {
	fx := repofixture.New(t)
	dir := t.TempDir()
	reg := registry.New(filepath.Join(dir, "projects.json"))
	if err := reg.Upsert(registry.Entry{
		Name:      filepath.Base(fx.Root),
		Path:      fx.Root,
		IndexedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	idx := project.NewIndex()
	project.BindIndex(reg, idx)
	t.Cleanup(func() {
		_ = idx.Close()
		project.BindIndex(reg, nil)
	})

	first, err := project.Resolve(reg, "file://"+fx.Root)
	if err != nil {
		t.Fatalf("Resolve first: %v", err)
	}
	_ = first.Close()

	second, err := project.Resolve(reg, "file://"+fx.Root)
	if err != nil {
		t.Fatalf("Resolve second: %v", err)
	}
	_ = second.Close()

	if first != second {
		t.Fatalf("warm Resolve returned a different *Repo: %p vs %p", first, second)
	}
	if idx.Len() != 1 {
		t.Fatalf("Index.Len = %d, want 1", idx.Len())
	}
}

func TestResolve_ColdWithoutBind(t *testing.T) {
	fx := repofixture.New(t)
	dir := t.TempDir()
	reg := registry.New(filepath.Join(dir, "projects.json"))
	if err := reg.Upsert(registry.Entry{
		Name:      filepath.Base(fx.Root),
		Path:      fx.Root,
		IndexedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	// No BindIndex — every Resolve must Load fresh.
	first, err := project.Resolve(reg, "file://"+fx.Root)
	if err != nil {
		t.Fatalf("Resolve first: %v", err)
	}
	_ = first.Close()

	second, err := project.Resolve(reg, "file://"+fx.Root)
	if err != nil {
		t.Fatalf("Resolve second: %v", err)
	}
	_ = second.Close()

	if first == second {
		t.Fatal("cold Resolve reused *Repo without a bound Index")
	}
}

func TestEvictFor_DropsWarmEntry(t *testing.T) {
	fx := repofixture.New(t)
	dir := t.TempDir()
	reg := registry.New(filepath.Join(dir, "projects.json"))
	if err := reg.Upsert(registry.Entry{
		Name:      filepath.Base(fx.Root),
		Path:      fx.Root,
		IndexedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	idx := project.NewIndex()
	project.BindIndex(reg, idx)
	t.Cleanup(func() {
		_ = idx.Close()
		project.BindIndex(reg, nil)
	})

	repo, err := project.Resolve(reg, "file://"+fx.Root)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	_ = repo.Close()
	if idx.Len() != 1 {
		t.Fatalf("Len = %d, want 1", idx.Len())
	}

	project.EvictFor(reg, fx.Root)
	if idx.Len() != 0 {
		t.Fatalf("Len after EvictFor = %d, want 0", idx.Len())
	}

	// Next Resolve must Load a new repo (Index miss).
	again, err := project.Resolve(reg, "file://"+fx.Root)
	if err != nil {
		t.Fatalf("Resolve after evict: %v", err)
	}
	_ = again.Close()
	if again == repo {
		t.Fatal("Resolve after EvictFor returned the ForceClose'd repo")
	}
}

func TestIndex_PutReplacesPrevious(t *testing.T) {
	fx := repofixture.New(t)
	idx := project.NewIndex()
	t.Cleanup(func() { _ = idx.Close() })

	a, _, err := store.Load(fx.Root)
	if err != nil {
		t.Fatalf("Load a: %v", err)
	}
	b, _, err := store.Load(fx.Root)
	if err != nil {
		t.Fatalf("Load b: %v", err)
	}
	idx.Put(a)
	idx.Put(b)
	if idx.Len() != 1 {
		t.Fatalf("Len = %d, want 1 after replace", idx.Len())
	}
	got, ok := idx.Get(fx.Root)
	if !ok || got != b {
		t.Fatalf("Get after replace: ok=%v got=%p want=%p", ok, got, b)
	}
}
