package tool_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kellenff/yactt/internal/store"
	"github.com/kellenff/yactt/internal/tool"
)

// inlineFixtureRepo builds a tiny Go repo and Loads it once per bench.
// The fixture is intentionally minimal — Login + Authenticate + Charge —
// but exercises every handler a "real" agent would reach for in a
// navigation session.
//
// ponytail: inline rather than tests/fixtures/sample-go so the bench is
// self-contained; tests/acceptance already covers the larger fixture.
func inlineFixtureRepo(b *testing.B) (root string, repo *store.Repo) {
	b.Helper()
	dir := b.TempDir()
	for _, sub := range []string{"auth", "payments"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			b.Fatalf("mkdir: %v", err)
		}
	}

	files := map[string]string{
		"auth/login.go": `package auth

type Session struct{ User string }

// Login authenticates a user.
func Login(user, pass string) (Session, error) {
	if user == "" {
		return Session{}, ErrBadCreds
	}
	return Session{User: user}, nil
}

// Authenticate runs the validation + payment flow.
func Authenticate(token string) (Session, error) {
	if err := Charge(token); err != nil {
		return Session{}, err
	}
	return Session{}, nil
}

var ErrBadCreds = errStr("bad credentials")

type strErr string

func (e strErr) Error() string { return string(e) }
func errStr(s string) error    { return strErr(s) }
`,
		"payments/pay.go": `package payments

// Charge processes a payment.
func Charge(token string) (string, error) { return "rcpt-" + token, nil }

// Refund reverses a Charge.
func Refund(rcpt string) error { return nil }
`,
	}
	for rel, body := range files {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
			b.Fatalf("write %s: %v", rel, err)
		}
	}
	r, _, err := store.Load(dir)
	if err != nil {
		b.Fatalf("store.Load: %v", err)
	}
	return dir, r
}

// dispatch drives a tool handler end-to-end with the given argsJSON and
// fails the bench on error. Used by every BenchmarkTool* below.
func dispatch(b *testing.B, h func(ctx context.Context, args json.RawMessage) (any, error), argsJSON string) {
	b.Helper()
	if _, err := h(context.Background(), json.RawMessage(argsJSON)); err != nil {
		b.Fatalf("handler: %v", err)
	}
}

// BenchmarkToolTreeOverview — the orientation ask. Depth 2 is what a
// real "what's in this repo" prompt requests.
func BenchmarkToolTreeOverview(b *testing.B) {
	root, repo := inlineFixtureRepo(b)
	h := tool.TreeOverview(repo)
	args := `{"repo":"` + root + `","depth":2}`
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dispatch(b, h, args)
	}
}

// BenchmarkToolFindCode — the "find every function matching X" ask.
// Anchored regex keeps the cost comparable across iters.
func BenchmarkToolFindCode(b *testing.B) {
	root, repo := inlineFixtureRepo(b)
	h := tool.FindCode(repo)
	args := `{"pattern":"^func (Login|Authenticate|Charge|Refund)$","pattern_kind":"regex","scope":"` + root + `","limit":20}`
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dispatch(b, h, args)
	}
}

// BenchmarkToolFindSymbol — the "find Login" / "find meth:User.Greet" ask.
func BenchmarkToolFindSymbol(b *testing.B) {
	_, repo := inlineFixtureRepo(b)
	h := tool.FindSymbol(repo)
	args := `{"name_path":"auth.Login"}`
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dispatch(b, h, args)
	}
}

// BenchmarkToolNodeEdges — the "what does X call" ask. callees tier-0
// path. Ponytail: pick Authenticate because its body has a real
// cross-package callee (Charge) that surfaces in Tier 0.
func BenchmarkToolNodeEdges(b *testing.B) {
	_, repo := inlineFixtureRepo(b)
	h := tool.NodeEdges(repo)
	args := `{"id":"fn:auth.Authenticate","kinds":["callees"]}`
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dispatch(b, h, args)
	}
}

// BenchmarkToolNodeGet_signature — cheap layer. baseline.
func BenchmarkToolNodeGet_signature(b *testing.B) {
	_, repo := inlineFixtureRepo(b)
	h := tool.GetNode(repo)
	args := `{"id":"fn:auth.Login","layers":["signature"]}`
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dispatch(b, h, args)
	}
}

// BenchmarkToolNodeGet_body — moderate layer. Should be slower than
// signature because body materialisation re-reads + parses the file.
func BenchmarkToolNodeGet_body(b *testing.B) {
	_, repo := inlineFixtureRepo(b)
	h := tool.GetNode(repo)
	args := `{"id":"fn:auth.Login","layers":["body"]}`
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dispatch(b, h, args)
	}
}

// BenchmarkToolNodeGet_source — heaviest layer (full file bytes).
// Most expensive of the three — confirms the bench is exercising the
// right axis (signature < body < source).
func BenchmarkToolNodeGet_source(b *testing.B) {
	_, repo := inlineFixtureRepo(b)
	h := tool.GetNode(repo)
	args := `{"id":"fn:auth.Login","layers":["source"]}`
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dispatch(b, h, args)
	}
}

// BenchmarkToolFindReferencingSymbols — the "where is X used" ask.
// Exercises the symbol→callers Tier-1 (LSP) with Tier-2 (tree-sitter)
// fallback.
func BenchmarkToolFindReferencingSymbols(b *testing.B) {
	_, repo := inlineFixtureRepo(b)
	h := tool.FindReferencingSymbols(repo)
	args := `{"symbol":"fn:auth.Login"}`
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dispatch(b, h, args)
	}
}