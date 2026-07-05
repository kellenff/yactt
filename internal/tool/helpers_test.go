package tool

import "testing"

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
