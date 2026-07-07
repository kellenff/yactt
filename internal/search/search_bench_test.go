package search_test

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/kellenff/yactt/internal/search"
	"github.com/kellenff/yactt/internal/store"
)

// buildInlineRepo writes a small Go-only repo to a tempdir and Loads it.
// Used to exercise Search without depending on tests/fixtures/sample-go
// (which would couple the bench to that fixture's shape).
//
// ponytail: three files with ~10 declared symbols each — enough that the
// search loop's per-file work is non-trivial, small enough that the
// bench starts in <100ms.
func buildInlineRepo(b *testing.B) *store.Repo {
	b.Helper()
	dir := b.TempDir()
	files := map[string]string{
		"auth/login.go": `package auth

import "context"

type Session struct{ User string }

// Login validates credentials and returns a Session.
func Login(ctx context.Context, user, pass string) (Session, error) {
	if user == "" {
		return Session{}, ErrBadCreds
	}
	return Session{User: user}, nil
}

// Logout terminates a Session.
func Logout(s Session) error { return nil }

var ErrBadCreds = errStr("bad credentials")

func errStr(s string) error { return &strErr{s} }

type strErr struct{ s string }

func (e *strErr) Error() string { return e.s }
`,
		"payments/pay.go": `package payments

// Charge processes a payment and returns the receipt id.
func Charge(amount int, currency string) (string, error) { return "rcpt-" + currency, nil }

// Refund reverses a Charge.
func Refund(rcpt string) error { return nil }

// History returns the last N charges for an account.
func History(account string, n int) []string { return nil }
`,
		"util/util.go": `package util

// Truncate shortens a string to max runes.
func Truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// Join concatenates strings with a separator.
func Join(parts []string, sep string) string { return "" }

// Split is the inverse of Join.
func Split(s, sep string) []string { return nil }
`,
	}
	for rel, body := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			b.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			b.Fatalf("write: %v", err)
		}
	}
	repo, _, err := store.Load(dir)
	if err != nil {
		b.Fatalf("store.Load: %v", err)
	}
	return repo
}

// BenchmarkSearch exercises the term+kind path. Most agent prompts are
// "find function Foo" or "find methods in package bar" — measured here.
func BenchmarkSearch(b *testing.B) {
	repo := buildInlineRepo(b)
	q := search.Query{
		Terms: []string{"Charge"},
		Kind:  nil, // all kinds
		Limit: 50,
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = search.Search(repo, q)
	}
}

// BenchmarkSearchRegex exercises the regex path — agents frequently ask
// "find every function matching pattern X" (YankBank-style filters).
func BenchmarkSearchRegex(b *testing.B) {
	repo := buildInlineRepo(b)
	// ponytail: anchored prefix regex so the workload is comparable to a
	// real "find every method starting with `find_`" ask.
	pat := regexp.MustCompile(`^find_`)
	q := search.Query{
		Regex: pat,
		Limit: 50,
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = search.Search(repo, q)
	}
}

// BenchmarkSearchWithScope constrains the search to a single sub-package,
// mirroring the "in auth" qualifier an agent often appends.
func BenchmarkSearchWithScope(b *testing.B) {
	repo := buildInlineRepo(b)
	q := search.Query{
		Terms: []string{"Session"},
		Limit: 50,
		// Scope is repo-relative; resolved against the loaded root.
	}
	// scope is set per-call so the absolute path resolution runs each time
	// — that path is part of what we're measuring.
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q.Scope = filepath.Join(repo.Root(), "auth")
		_ = search.Search(repo, q)
	}
}