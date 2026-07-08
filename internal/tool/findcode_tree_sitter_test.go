package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/kellenff/yactt/internal/store"
	"github.com/kellenff/yactt/internal/store/repofixture"
)

// loadRepoFromFixture builds a fresh *store.Repo from a tempdir fixture
// (Go + TypeScript files) and returns it. The per-test fixture gives
// the tree-sitter runner both grammars to compile against, exercising
// the per-language compile-once cache.
func loadRepoFromFixture(t *testing.T) *store.Repo {
	t.Helper()
	fx := repofixture.New(t)
	r, _, err := store.Load(fx.Root)
	if err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// callFindCode drives the handler with `pattern_kind=tree_sitter` and
// returns the unwrapped []FindCodeMatch slice.
func callFindCode(t *testing.T, repo *store.Repo, argsJSON string) []FindCodeMatch {
	t.Helper()
	out, err := FindCode(repo)(context.Background(), json.RawMessage(argsJSON))
	if err != nil {
		t.Fatalf("handler error: %v (args=%s)", err, argsJSON)
	}
	env, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("envelope type: got %T", out)
	}
	matches, ok := env["matches"].([]FindCodeMatch)
	if !ok {
		t.Fatalf("matches slice type: got %T", env["matches"])
	}
	return matches
}

// TestFindCode_TreeSitter_CallExpression exercises the happy path:
// `(call_expression) @c` matches every call site across both Go and
// TypeScript files in the fixture. Asserts ≥1 hit per expected file.
func TestFindCode_TreeSitter_CallExpression(t *testing.T) {
	repo := loadRepoFromFixture(t)
	matches := callFindCode(t, repo, `{"pattern":"(call_expression) @c","pattern_kind":"tree_sitter","limit":50}`)

	if len(matches) == 0 {
		t.Fatalf("expected matches for (call_expression); got 0")
	}
	var (
		login bool
		multi bool
		user  bool
	)
	for _, m := range matches {
		switch {
		case strings.HasSuffix(m.File, "auth/login.go"):
			login = true
		case strings.HasSuffix(m.File, "auth/multi.go"):
			multi = true
		case strings.HasSuffix(m.File, "auth/user.go"):
			user = true
		}
	}
	if !login {
		t.Errorf("expected ≥1 hit in auth/login.go; got %+v", fileNames(matches))
	}
	if !multi {
		t.Errorf("expected ≥1 hit in auth/multi.go; got %+v", fileNames(matches))
	}
	if !user {
		t.Errorf("expected ≥1 hit in auth/user.go; got %+v", fileNames(matches))
	}
}

// TestFindCode_TreeSitter_IncludeContext confirms the context path:
// every emitted match carries the enclosing function/class metadata
// when include_context=true.
func TestFindCode_TreeSitter_IncludeContext(t *testing.T) {
	repo := loadRepoFromFixture(t)
	matches := callFindCode(t, repo, `{"pattern":"(call_expression) @c","pattern_kind":"tree_sitter","include_context":true,"limit":10}`)

	if len(matches) == 0 {
		t.Fatalf("expected matches; got 0")
	}
	withCtx := 0
	for _, m := range matches {
		if m.Context != nil {
			withCtx++
		}
	}
	if withCtx == 0 {
		t.Fatalf("expected at least one match with non-nil Context; got %+v", matches)
	}
}

// TestFindCode_TreeSitter_Limit pins the limit gate: a query that
// matches many sites must cap at limit. The handler overscans
// internally so the response can surface `truncated` + `totalCount`
// honestly; the wire `matches` slice is trimmed to the user-requested
// limit (issue #33).
func TestFindCode_TreeSitter_Limit(t *testing.T) {
	repo := loadRepoFromFixture(t)
	out, err := FindCode(repo)(context.Background(), json.RawMessage(`{"pattern":"(call_expression) @c","pattern_kind":"tree_sitter","limit":2}`))
	if err != nil {
		t.Fatalf("find_code: %v", err)
	}
	env, _ := out.(map[string]any)
	matches, _ := env["matches"].([]FindCodeMatch)
	if len(matches) != 2 {
		t.Fatalf("expected exactly 2 matches on the wire; got %d (%+v)", len(matches), fileNames(matches))
	}
	truncated, _ := env["truncated"].(bool)
	if !truncated {
		t.Errorf("expected truncated=true when overscanned beyond limit; got false (envelope=%+v)", env)
	}
	total, _ := env["totalCount"].(int)
	if total <= 2 {
		t.Errorf("expected totalCount > 2 (overscan window); got %d", total)
	}
}

// TestFindCode_TreeSitter_EmptyResult verifies the envelope shape for
// a query that compiles but matches nothing. The fixture has no for
// loops in either Go or TypeScript, so the result is an empty slice,
// not an error.
func TestFindCode_TreeSitter_EmptyResult(t *testing.T) {
	repo := loadRepoFromFixture(t)
	matches := callFindCode(t, repo, `{"pattern":"(for_statement) @fs","pattern_kind":"tree_sitter","limit":10}`)
	if len(matches) != 0 {
		t.Fatalf("expected empty matches; got %d (%+v)", len(matches), fileNames(matches))
	}
}

// TestFindCode_TreeSitter_CompileError confirms a malformed pattern
// surfaces as a user-readable error rather than a panic or empty
// result. Checks both the *sitter.QueryError wrap and the project's
// canonical "invalid tree-sitter query" prefix.
func TestFindCode_TreeSitter_CompileError(t *testing.T) {
	repo := loadRepoFromFixture(t)
	_, err := FindCode(repo)(context.Background(), json.RawMessage(`{"pattern":"(function_declaration","pattern_kind":"tree_sitter","limit":10}`))
	if err == nil {
		t.Fatalf("expected compile error; got nil")
	}
	var qe *sitter.QueryError
	if !errors.As(err, &qe) {
		t.Fatalf("expected wrapped *sitter.QueryError; got %T (%v)", err, err)
	}
	if !strings.Contains(err.Error(), "invalid tree-sitter query") {
		t.Errorf("error message missing standard prefix: %q", err.Error())
	}
}

// TestFindCode_TreeSitter_CrossLanguage verifies the per-language
// compile-once cache: a single call covers Go and TypeScript files in
// the same pass. The fixture carries auth/user.ts (with
// `recordUsage(amount)`) and auth/login.go (with `Charge(token)`).
// Both must surface.
func TestFindCode_TreeSitter_CrossLanguage(t *testing.T) {
	repo := loadRepoFromFixture(t)
	matches := callFindCode(t, repo, `{"pattern":"(call_expression) @c","pattern_kind":"tree_sitter","limit":50}`)
	var seenGo, seenTS bool
	for _, m := range matches {
		switch {
		case strings.HasSuffix(m.File, ".go"):
			seenGo = true
		case strings.HasSuffix(m.File, ".ts"):
			seenTS = true
		}
	}
	if !seenGo {
		t.Errorf("expected Go hits; got %+v", fileNames(matches))
	}
	if !seenTS {
		t.Errorf("expected TypeScript hits; got %+v", fileNames(matches))
	}
}

// TestFindCode_TreeSitter_PredicateEq_Match verifies predicate
// evaluation via cursor.FilterPredicates: the (#eq? @n "Charge")
// predicate must keep only call sites whose callee name equals "Charge".
// The fixture has three Charge call sites (auth/login.go's
// Authenticate, auth/multi.go's Alpha.Ping, auth/user.go's User.Refresh);
// none of the other call sites (e.g. Refund) survive.
//
// Note: tree-sitter predicates live INSIDE the parenthesized pattern
// (per the upstream S-expression grammar), not as a separate clause.
// Putting `(#eq? @n "X")` outside the parens makes the smacker binding
// parse it as a second pattern, which leaves the first pattern
// (the structural one) without predicates — empirically observed.
func TestFindCode_TreeSitter_PredicateEq_Match(t *testing.T) {
	repo := loadRepoFromFixture(t)
	matches := callFindCode(t, repo, `{"pattern":"(call_expression function: (identifier) @n (#eq? @n \"Charge\"))","pattern_kind":"tree_sitter","limit":50}`)
	if len(matches) == 0 {
		t.Fatalf("expected Charge call sites; got 0")
	}
	for _, m := range matches {
		if !strings.Contains(m.Snippet, "Charge") {
			t.Errorf("predicate should filter matches whose callee != Charge; got %+v", m)
		}
	}
}

// TestFindCode_TreeSitter_PredicateEq_NoMatch verifies that a
// predicate rejecting every match results in an empty result set,
// not an error. The pattern compiles fine (valid S-expression, valid
// node type, valid capture) but the predicate excludes every site.
func TestFindCode_TreeSitter_PredicateEq_NoMatch(t *testing.T) {
	repo := loadRepoFromFixture(t)
	matches := callFindCode(t, repo, `{"pattern":"(call_expression function: (identifier) @n (#eq? @n \"DoesNotExistAnywhere\"))","pattern_kind":"tree_sitter","limit":50}`)
	if len(matches) != 0 {
		t.Fatalf("expected predicate to filter every match; got %d (%+v)", len(matches), fileNames(matches))
	}
}

// TestFindCode_TreeSitter_PredicateEq_PartialFilter verifies that
// the predicate trims to a subset, not all-or-nothing. (#eq? @n
// "Refund") must surface only the Refund call in auth/multi.go's
// Beta.Ping; the Charge calls in the fixture must be filtered out.
func TestFindCode_TreeSitter_PredicateEq_PartialFilter(t *testing.T) {
	repo := loadRepoFromFixture(t)
	matches := callFindCode(t, repo, `{"pattern":"(call_expression function: (identifier) @n (#eq? @n \"Refund\"))","pattern_kind":"tree_sitter","limit":50}`)
	if len(matches) == 0 {
		t.Fatalf("expected at least one Refund call site; got 0")
	}
	for _, m := range matches {
		if !strings.Contains(m.Snippet, "Refund") {
			t.Errorf("filtered match leaked through predicate; got %+v", m)
		}
	}
}

// TestFindCode_TreeSitter_PredicateMatch verifies #match? (a regex
// match against a capture's source text). (#match? @n "^Ch") must
// keep every name that starts with "Ch" — Charge call sites — and
// reject Refund.
func TestFindCode_TreeSitter_PredicateMatch(t *testing.T) {
	repo := loadRepoFromFixture(t)
	matches := callFindCode(t, repo, `{"pattern":"(call_expression function: (identifier) @n (#match? @n \"^Ch\"))","pattern_kind":"tree_sitter","limit":50}`)
	if len(matches) == 0 {
		t.Fatalf("expected at least one Ch-prefixed call site; got 0")
	}
	for _, m := range matches {
		if !strings.Contains(m.Snippet, "Charge") {
			t.Errorf("#match? ^Ch should keep only Charge; got %+v", m)
		}
	}
}

// fileNames collapses matches to "File L<line>" strings for failure
// messages so a long list of matches doesn't drown the diff.
func fileNames(matches []FindCodeMatch) []string {
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m.File+" L"+lineStr(m.LineRange.Start))
	}
	return out
}

func lineStr(n int) string {
	if n == 0 {
		return "0"
	}
	const digits = "0123456789"
	if n < 0 {
		return "-" + lineStr(-n)
	}
	var s []byte
	for n > 0 {
		s = append([]byte{digits[n%10]}, s...)
		n /= 10
	}
	return string(s)
}

// guard that os is imported (used in some harnesses); keeps the
// dependency visible for future test helpers.
var _ = os.Getenv
