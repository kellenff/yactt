package search_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/search"
	"github.com/kellenff/yactt/internal/store"
	"github.com/kellenff/yactt/internal/store/repofixture"
)

// loadRepo loads the shared fixture and returns the repo + root path. Kept
// here (rather than reusing the store_test helper) so the search package's
// tests don't pull in store_test internal helpers.
func loadRepo(t *testing.T) (*store.Repo, string) {
	t.Helper()
	fix := repofixture.New(t)
	r, errs, err := store.Load(fix.Root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, e := range errs {
		t.Errorf("Load per-file err: %v", e)
	}
	return r, fix.Root
}

func findResult(t *testing.T, results []search.Result, name string) search.Result {
	t.Helper()
	for _, r := range results {
		if r.Node.Summary != "" && containsToken(r.Node.Summary, name) {
			return r
		}
		// Fallback: ID has the name as the last segment.
		if strings.HasSuffix(r.Node.ID, "."+name) {
			return r
		}
	}
	t.Fatalf("no result with name %q in %+v", name, results)
	return search.Result{}
}

func containsToken(haystack, needle string) bool {
	// Token-bounded match: word boundary by spaces/punctuation.
	if !strings.Contains(haystack, needle) {
		return false
	}
	idx := strings.Index(haystack, needle)
	before := idx == 0
	after := idx+len(needle) == len(haystack)
	if !before {
		c := haystack[idx-1]
		if c == ' ' || c == ':' || c == '.' || c == '(' {
			before = true
		}
	}
	if !after {
		c := haystack[idx+len(needle)]
		if c == ' ' || c == ':' || c == '.' || c == ')' {
			after = true
		}
	}
	return before && after
}

func TestSearchExactNameMatch(t *testing.T) {
	r, _ := loadRepo(t)
	res := search.Search(r, search.Query{Terms: []string{"Login"}})
	if len(res) == 0 {
		t.Fatal("no results for exact name match")
	}
	if res[0].Score < 0.9 {
		t.Errorf("exact match score = %v, want >= 0.9", res[0].Score)
	}
	if res[0].Node.Kind != domain.KindFunction {
		t.Errorf("first hit kind = %q, want FUNCTION", res[0].Node.Kind)
	}
}

func TestSearchSubstringMatch(t *testing.T) {
	r, _ := loadRepo(t)
	res := search.Search(r, search.Query{Terms: []string{"ogin"}})
	if len(res) == 0 {
		t.Fatal("no results for substring match")
	}
	// Substring scores should be in [0.6, 0.9).
	if res[0].Score < 0.6 || res[0].Score >= 0.9 {
		t.Errorf("substring score = %v, want [0.6, 0.9)", res[0].Score)
	}
}

func TestSearchSubstringLengthBonus(t *testing.T) {
	r, _ := loadRepo(t)
	short := search.Search(r, search.Query{Terms: []string{"ogi"}})
	long := search.Search(r, search.Query{Terms: []string{"ogin"}})
	if len(short) == 0 || len(long) == 0 {
		t.Fatal("missing results")
	}
	shortHit := findResult(t, short, "Login")
	longHit := findResult(t, long, "Login")
	if longHit.Score <= shortHit.Score {
		t.Errorf("long substring should score higher than short: %v vs %v", longHit.Score, shortHit.Score)
	}
}

func TestSearchDocCommentMatch(t *testing.T) {
	r, _ := loadRepo(t)
	// "authenticates" appears in Login's doc comment, not in any name.
	res := search.Search(r, search.Query{Terms: []string{"authenticates"}})
	if len(res) == 0 {
		t.Fatal("no results for doc-comment match")
	}
	if res[0].Score >= 0.6 {
		t.Errorf("doc-comment score = %v, want < 0.6", res[0].Score)
	}
	if res[0].Score < 0.3 {
		t.Errorf("doc-comment score = %v, want >= 0.3", res[0].Score)
	}
}

func TestSearchKindFilter(t *testing.T) {
	r, _ := loadRepo(t)

	// No symbol in the fixture has Method kind with "authenticates" anywhere.
	res := search.Search(r, search.Query{
		Terms: []string{"authenticates"},
		Kind:  []domain.NodeKind{domain.KindMethod},
	})
	if len(res) != 0 {
		t.Errorf("expected no method-kind matches for 'authenticates', got %d", len(res))
	}

	// Login has 'authenticates' in its doc comment; filtering Kind=Function
	// should keep Login (a function) and drop User (a class).
	res = search.Search(r, search.Query{
		Terms: []string{"authenticates"},
		Kind:  []domain.NodeKind{domain.KindFunction},
	})
	if len(res) == 0 {
		t.Error("expected Login function-kind match")
	}
	for _, r := range res {
		if r.Node.Kind != domain.KindFunction {
			t.Errorf("kind filter leaked: %q", r.Node.Kind)
		}
	}
}

func TestSearchScopeFilter(t *testing.T) {
	r, root := loadRepo(t)
	res := search.Search(r, search.Query{
		Terms: []string{"Charge"},
		Scope: root + "/payments",
	})
	if len(res) == 0 {
		t.Fatal("scope=payments should match Charge")
	}
	for _, hit := range res {
		if !strings.HasPrefix(hit.Node.PathContext, root+"/payments") {
			t.Errorf("scope leak: %s", hit.Node.PathContext)
		}
	}

	// Scope to auth only — Charge (payments) must drop out.
	res = search.Search(r, search.Query{
		Terms: []string{"Charge"},
		Scope: root + "/auth",
	})
	if len(res) != 0 {
		t.Errorf("scope=auth should not match Charge, got %d", len(res))
	}
}

func TestSearchRegex(t *testing.T) {
	r, _ := loadRepo(t)
	res := search.Search(r, search.Query{
		Regex: regexp.MustCompile(`^Cha`),
	})
	if len(res) == 0 {
		t.Fatal("regex should match Charge")
	}
	if res[0].Score < 0.5 {
		t.Errorf("regex match score = %v, want >= 0.5", res[0].Score)
	}
}

func TestSearchLimit(t *testing.T) {
	r, _ := loadRepo(t)
	res := search.Search(r, search.Query{
		Terms: []string{"o"}, // short substring matches Login, Refund, etc.
		Limit: 2,
	})
	if len(res) > 2 {
		t.Errorf("Limit=2 but got %d results", len(res))
	}
}

func TestSearchMiss(t *testing.T) {
	r, _ := loadRepo(t)
	res := search.Search(r, search.Query{Terms: []string{"thisdoesnotmatchanything"}})
	if len(res) != 0 {
		t.Errorf("expected empty results, got %d", len(res))
	}
}

func TestSearchSortedByScore(t *testing.T) {
	r, _ := loadRepo(t)
	res := search.Search(r, search.Query{Terms: []string{"o"}, Limit: 50})
	for i := 1; i < len(res); i++ {
		if res[i-1].Score < res[i].Score {
			t.Errorf("results not sorted by score: %v < %v at %d", res[i-1].Score, res[i].Score, i)
		}
	}
}

func TestSearchDefaultLimit(t *testing.T) {
	// Build a tempdir repo with many symbols so we hit the default limit.
	dir := t.TempDir()
	src := "package x\n"
	for i := 0; i < 20; i++ {
		src += "func Func" + itoa(i) + "() {}\n"
	}
	if err := writeFileFS(dir, "many.go", []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	r2, _, err := store.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	res := search.Search(r2, search.Query{Terms: []string{"Func"}})
	if len(res) > 10 {
		t.Errorf("default Limit should cap at 10, got %d", len(res))
	}
}

func TestSearchPathContext(t *testing.T) {
	r, _ := loadRepo(t)
	res := search.Search(r, search.Query{Terms: []string{"Login"}})
	if len(res) == 0 {
		t.Fatal("expected Login results")
	}
	if !strings.HasSuffix(res[0].Node.PathContext, "auth/login.go") {
		t.Errorf("PathContext = %q, want .../auth/login.go", res[0].Node.PathContext)
	}
}

func TestSearchIDsParseable(t *testing.T) {
	r, _ := loadRepo(t)
	res := search.Search(r, search.Query{Terms: []string{"Login"}})
	if len(res) == 0 {
		t.Fatal("expected Login results")
	}
	if res[0].Node.ID == "" {
		t.Error("ID should be set on result")
	}
	if !strings.HasPrefix(res[0].Node.ID, "fn:") {
		t.Errorf("function result ID should start with fn:, got %q", res[0].Node.ID)
	}
}

// itoa: small decimal converter; avoids the strconv import.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}

// writeFileFS writes bytes to dir/name with mode 0o644. Single-purpose
// helper used by TestSearchDefaultLimit.
func writeFileFS(dir, name string, content []byte, mode uint32) error {
	return os.WriteFile(filepath.Join(dir, name), content, os.FileMode(mode))
}
