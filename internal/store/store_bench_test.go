package store_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kellenff/yactt/internal/id"
	"github.com/kellenff/yactt/internal/parser"
	"github.com/kellenff/yactt/internal/store"
)

// inlineFixture writes a tiny Go-only fixture (one file declaring
// `Login`, `Authenticate`, and `Charge`) and Loads it. Mirrors the
// shape repofixture.New provides, but lives inline so the bench file
// has zero cross-package test-fixture coupling.
//
// ponytail: the choice to inline rather than call repofixture.New is
// deliberate — repofixture.New's `t *testing.T` parameter is a concrete
// type, and *testing.B is not assignable to it. Inlining keeps the
// bench self-contained at the cost of ~40 lines of fixture.
func inlineFixture(b *testing.B) (root, loginPath, paymentPath string, repo *store.Repo) {
	b.Helper()
	dir := b.TempDir()

	for _, sub := range []string{"auth", "payments"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			b.Fatalf("mkdir: %v", err)
		}
	}

	login := `package auth

type Session struct{ User string }

// Login authenticates a user and returns a session.
func Login(user, pass string) (Session, error) {
	if user == "" {
		return Session{}, ErrBadCreds
	}
	return Session{User: user}, nil
}

// Authenticate runs the validation + payment flow. Calls Charge.
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
`
	pay := `package payments

// Charge processes a payment and returns the receipt id.
func Charge(token string) (string, error) { return "rcpt-" + token, nil }

// Refund reverses a Charge.
func Refund(rcpt string) error { return nil }
`
	loginPath = filepath.Join(dir, "auth", "login.go")
	paymentPath = filepath.Join(dir, "payments", "pay.go")
	if err := os.WriteFile(loginPath, []byte(login), 0o644); err != nil {
		b.Fatalf("write login: %v", err)
	}
	if err := os.WriteFile(paymentPath, []byte(pay), 0o644); err != nil {
		b.Fatalf("write pay: %v", err)
	}
	r, _, err := store.Load(dir)
	if err != nil {
		b.Fatalf("store.Load: %v", err)
	}
	return dir, loginPath, paymentPath, r
}

// BenchmarkLoadFixture measures the cold-path cost of a full repo load.
// First-cache-miss latency a user pays on every fresh install.
//
// ponytail: each iter rebuilds fixture from a fresh TempDir (CleanUp
// fires). Means the bench reports real "build a repo" cost, not amortised
// per-iter drift. Skip with `-short` when iterating quickly.
func BenchmarkLoadFixture(b *testing.B) {
	root, _, _, _ := inlineFixture(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := store.Load(root); err != nil {
			b.Fatalf("store.Load: %v", err)
		}
	}
}

// BenchmarkReloadInvalidate measures the cost of invalidating one file
// (the Tier-0 index rebuild). Equivalent to what fsnotify fires on a
// watched-file change.
func BenchmarkReloadInvalidate(b *testing.B) {
	_, loginPath, _, repo := inlineFixture(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := repo.ReloadInvalidate(loginPath); err != nil {
			b.Fatalf("ReloadInvalidate: %v", err)
		}
	}
}

// BenchmarkLocateSymbol exercises the by-ID lookup node_get uses on
// every request.
func BenchmarkLocateSymbol(b *testing.B) {
	_, _, _, repo := inlineFixture(b)
	const nodeID = "fn:auth.Login"
	want, err := id.Parse(nodeID)
	if err != nil {
		b.Fatalf("parseID: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, _, err := repo.LocateSymbol(want); err != nil {
			b.Fatalf("LocateSymbol: %v", err)
		}
	}
}

// BenchmarkEdgesByCallee exercises the Tier-0 call-edge lookup node_edges
// "callees" uses.
func BenchmarkEdgesByCallee(b *testing.B) {
	_, _, _, repo := inlineFixture(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = repo.EdgesByCallee("Charge")
	}
}

// BenchmarkEdgesByCaller exercises the by-caller index node_edges
// "callers" Tier-2 uses when LSP isn't present.
func BenchmarkEdgesByCaller(b *testing.B) {
	root, _, paymentPath, repo := inlineFixture(b)
	sym := parser.Symbol{Kind: "function_declaration", Name: "Charge"}
	rel, _ := filepath.Rel(root, paymentPath)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = repo.EdgesByCaller(rel, sym)
	}
}

// BenchmarkImportsIn exercises the persisted IMPORTS index added in V3.
// Hot path when an agent asks "what does this file depend on?".
func BenchmarkImportsIn(b *testing.B) {
	root, loginPath, _, repo := inlineFixture(b)
	rel, _ := filepath.Rel(root, loginPath)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = repo.ImportsIn(rel)
	}
}