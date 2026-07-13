package tool

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kellenff/yactt/internal/registry"
)

// TestJoinDotted covers the four-cell truth table of joinDotted:
//
//	pkg=name=""     -> ""            (both branches fire)
//	pkg="",name="X" -> "X"           (pkg branch)
//	pkg="X",name="" -> "X"           (name branch)
//	pkg="X",name="Y" -> "X.Y"         (concat)
//
// Each cell kills the relevant CONDITIONALS_* mutation in joinDotted.
func TestJoinDotted(t *testing.T) {
	cases := []struct {
		name     string
		pkg, sym string
		want     string
	}{
		{"both empty", "", "", ""},
		{"empty pkg", "", "Sym", "Sym"},
		{"empty name", "Pkg", "", "Pkg"},
		{"both present", "Pkg", "Sym", "Pkg.Sym"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := joinDotted(tc.pkg, tc.sym)
			if got != tc.want {
				t.Errorf("joinDotted(%q, %q) = %q, want %q", tc.pkg, tc.sym, got, tc.want)
			}
		})
	}
}

// TestBaseName_LastSegment covers the split-slash branch. baseName must
// return the segment after the final '/'. Empty path and no-slash paths
// are also covered.
func TestBaseName_LastSegment(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"auth/login.go", "login.go"},
		{"/abs/path/auth/login.go", "login.go"},
		{"login.go", "login.go"},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := baseName(tc.in); got != tc.want {
				t.Errorf("baseName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestRelPath_TrimsRootPrefix mirrors the relPath helper: it strips the
// root prefix and the leading separator.
// seedRegForRoot creates a fresh registry containing one entry for the
// given absolute root path. Used by per-tool unit tests that load a
// repo via store.Load and then need a *registry.Registry to call the
// migrated tool factories with. The tool factory takes the registry
// (not the repo); the resolved repo is loaded by project.Resolve
// inside the handler.
//
// Ponytail: live in helpers_test.go so wire_shape_test.go,
// wire_shape_bench_test.go, and the per-tool test files can all
// share one definition.
func seedRegForRoot(tb testing.TB, root string) *registry.Registry {
	tb.Helper()
	dir := tb.TempDir()
	reg := registry.New(filepath.Join(dir, "projects.json"))
	if err := reg.Upsert(registry.Entry{
		Name:      filepath.Base(root),
		Path:      root,
		IndexedAt: time.Now().UTC(),
		Files:     0,
	}); err != nil {
		tb.Fatalf("seedRegForRoot: %v", err)
	}
	return reg
}

// seedRegFromRepo is the convenience wrapper that takes a loaded
// *store.Repo. Use it right after a `r, _, err := store.Load(...)`
// call in tests.
func seedRegFromRepo(tb testing.TB, repo interface{ Root() string }) *registry.Registry {
	return seedRegForRoot(tb, repo.Root())
}

// withProject prepends a `project` (file:// URI) field to the args
// JSON if the field is missing. Mirrors what every per-tool handler
// does at the start of a request: ensure `project` is set. Used by
// the test helpers (callSnippet, findSymbols, etc.) so the per-tool
// args JSON in the test bodies doesn't have to know the tempdir
// path up front.
//
// Args are expected to be a JSON object literal starting with `{`.
// The returned string is also a JSON object literal.
func withProject(root, args string) string {
	if strings.Contains(args, `"project"`) {
		return args
	}
	return `{"project":"file://` + root + `",` + args[1:]
}

func TestRelPath_TrimsRootPrefix(t *testing.T) {
	cases := []struct {
		root, in, want string
	}{
		{"/repo", "/repo/auth/login.go", "auth/login.go"},
		{"/repo", "/repo/", ""},
		{"/repo", "auth/login.go", "auth/login.go"}, // no root prefix -> still trimmed of leading /
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := relPath(tc.root, tc.in); got != tc.want {
				t.Errorf("relPath(%q, %q) = %q, want %q", tc.root, tc.in, got, tc.want)
			}
		})
	}
}
