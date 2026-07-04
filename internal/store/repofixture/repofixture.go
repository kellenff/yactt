// Package repofixture builds a small tempdir-based Go repo for store and
// search tests. The fixture is intentionally minimal:
//
//	auth/login.go      Login function, Session type
//	auth/user.go       User type, User.Greet method
//	auth/multi.go      Alpha.Ping and Beta.Ping — same-named methods on
//	                   different receivers, used to verify the resolver
//	                   disambiguates methods correctly.
//	payments/pay.go    Charge function, Refund function
//
// Plus a hidden `.git/` dir, a hidden `.foo/` dir, and a `notes.txt` file
// so the walker's skip behaviour can be verified from the same fixture.
package repofixture

import (
	"os"
	"path/filepath"
	"testing"
)

// Fixture is a constructed test repo.
type Fixture struct {
	Root         string
	LoginPath    string
	UserPath     string
	MultiPath    string
	PaymentPath  string
	NotesPath    string
	HiddenGoPath string // lives under .foo/; must NOT be indexed
}

// New constructs the fixture under t.TempDir(). Files are written 0o644.
// The fixture is fully self-contained; cleanup is automatic.
func New(t *testing.T) *Fixture {
	t.Helper()
	dir := t.TempDir()

	// Hidden dirs that the walker must skip.
	if err := os.MkdirAll(filepath.Join(dir, ".git", "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".foo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Real source tree.
	if err := os.MkdirAll(filepath.Join(dir, "auth"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "payments"), 0o755); err != nil {
		t.Fatal(err)
	}

	loginSrc := `package auth

// Login authenticates a user and returns a session.
func Login(user, pass string) (Session, error) {
	return Session{}, nil
}

// Session holds a user's auth state.
type Session struct {
	User string
	Tok  string
}
`
	if err := os.WriteFile(filepath.Join(dir, "auth", "login.go"), []byte(loginSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	userSrc := `package auth

// User represents an authenticated user.
type User struct {
	Name  string
	Email string
}

func (u *User) Greet() string {
	return "hi " + u.Name
}
`
	if err := os.WriteFile(filepath.Join(dir, "auth", "user.go"), []byte(userSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	paySrc := `package payments

// Charge processes a payment.
func Charge(amount int) error { return nil }

// Refund reverses a charge.
func Refund(txID string) error { return nil }
`
	if err := os.WriteFile(filepath.Join(dir, "payments", "pay.go"), []byte(paySrc), 0o644); err != nil {
		t.Fatal(err)
	}

	// Two types with same-named methods, used to verify the resolver can
	// disambiguate `meth:auth.Alpha.Ping` from `meth:auth.Beta.Ping`.
	multiSrc := `package auth

// Alpha is the first dual-receiver fixture.
type Alpha struct{ Value string }

// Ping returns "alpha".
func (a *Alpha) Ping() string { return "alpha" }

// Beta is the second dual-receiver fixture.
type Beta struct{ Value int }

// Ping returns "beta".
func (b *Beta) Ping() string { return "beta" }
`
	if err := os.WriteFile(filepath.Join(dir, "auth", "multi.go"), []byte(multiSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	// Non-Go file: must be skipped.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("plain text\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// .foo file: also hidden, also must be skipped.
	if err := os.WriteFile(filepath.Join(dir, ".foo", "should_skip.go"), []byte("package foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	return &Fixture{
		Root:         dir,
		LoginPath:    filepath.Join(dir, "auth", "login.go"),
		UserPath:     filepath.Join(dir, "auth", "user.go"),
		MultiPath:    filepath.Join(dir, "auth", "multi.go"),
		PaymentPath:  filepath.Join(dir, "payments", "pay.go"),
		NotesPath:    filepath.Join(dir, "notes.txt"),
		HiddenGoPath: filepath.Join(dir, ".foo", "should_skip.go"),
	}
}
