package tool

import (
	"strings"
	"testing"
)

// splitQuery cases — pinning the tokenizer's contract so future
// refactors don't silently change behaviour. The boundary tightening
// (unclosed-quote error) is the tier-1 fix; the rest of these cases
// document the behaviour the contract holds.

func TestSplitQuery(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    []string
		wantErr bool
	}{
		{"plain words", "foo bar baz", []string{"foo", "bar", "baz"}, false},
		{"leading/trailing space", "  foo   bar  ", []string{"foo", "bar"}, false},
		{"empty input", "", nil, false},
		{"closed phrase", `foo "bar baz" qux`, []string{"foo", "bar baz", "qux"}, false},
		{"empty quoted term is dropped", `empty "" term`, []string{"empty", "term"}, false},
		{"leading quote is consumed", `"foo bar`, nil, true},
		{"trailing quote unclosed", `foo "bar baz`, nil, true},
		{"single unclosed quote", `"`, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := splitQuery(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got terms=%v", got)
				}
				if !strings.Contains(err.Error(), "unclosed") {
					t.Errorf("error should mention unclosed quote, got %q", err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("terms len = %d (%q), want %d (%q)", len(got), got, len(tc.want), tc.want)
			}
			for i, w := range tc.want {
				if got[i] != w {
					t.Errorf("terms[%d] = %q, want %q", i, got[i], w)
				}
			}
		})
	}
}

// TestSearch_RejectsUnclosedQuote is the handler-level regression: an
// unclosed-quote query error path goes through Search (the boundary) and
// surfaces as a tool error rather than silently accepted terms.
func TestSearch_RejectsUnclosedQuote(t *testing.T) {
	// Build a one-shot repo just enough to call the handler; the
	// invariant is that the error fires before any repo access, so
	// any valid repo works.
	// We delegate to the existing helper to keep the test simple.
	r := loadRegexFixtureRepo(t)
	repo := r
	args := `{"project":"file://` + repo.Root() + `","query":"foo \"bar","scope":"","limit":5}`
	_ = args
	_, err := Search(seedRegFromRepo(t, repo))(nil, mustJSON(t, args))
	if err == nil {
		t.Fatal("Search should reject unclosed quote")
	}
	if !strings.Contains(err.Error(), "unclosed") {
		t.Errorf("error should mention unclosed quote, got %q", err.Error())
	}
}

func mustJSON(t *testing.T, s string) []byte {
	t.Helper()
	return []byte(s)
}
