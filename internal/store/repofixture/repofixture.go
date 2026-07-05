// Package repofixture builds a small tempdir-based Go repo for store and
// search tests. The fixture is intentionally minimal:
//
//	auth/login.go      Login function, Session type
//	auth/user.go       User type, User.Greet and User.Refresh methods
//	auth/multi.go      Alpha.Ping and Beta.Ping — same-named methods on
//	                   different receivers, used to verify the resolver
//	                   disambiguates methods correctly.
//	auth/user.ts       User class, greet + refresh methods
//	payments/pay.go    Charge function, Refund function
//	payments/pay.ts    recordUsage function
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
	UserTSPath   string
	PaymentTSPath string
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

// Authenticate runs the validation + payment flow. The body calls into
// the payments package without an import (so the call is unresolved but
// still recorded by tree-sitter). This gives the persisted call-edge index
// a real cross-package edge to surface in store-level tests, while keeping
// symbol-name analysis clean — the callee has exactly one declaration, in
// the payments package.
func Authenticate(token string) (Session, error) {
	if err := Charge(token); err != nil {
		return Session{}, err
	}
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

// Refresh reconciles the user's billing snapshot. The body makes a
// cross-package call used to verify that the persisted call-edge
// index walks method_declaration bodies alongside function_declaration
// ones.
func (u *User) Refresh(amount int) error {
	return Charge(amount)
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
	// disambiguate `meth:auth.Alpha.Ping` from `meth:auth.Beta.Ping` AND
	// the persisted call-edge index keys each method independently under
	// (file::Receiver.Name). Alpha.Ping calls Charge and Beta.Ping calls
	// Refund; both are unresolved cross-package calls recorded by tree-
	// sitter. Without per-receiver keying, both methods would collide
	// under `multi.go::Ping` and the per-method call sets would merge.
	multiSrc := `package auth

// Alpha is the first dual-receiver fixture.
type Alpha struct{ Value string }

// Ping returns "alpha".
func (a *Alpha) Ping() string { return Charge(0) }

// Beta is the second dual-receiver fixture.
type Beta struct{ Value int }

// Ping returns "beta".
func (b *Beta) Ping() string { return Refund("x") }
`
	if err := os.WriteFile(filepath.Join(dir, "auth", "multi.go"), []byte(multiSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	// Non-Go file: must be skipped.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("plain text\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// TypeScript counterparts — exercise the TS parser path on the same
	// fixture so the persisted call-edge index's language-agnostic walker
	// can be pinned against method_definition bodies in addition to Go's
	// method_declaration ones.
	userTSSrc := `export class User {
	name: string;
	email: string;

	constructor(name: string, email: string) {
		this.name = name;
		this.email = email;
	}

	greet(): string {
		return "hi " + this.name;
	}

	// refresh reconciles the user's billing snapshot. Walks a
	// method_definition body to verify the persisted call-edge index
	// covers TypeScript class methods alongside Go's.
	refresh(amount: number): Error {
		return recordUsage(amount);
	}
}
`
	if err := os.WriteFile(filepath.Join(dir, "auth", "user.ts"), []byte(userTSSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	payTSSrc := `// recordUsage reconciles a billing entry for the given amount.
export function recordUsage(amount: number): Error {
	return null;
}
`
	if err := os.WriteFile(filepath.Join(dir, "payments", "pay.ts"), []byte(payTSSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	// .foo file: also hidden, also must be skipped.
	if err := os.WriteFile(filepath.Join(dir, ".foo", "should_skip.go"), []byte("package foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	return &Fixture{
		Root:          dir,
		LoginPath:     filepath.Join(dir, "auth", "login.go"),
		UserPath:      filepath.Join(dir, "auth", "user.go"),
		MultiPath:     filepath.Join(dir, "auth", "multi.go"),
		PaymentPath:   filepath.Join(dir, "payments", "pay.go"),
		UserTSPath:    filepath.Join(dir, "auth", "user.ts"),
		PaymentTSPath: filepath.Join(dir, "payments", "pay.ts"),
		NotesPath:     filepath.Join(dir, "notes.txt"),
		HiddenGoPath:  filepath.Join(dir, ".foo", "should_skip.go"),
	}
}
