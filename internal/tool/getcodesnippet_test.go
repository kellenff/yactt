package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kellenff/yactt/internal/store"
)

// TestGetCodeSnippet_ByID exercises the stable-id affordance. Asserts
// the resolved Kind + that the body contains the function header.
func TestGetCodeSnippet_ByID(t *testing.T) {
	r := loadTestRepo(t)
	out := callSnippet(t, r, `{"id":"fn:auth.Login"}`)

	if out.ID != "fn:auth.Login" {
		t.Errorf("id = %q, want fn:auth.Login", out.ID)
	}
	if out.Kind != "FUNCTION" {
		t.Errorf("kind = %q, want FUNCTION", out.Kind)
	}
	if !strings.Contains(out.Text, "func Login") {
		t.Errorf("text does not contain function header; got:\n%s", out.Text)
	}
	if out.Ambiguous != 0 {
		t.Errorf("ambiguous = %d, want 0", out.Ambiguous)
	}
	if out.Encoding != "utf-8" {
		t.Errorf("encoding = %q, want utf-8", out.Encoding)
	}
}

// TestGetCodeSnippet_ByNamePath_Dotted covers the dotted qualified name
// (most common Go-style).
func TestGetCodeSnippet_ByNamePath_Dotted(t *testing.T) {
	r := loadTestRepo(t)
	out := callSnippet(t, r, `{"name_path":"auth.Login"}`)

	if out.ID != "fn:auth.Login" {
		t.Errorf("id = %q, want fn:auth.Login", out.ID)
	}
	if out.Kind != "FUNCTION" {
		t.Errorf("kind = %q, want FUNCTION", out.Kind)
	}
	if !strings.Contains(out.Text, "func Login") {
		t.Errorf("text missing function header")
	}
}

// TestGetCodeSnippet_ByNamePath_Slash covers the slash variant. Many
// agents pass paths in this shape.
func TestGetCodeSnippet_ByNamePath_Slash(t *testing.T) {
	r := loadTestRepo(t)
	out := callSnippet(t, r, `{"name_path":"auth/Login"}`)

	if out.ID != "fn:auth.Login" {
		t.Errorf("id = %q, want fn:auth.Login", out.ID)
	}
}

// TestGetCodeSnippet_ByNamePath_Bare covers the unqualified case (no
// dot or slash). Useful when the user already has a single-match name
// and doesn't want to guess the package.
func TestGetCodeSnippet_ByNamePath_Bare(t *testing.T) {
	r := loadTestRepo(t)
	out := callSnippet(t, r, `{"name_path":"Refund"}`)

	if !strings.Contains(out.Text, "func Refund") {
		t.Errorf("text missing function header; got:\n%s", out.Text)
	}
}

// TestGetCodeSnippet_Method covers method-level resolution. The fixture
// has `meth:auth.User.Greet`; passing "auth.User.Greet" should resolve.
func TestGetCodeSnippet_Method(t *testing.T) {
	r := loadTestRepo(t)
	out := callSnippet(t, r, `{"name_path":"auth.User.Greet"}`)

	if out.ID != "meth:auth.User.Greet" {
		t.Errorf("id = %q, want meth:auth.User.Greet", out.ID)
	}
	if out.Kind != "METHOD" {
		t.Errorf("kind = %q, want METHOD", out.Kind)
	}
	if !strings.Contains(out.Text, "func (u *User) Greet") {
		t.Errorf("text missing method header; got:\n%s", out.Text)
	}
}

// TestGetCodeSnippet_Ambiguous is the documented disambiguation case.
// Both Alpha.Ping and Beta.Ping exist; name_path="Ping" matches both. We
// return the first with ambiguous=2 rather than silently picking one.
func TestGetCodeSnippet_Ambiguous(t *testing.T) {
	r := loadTestRepo(t)
	out := callSnippet(t, r, `{"name_path":"Ping"}`)

	if out.Ambiguous != 2 {
		t.Errorf("ambiguous = %d, want 2 (Alpha.Ping, Beta.Ping)", out.Ambiguous)
	}
	// First match in file-sorted order is always Alpha.Ping
	// (auth/multi.go is unique; alpha comes before beta in source order,
	// so id.For returns Alpha's id first).
	if !strings.Contains(out.Text, "alpha") && !strings.Contains(out.Text, "Alpha") {
		t.Errorf("expected first match to mention Alpha; got:\n%s", out.Text)
	}
}

// TestGetCodeSnippet_NoArgs asserts we reject the empty input.
func TestGetCodeSnippet_NoArgs(t *testing.T) {
	r := loadTestRepo(t)
	_, err := GetCodeSnippet(seedRegFromRepo(t, r))(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error on empty args, got nil")
	}
	if !strings.Contains(err.Error(), "id or name_path is required") {
		t.Errorf("expected required-arg error; got %q", err.Error())
	}
}

// TestGetCodeSnippet_UnknownID asserts we error cleanly on a non-existent id.
func TestGetCodeSnippet_UnknownID(t *testing.T) {
	r := loadTestRepo(t)
	_, err := GetCodeSnippet(seedRegFromRepo(t, r))(context.Background(), json.RawMessage(`{"id":"fn:does.not.Exist"}`))
	if err == nil {
		t.Fatal("expected error on unknown id, got nil")
	}
}

// TestGetCodeSnippet_UnknownNamePath covers a name_path that resolves to
// zero declarations.
func TestGetCodeSnippet_UnknownNamePath(t *testing.T) {
	r := loadTestRepo(t)
	_, err := GetCodeSnippet(seedRegFromRepo(t, r))(context.Background(), json.RawMessage(`{"name_path":"NoSuchSymbol"}`))
	if err == nil {
		t.Fatal("expected error on unknown name_path, got nil")
	}
}

// TestGetCodeSnippet_DidYouMeanInError verifies the error text on a
// misspelled name_path embeds an edit-distance suggestion. Regression
// guard for issue #33.
func TestGetCodeSnippet_DidYouMeanInError(t *testing.T) {
	r := loadTestRepo(t)
	// "Loginn" is edit-distance 1 from "Login" — the suggestion hint
	// should be present in the error text.
	_, err := GetCodeSnippet(seedRegFromRepo(t, r))(context.Background(), json.RawMessage(`{"name_path":"auth.Loginn"}`))
	if err == nil {
		t.Fatal("expected error on misspelled name_path")
	}
	if !strings.Contains(err.Error(), "did you mean") || !strings.Contains(err.Error(), "Login") {
		t.Errorf("expected error to embed 'did you mean: Login'; got %q", err.Error())
	}
}

// --- helpers --------------------------------------------------------------

func callSnippet(t *testing.T, repo *store.Repo, args string) *GetCodeSnippetResult {
	t.Helper()
	out, err := GetCodeSnippet(seedRegFromRepo(t, repo))(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("handler: %v (args=%s)", err, args)
	}
	gs, ok := out.(*GetCodeSnippetResult)
	if !ok {
		t.Fatalf("result type: got %T", out)
	}
	return gs
}