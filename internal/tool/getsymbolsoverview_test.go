package tool

import "testing"

// TestResolveFile is the regression guard for the tier-2 canonicalisation
// finding: the resolver must reject anything that resolves outside the
// repo (via `..`, an absolute path elsewhere, or a non-existent repo-
// relative path). The safety is the Files() filter — these tests pin the
// contract so a future change to the filter doesn't silently let out-of-
// repo paths through.
func TestResolveFile(t *testing.T) {
	r := loadRegexFixtureRepo(t) // any valid repo works for resolveFile
	root := r.Root()

	files := r.Files()
	if len(files) == 0 {
		t.Fatal("repo has no files")
	}
	known := files[0]
	// Path under root, e.g. "auth/login.go".
	rootRel := known[len(root)+1:]

	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"absolute path under root", known, known, true},
		{"repo-relative path", rootRel, known, true},
		{"absolute-with-leading-slash repo-relative", "/" + rootRel, known, true},
		{"parent segment escaping repo", "/../etc/passwd", "", false},
		{"absolute path outside repo", "/etc/passwd", "", false},
		{"non-existent repo-relative path", "nope/missing.go", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := resolveFile(r, tc.in)
			if ok != tc.ok {
				t.Fatalf("resolveFile(%q) ok = %v, want %v", tc.in, ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Errorf("resolveFile(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
