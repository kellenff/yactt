package tool

import (
	"testing"

	"github.com/kellenff/yactt/internal/registry"
	"github.com/kellenff/yactt/internal/registry/registrytest"
	"github.com/kellenff/yactt/internal/store/repofixture"
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
// given absolute root path. Thin wrapper around registrytest.Seed
// kept here for backward compatibility with the dozen-plus tool
// test files that already call it.
func seedRegForRoot(tb testing.TB, root string) *registry.Registry {
	tb.Helper()
	return registrytest.Seed(tb, root)
}

// seedRegFromRepo is the convenience wrapper that takes a loaded
// *store.Repo. Thin wrapper around registrytest.SeedFromRepo.
func seedRegFromRepo(tb testing.TB, repo interface{ Root() string }) *registry.Registry {
	tb.Helper()
	return registrytest.SeedFromRepo(tb, repo)
}

// withProject prepends a `project` (file:// URI) field to the args
// JSON if the field is missing. Thin wrapper around
// repofixture.WithProject — the canonical implementation lives
// next to the Fixture type so test code that has a Fixture handle
// can also use fx.WithProject(args) if it wants to.
func withProject(root, args string) string {
	return repofixture.WithProject(root, args)
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
