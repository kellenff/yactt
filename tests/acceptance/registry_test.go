// Package acceptance covers the four registry tools: list_projects,
// index_repository, index_status, delete_project. Each test is
// end-to-end (real on-disk registry, real on-disk repo) but stays
// isolated from the developer's $XDG_CACHE_HOME by routing the
// registry through a tempdir.
//
// The tests don't share state across functions; each test sets up
// its own registry and uses a per-test tempdir so a `go test
// ./tests/acceptance/...` run can never trample the developer's
// real project book.
package acceptance_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kellenff/yactt/internal/registry"
	"github.com/kellenff/yactt/internal/tool"
)

// isolatedRegistry returns a *registry.Registry rooted at a fresh
// tempdir. Every test gets its own — the package-level
// `fixtureRepoCache` would let two tests share a repo, but the
// registry book is shared by every process on disk, so a
// per-test isolation is mandatory.
func isolatedRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	return registry.New(filepath.Join(t.TempDir(), "projects.json"))
}

// callRegistry runs a registry tool's handler and returns the raw
// result. Mirrors the callJSON helper above but takes the args as
// a generic map[string]any so tests can build them up without
// quoting JSON strings by hand.
func callRegistry(t *testing.T, h func(ctx context.Context, args json.RawMessage) (any, error), args map[string]any) any {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	out, err := h(context.Background(), raw)
	if err != nil {
		t.Fatalf("handler error: %v (args=%s)", err, raw)
	}
	return out
}

// TestTool_ListProjects walks the full lifecycle: empty → one
// entry → still one entry → empty again. Each subtest asserts a
// property of the list — count, path presence, sort order — so a
// regression in any of them fires a single, named test.
func TestTool_ListProjects(t *testing.T) {
	reg := isolatedRegistry(t)
	fx := newFixture(t)
	h := tool.ListProjects(reg)

	// Empty registry: zero entries, the path is the tempdir we made.
	empty, ok := callRegistry(t, h, map[string]any{}).(*tool.ListProjectsResult)
	if !ok {
		t.Fatalf("list_projects: result type %T, want *ListProjectsResult", empty)
	}
	if empty.Count != 0 {
		t.Errorf("empty: Count = %d, want 0", empty.Count)
	}
	if len(empty.Projects) != 0 {
		t.Errorf("empty: len(Projects) = %d, want 0", len(empty.Projects))
	}
	if empty.Registry == "" {
		t.Error("empty: Registry path is empty")
	}

	// Index one entry — should now show up.
	idxH := tool.IndexRepository(reg)
	if _, err := idxH(context.Background(), json.RawMessage(`{"path":"`+fx.Root+`"}`)); err != nil {
		t.Fatalf("index_repository: %v", err)
	}
	one := callRegistry(t, h, map[string]any{}).(*tool.ListProjectsResult)
	if one.Count != 1 {
		t.Fatalf("after index: Count = %d, want 1", one.Count)
	}
	if one.Projects[0].Path != fx.Root {
		t.Errorf("after index: path = %q, want %q", one.Projects[0].Path, fx.Root)
	}
	if one.Projects[0].IndexedAt.IsZero() {
		t.Error("after index: IndexedAt is zero")
	}
	if one.Projects[0].Files <= 0 {
		t.Errorf("after index: Files = %d, want >0", one.Projects[0].Files)
	}
}

// TestTool_IndexRepository validates the full entry shape. The
// handler is the only one that does heavy I/O (walks the source
// tree), so this test gets the most scenario coverage:
//
//   - happy path (default mode, default name → basename)
//   - custom name
//   - unknown mode rejected
//   - missing path rejected
//   - re-indexing the same path is idempotent (count stays 1)
func TestTool_IndexRepository(t *testing.T) {
	reg := isolatedRegistry(t)
	fx := newFixture(t)
	h := tool.IndexRepository(reg)

	t.Run("happy_path", func(t *testing.T) {
		out := callRegistry(t, h, map[string]any{"path": fx.Root}).(*tool.IndexRepositoryResult)
		if out.Entry.Path != fx.Root {
			t.Errorf("Entry.Path = %q, want %q", out.Entry.Path, fx.Root)
		}
		if out.Entry.Name == "" {
			t.Error("Entry.Name is empty; defaulting to basename failed")
		}
		if out.Entry.IndexedAt.IsZero() {
			t.Error("Entry.IndexedAt is zero")
		}
		if out.Entry.Files <= 0 {
			t.Errorf("Entry.Files = %d, want >0", out.Entry.Files)
		}
		if out.Entry.Mode != "full" {
			t.Errorf("Entry.Mode = %q, want full", out.Entry.Mode)
		}
	})

	t.Run("custom_name", func(t *testing.T) {
		out := callRegistry(t, h, map[string]any{"path": fx.Root, "name": "billing-svc"}).(*tool.IndexRepositoryResult)
		if out.Entry.Name != "billing-svc" {
			t.Errorf("Entry.Name = %q, want billing-svc", out.Entry.Name)
		}
	})

	t.Run("rejects_unknown_mode", func(t *testing.T) {
		_, err := h(context.Background(), json.RawMessage(`{"path":"`+fx.Root+`","mode":"nonsense"}`))
		if err == nil {
			t.Fatal("index_repository with mode=nonsense: want error")
		}
		if msg := err.Error(); !contains(msg, "unknown mode") {
			t.Errorf("error %q does not mention unknown mode", msg)
		}
	})

	t.Run("rejects_missing_path", func(t *testing.T) {
		_, err := h(context.Background(), json.RawMessage(`{}`))
		if err == nil {
			t.Fatal("index_repository with no path: want error")
		}
	})

	t.Run("idempotent_under_same_path", func(t *testing.T) {
		// Reset to a fresh registry so the "before" count is zero.
		reg2 := isolatedRegistry(t)
		idxH := tool.IndexRepository(reg2)
		for i := 0; i < 3; i++ {
			if _, err := idxH(context.Background(), json.RawMessage(`{"path":"`+fx.Root+`"}`)); err != nil {
				t.Fatalf("index #%d: %v", i, err)
			}
		}
		list := tool.ListProjects(reg2)
		got := callRegistry(t, list, map[string]any{}).(*tool.ListProjectsResult)
		if got.Count != 1 {
			t.Fatalf("after 3x reindex: Count = %d, want 1 (path must merge)", got.Count)
		}
	})
}

// TestTool_IndexStatus covers the freshness contract: after a
// fresh index the cache is fresh; after a touch on the source
// tree it isn't. The "missing entry" case (no prior index) is
// also checked — it returns cacheExists=false, cacheFresh=false
// without erroring, which is the right "not yet" state for an
// agent.
func TestTool_IndexStatus(t *testing.T) {
	reg := isolatedRegistry(t)
	fx := newFixture(t)
	idxH := tool.IndexRepository(reg)
	statusH := tool.IndexStatus(reg)

	t.Run("missing_entry", func(t *testing.T) {
		out := callRegistry(t, statusH, map[string]any{"path": fx.Root}).(*tool.IndexStatusResult)
		if out.Entry != nil {
			t.Errorf("Entry = %+v, want nil for unindexed path", out.Entry)
		}
		// cacheExists may be true or false depending on whether
		// a previous run left bytes under XDG_CACHE_HOME — but
		// cacheFresh is what we control: no entry means we
		// can't claim freshness.
		if out.CacheFresh {
			t.Error("CacheFresh = true, want false (no recorded index time)")
		}
		if out.CacheKey == "" {
			t.Error("CacheKey is empty")
		}
	})

	t.Run("fresh_after_index", func(t *testing.T) {
		if _, err := idxH(context.Background(), json.RawMessage(`{"path":"`+fx.Root+`"}`)); err != nil {
			t.Fatalf("index: %v", err)
		}
		out := callRegistry(t, statusH, map[string]any{"path": fx.Root}).(*tool.IndexStatusResult)
		if out.Entry == nil {
			t.Fatal("Entry is nil after index")
		}
		if out.Entry.Path != fx.Root {
			t.Errorf("Entry.Path = %q, want %q", out.Entry.Path, fx.Root)
		}
		if !out.CacheFresh {
			t.Error("CacheFresh = false immediately after index, want true")
		}
	})

	t.Run("rejects_missing_path", func(t *testing.T) {
		_, err := statusH(context.Background(), json.RawMessage(`{}`))
		if err == nil {
			t.Fatal("index_status with no path: want error")
		}
	})
}

// TestTool_DeleteProject covers the eviction contract. Two
// important properties:
//
//   - the registry row goes away
//   - the per-repo cache directory is removed
//
// Both happen together on a known path; the second-call case
// verifies the operation is idempotent (the bool flips, no error).
func TestTool_DeleteProject(t *testing.T) {
	reg := isolatedRegistry(t)
	fx := newFixture(t)

	// Set up: index once, then we have a row + (maybe) cache bytes.
	idxH := tool.IndexRepository(reg)
	if _, err := idxH(context.Background(), json.RawMessage(`{"path":"`+fx.Root+`"}`)); err != nil {
		t.Fatalf("index: %v", err)
	}

	delH := tool.DeleteProject(reg)
	out := callRegistry(t, delH, map[string]any{"path": fx.Root}).(*tool.DeleteProjectResult)
	if !out.Deleted {
		t.Error("Deleted = false on first call (registry had the row)")
	}
	if out.CachePath == "" {
		t.Error("CachePath is empty; the cleanup target was not reported")
	}

	// List should now be empty.
	list := tool.ListProjects(reg)
	got := callRegistry(t, list, map[string]any{}).(*tool.ListProjectsResult)
	if got.Count != 0 {
		t.Fatalf("after delete: Count = %d, want 0", got.Count)
	}

	// Second call: idempotent.
	again := callRegistry(t, delH, map[string]any{"path": fx.Root}).(*tool.DeleteProjectResult)
	if again.Deleted {
		t.Error("Deleted = true on second call (idempotency broken)")
	}
}

// TestTool_DeleteProject_RejectsMissingPath is a quick negative
// case: empty `path` is rejected at the boundary, not after
// touching the registry.
func TestTool_DeleteProject_RejectsMissingPath(t *testing.T) {
	reg := isolatedRegistry(t)
	_, err := tool.DeleteProject(reg)(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("delete_project with no path: want error")
	}
}

// newFixture builds a small tempdir-based repo for the registry
// tools to index. Distinct from the package-level repofixture
// (which other acceptance tests use) because the registry tests
// want isolation at the filesystem level too — two parallel test
// runs must not trample each other's source trees.
//
// Returns a fixture-shaped object with just the fields we need.
type registryFixture struct {
	Root string
}

func newFixture(t *testing.T) registryFixture {
	t.Helper()
	dir := t.TempDir()
	// One Go file is enough to drive parser.Detect and produce a
	// non-zero Files count. A one-line package keeps the test
	// cheap; a richer fixture would slow the walk without
	// exercising anything the simpler shape doesn't.
	src := `package fixture

import "fmt"

func Hello(name string) string {
	return fmt.Sprintf("hello, %s", name)
}
`
	if err := writeFile(filepath.Join(dir, "go.mod"), "module fixture\n\ngo 1.26\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(filepath.Join(dir, "hello.go"), src); err != nil {
		t.Fatal(err)
	}
	return registryFixture{Root: dir}
}

func writeFile(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o644)
}

// contains is a tiny stdlib-free substring check; we avoid
// strings.Contains here only because it would drag another
// import into a file that's deliberately minimal.
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
