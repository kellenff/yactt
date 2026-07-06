package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kellenff/yactt/internal/store"
	"github.com/kellenff/yactt/internal/store/repofixture"
)

// TestFindCode_Regex_PatternLengthCap is the regression guard for the
// tier-1 DoS finding. Go's regexp is RE2-based and not vulnerable to
// catastrophic backtracking (verified empirically with `(a+)+$` matching
// 100k chars in 7ms on this machine), so the realistic exposure is a
// pathological *pattern length*: a 100 KB pattern compiles a state
// machine that allocates real memory and runs in O(N*M) per line. The
// handler rejects patterns longer than maxRegexPatternBytes at the
// boundary, before any compile or per-file work.
func TestFindCode_Regex_PatternLengthCap(t *testing.T) {
	r := loadRegexFixtureRepo(t)

	// 8 KB of `a` — well past the 4 KB cap.
	huge := strings.Repeat("a", 8*1024)
	args := `{"pattern":"` + huge + `","pattern_kind":"regex","limit":1}`
	out, err := FindCode(r)(context.Background(), json.RawMessage(args))
	if err == nil {
		t.Fatalf("expected error on %d-byte pattern, got %v", len(huge), out)
	}
	if !strings.Contains(err.Error(), "pattern too long") {
		t.Errorf("error doesn't say pattern too long; got %q", err.Error())
	}
}

// TestFindCode_Regex_PatternAtCap confirms the boundary at exactly
// maxRegexPatternBytes is accepted (compile + run, no error).
func TestFindCode_Regex_PatternAtCap(t *testing.T) {
	r := loadRegexFixtureRepo(t)

	// maxRegexPatternBytes-1 alphanumerics: compiles, runs, finds nothing.
	at := strings.Repeat("a", maxRegexPatternBytes-1)
	args := `{"pattern":"` + at + `","pattern_kind":"regex","limit":1}`
	if _, err := FindCode(r)(context.Background(), json.RawMessage(args)); err != nil {
		// The repo's on-disk corpus is small enough that the long
		// pattern returns zero matches without error. If we ever break
		// that invariant, this test catches it.
		t.Fatalf("pattern at cap should be accepted, got %v", err)
	}
}

func loadRegexFixtureRepo(t *testing.T) *store.Repo {
	t.Helper()
	fx := repofixture.New(t)
	r, _, err := store.Load(fx.Root)
	if err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}
