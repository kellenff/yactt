package lsp

import "testing"

// TestFileURI_NormalisesPath is the regression guard for the tier-2
// finding: paths handed to fileURI were concatenated raw
// ("file://" + path). A path containing `/./`, `//`, or whitespace
// produced a malformed URI that LSP servers parse incorrectly.
// Normalising via filepath.Clean + url.URL handles the clean cases and
// the reserved-char cases without breaking the existing wire format.
func TestFileURI(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain absolute", "/tmp/repo", "file:///tmp/repo"},
		{"trailing slash trimmed", "/tmp/repo/", "file:///tmp/repo"},
		{"double slash collapsed", "/tmp//repo", "file:///tmp/repo"},
		{"dot segment collapsed", "/tmp/./repo", "file:///tmp/repo"},
		{"parent resolved", "/tmp/repo/..", "file:///tmp"},
		{"space percent-encoded", "/tmp/foo bar", "file:///tmp/foo%20bar"},
		{"hash percent-encoded", "/tmp/foo#bar", "file:///tmp/foo%23bar"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := fileURI(tc.in)
			if got != tc.want {
				t.Errorf("fileURI(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}