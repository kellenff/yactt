package store_test

import (
	"strings"
	"testing"

	"github.com/kellenff/yactt/internal/parser"
	"github.com/kellenff/yactt/internal/store"
)

// findSymbol returns the first symbol in the file whose Name matches
// `name`, or fails the test. Used to write tests that don't depend on
// the iteration order of repo.Symbols.
func findSymbol(t *testing.T, r *store.Repo, file, name string) parser.Symbol {
	t.Helper()
	for _, s := range r.Symbols(file) {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("symbol %q not found in %s", name, file)
	return parser.Symbol{}
}

// TestSignature covers the public Signature accessor. The signature
// must include the function/method declaration line. We don't pin the
// LSP-or-tree-sitter path here — the chunker is a Tier-2 consumer and
// the path is exercised in node_test.go.
func TestSignature(t *testing.T) {
	r, fix := loadFixture(t)
	login := findSymbol(t, r, fix.LoginPath, "Login")

	got := r.Signature(fix.LoginPath, login)
	if !strings.Contains(got, "func Login") {
		t.Errorf("Signature for Login = %q, want it to contain %q", got, "func Login")
	}

	// Method signature must include the receiver name.
	user := findSymbol(t, r, fix.UserPath, "Greet")
	got = r.Signature(fix.UserPath, user)
	if !strings.Contains(got, "func (") || !strings.Contains(got, "Greet") {
		t.Errorf("Signature for User.Greet = %q, want method-decl shape", got)
	}
}

// TestSignature_MissingFile returns "" rather than panicking when the
// file is unavailable. The chunker relies on this for graceful
// degradation when a file disappears between Symbols and CachedFile.
func TestSignature_MissingFile(t *testing.T) {
	r, _ := loadFixture(t)
	got := r.Signature("/no/such/file.go", parser.Symbol{Name: "Foo", StartRow: 0, EndRow: 1})
	if got != "" {
		t.Errorf("Signature for missing file = %q, want \"\"", got)
	}
}

// TestPackageOf covers the per-file package derivation. The fixture
// has two Go packages (auth, payments); a file at the repo root gets
// the root package.
func TestPackageOf(t *testing.T) {
	r, fix := loadFixture(t)
	if got, want := r.PackageOf(fix.LoginPath), "auth"; got != want {
		t.Errorf("PackageOf(login) = %q, want %q", got, want)
	}
	if got, want := r.PackageOf(fix.PaymentPath), "payments"; got != want {
		t.Errorf("PackageOf(pay) = %q, want %q", got, want)
	}
}

// TestSymbolID covers the canonical id round-trip. The chunker relies
// on this matching entity.Entity.ID() byte-for-byte so callers/callees
// references and node_get tool invocations line up.
func TestSymbolID(t *testing.T) {
	r, fix := loadFixture(t)
	login := findSymbol(t, r, fix.LoginPath, "Login")

	id, err := r.SymbolID(fix.LoginPath, login)
	if err != nil {
		t.Fatalf("SymbolID: %v", err)
	}
	if id != "fn:auth.Login" {
		t.Errorf("SymbolID(Login) = %q, want %q", id, "fn:auth.Login")
	}

	// Method ID must include the receiver in canonical form.
	greet := findSymbol(t, r, fix.UserPath, "Greet")
	id, err = r.SymbolID(fix.UserPath, greet)
	if err != nil {
		t.Fatalf("SymbolID(Greet): %v", err)
	}
	if id != "meth:auth.User.Greet" {
		t.Errorf("SymbolID(Greet) = %q, want %q", id, "meth:auth.User.Greet")
	}
}

// TestSymbolID_RepofixturePin is a parity check: SymbolID's output
// must match what the id.For helper produces for the same Symbol
// triple. This pins the canonical-id source-of-truth decision made
// in PR 1 of the chunker plan: chunker uses SymbolID, never id.For
// inline.
func TestSymbolID_RepofixturePin(t *testing.T) {
	r, fix := loadFixture(t)
	charge := findSymbol(t, r, fix.PaymentPath, "Charge")

	got, err := r.SymbolID(fix.PaymentPath, charge)
	if err != nil {
		t.Fatalf("SymbolID: %v", err)
	}
	if !strings.HasPrefix(got, "fn:payments.") {
		t.Errorf("SymbolID(Charge) = %q, want fn:payments.* prefix", got)
	}
}
