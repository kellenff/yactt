package project_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kellenff/yactt/internal/project"
	"github.com/kellenff/yactt/internal/registry"
	"github.com/kellenff/yactt/internal/store"
	"github.com/kellenff/yactt/internal/store/repofixture"
)

func TestParseRef_Valid(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"file:///abs/path", "/abs/path"},
		{"file:///abs/path/", "/abs/path"},
		{"file:///abs/path/with/%20space", "/abs/path/with/ space"},
		{"file:///abs/path/with/%2Fslash", "/abs/path/with/slash"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			ref, err := project.ParseRef(tc.in)
			if err != nil {
				t.Fatalf("ParseRef(%q) error: %v", tc.in, err)
			}
			if ref.Path != tc.want {
				t.Errorf("Path = %q, want %q", ref.Path, tc.want)
			}
		})
	}
}

func TestParseRef_Rejects(t *testing.T) {
	cases := []struct {
		in     string
		wantIs error
	}{
		{"", project.ErrEmpty},
		{"   ", project.ErrEmpty},
		{"/abs/path", project.ErrUnsupportedScheme},
		{"git://host/abs/path", project.ErrUnsupportedScheme},
		{"https://example.com/foo", project.ErrUnsupportedScheme},
		{"file:/abs/path", project.ErrUnsupportedScheme},
		{"file://host/abs/path", project.ErrNonLocal},
		{"file://localhost/abs", project.ErrNonLocal},
		{"file://relative", project.ErrNotAbsolute},
		{"file:///foo/%2e%2e/bar", project.ErrNotAbsolute},
		{"file:///foo/../bar", project.ErrNotAbsolute},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			_, err := project.ParseRef(tc.in)
			if err == nil {
				t.Fatalf("ParseRef(%q) succeeded; want error", tc.in)
			}
			if !strings.Contains(err.Error(), tc.wantIs.Error()) {
				t.Errorf("ParseRef(%q) error = %v; want containing %v", tc.in, err, tc.wantIs)
			}
		})
	}
}

func TestResolve_HitsRegistry(t *testing.T) {
	fx := repofixture.New(t)
	dir := t.TempDir()
	reg := registry.New(filepath.Join(dir, "projects.json"))
	if err := newIndexStub(context.Background(), fx.Root, reg); err != nil {
		t.Fatalf("seed: %v", err)
	}
	repo, err := project.Resolve(reg, "file://"+fx.Root)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if repo.Root() != fx.Root {
		t.Errorf("Root = %q, want %q", repo.Root(), fx.Root)
	}
}

func TestResolve_NotIndexed(t *testing.T) {
	dir := t.TempDir()
	reg := registry.New(filepath.Join(dir, "projects.json"))
	_, err := project.Resolve(reg, "file:///no/such/path")
	if err == nil {
		t.Fatal("Resolve: expected error for unregistered path")
	}
	if !strings.Contains(err.Error(), project.ErrNotIndexed.Error()) {
		t.Errorf("error = %v; want containing ErrNotIndexed", err)
	}
}

func TestResolve_Canonicalization(t *testing.T) {
	fx := repofixture.New(t)
	dir := t.TempDir()
	reg := registry.New(filepath.Join(dir, "projects.json"))
	if err := newIndexStub(context.Background(), fx.Root, reg); err != nil {
		t.Fatalf("seed: %v", err)
	}
	for _, raw := range []string{"file://" + fx.Root, "file://" + fx.Root + "/"} {
		repo, err := project.Resolve(reg, raw)
		if err != nil {
			t.Fatalf("Resolve(%q) error: %v", raw, err)
		}
		_ = repo.Close()
	}
}

// newIndexStub seeds a registry entry by calling the same logic
// tool.IndexRepository uses, but without the audit/TOFU hooks
// (those land in a later task). Mirrors enough of IndexRepository
// for the seed step.
func newIndexStub(ctx context.Context, root string, reg *registry.Registry) error {
	abs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	if _, err := os.Stat(abs); err != nil {
		return err
	}
	repo, _, err := store.Load(abs, registry.LoadOptsWithDiskCache(abs)...)
	if err != nil {
		return err
	}
	_ = repo.Close()
	return reg.Upsert(registry.Entry{
		Name:      filepath.Base(abs),
		Path:      abs,
		IndexedAt: time.Now().UTC(),
		Files:     0,
	})
}