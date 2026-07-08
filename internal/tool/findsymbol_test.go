package tool

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kellenff/yactt/internal/store"
)

// findSymbols drives tool.FindSymbol and returns the symbols slice from
// the {"symbols":[...]} envelope. Test-local helper — mirrors the style
// of callSnippet in getcodesnippet_test.go.
func findSymbols(t *testing.T, repo *store.Repo, args string) []FindSymbolResult {
	t.Helper()
	out, err := FindSymbol(repo)(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("find_symbol: %v (args=%s)", err, args)
	}
	env, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("envelope type: got %T", out)
	}
	hits, ok := env["symbols"].([]FindSymbolResult)
	if !ok {
		t.Fatalf("symbols slice type: got %T", env["symbols"])
	}
	return hits
}

// TestFindSymbol_DottedNamePath_AuthLogin pins the regression for issue
// #28: the canonical Go form "auth.Login" must resolve to the same node
// id as the slash form. Without the fix the dotted form silently
// returned an empty slice.
func TestFindSymbol_DottedNamePath_AuthLogin(t *testing.T) {
	r := loadTestRepo(t)
	hits := findSymbols(t, r, `{"name_path":"auth.Login","limit":5}`)

	if len(hits) == 0 {
		t.Fatal("expected at least one match for auth.Login; got none")
	}
	if hits[0].Node == nil || hits[0].Node.ID != "fn:auth.Login" {
		t.Fatalf("first hit node id=%q want fn:auth.Login", hits[0].Node.ID)
	}
}

// TestFindSymbol_SlashAndDottedAgree drives both forms and asserts the
// first hit's id is identical — model UX guarantee: callers shouldn't
// have to remember which separator is canonical.
func TestFindSymbol_SlashAndDottedAgree(t *testing.T) {
	r := loadTestRepo(t)

	slash := findSymbols(t, r, `{"name_path":"auth/Login","limit":5}`)
	dotted := findSymbols(t, r, `{"name_path":"auth.Login","limit":5}`)

	if len(slash) == 0 {
		t.Fatal("slash form returned no matches")
	}
	if len(dotted) == 0 {
		t.Fatal("dotted form returned no matches")
	}
	if slash[0].Node == nil || slash[0].Node.ID != "fn:auth.Login" {
		t.Fatalf("slash hit id=%q want fn:auth.Login", slash[0].Node.ID)
	}
	if dotted[0].Node == nil || dotted[0].Node.ID != "fn:auth.Login" {
		t.Fatalf("dotted hit id=%q want fn:auth.Login", dotted[0].Node.ID)
	}
}

// TestFindSymbol_BareName_Login keeps the bare-name fallback (no
// separator at all) working — same input reaches the matcher as a
// name-only lookup.
func TestFindSymbol_BareName_Login(t *testing.T) {
	r := loadTestRepo(t)
	hits := findSymbols(t, r, `{"name_path":"Login","limit":5}`)

	if len(hits) == 0 {
		t.Fatal("expected at least one match for bare 'Login'")
	}
	// Login is unique in the fixture, so first hit is the function.
	if hits[0].Node == nil || hits[0].Node.ID != "fn:auth.Login" {
		t.Fatalf("first hit id=%q want fn:auth.Login", hits[0].Node.ID)
	}
}

// TestFindSymbol_SlashKindPrefix_ClassUserMethod exercises the slash
// path with the kind-segment strip — the dotted form doesn't support
// this and shouldn't regress when the dotted branch was added.
func TestFindSymbol_SlashKindPrefix_ClassUserMethod(t *testing.T) {
	r := loadTestRepo(t)
	// No `class` segment matches the fixture (sample-go has functions
	// and methods, no classes); assert the kind-strip doesn't break the
	// bare-name slash form either.
	hits := findSymbols(t, r, `{"name_path":"fn/Login","limit":5}`)
	if len(hits) == 0 {
		t.Fatal("expected at least one match for fn/Login (kind-prefix stripped)")
	}
}