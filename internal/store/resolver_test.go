package store_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/kellenff/yactt/internal/id"
	"github.com/kellenff/yactt/internal/store"
)

func TestLookupByName(t *testing.T) {
	r, _ := loadFixture(t)
	hits := r.Lookup("", "Login")
	if len(hits) != 1 {
		t.Fatalf("Lookup(\"\", Login) = %d hits, want 1: %+v", len(hits), hits)
	}
	if hits[0].Sym.Name != "Login" {
		t.Errorf("hit name = %q, want Login", hits[0].Sym.Name)
	}
}

func TestLookupByPkgAndName(t *testing.T) {
	r, _ := loadFixture(t)
	hits := r.Lookup("auth", "Login")
	if len(hits) != 1 {
		t.Fatalf("Lookup(auth, Login) = %d hits, want 1: %+v", len(hits), hits)
	}
	if hits[0].Sym.Name != "Login" {
		t.Errorf("hit name = %q", hits[0].Sym.Name)
	}
	if hits[0].Sym.Kind != "function_declaration" {
		t.Errorf("hit kind = %q", hits[0].Sym.Kind)
	}
}

func TestLookupByPkgOnly(t *testing.T) {
	r, _ := loadFixture(t)
	hits := r.Lookup("auth", "")
	// Four auth-package source files: login.go (Login + Session), user.go
	// (User + Greet + Refresh method), multi.go (Alpha, Beta types and Ping
	// methods), user.ts (User class + greet + refresh methods). At least 7
	// declarations; extras from the TS file raise the floor.
	if len(hits) < 7 {
		t.Errorf("Lookup(auth, \"\") = %d hits, want >=7: %+v", len(hits), hits)
	}
	// Every hit must be from the auth package.
	for _, h := range hits {
		base := h.File
		if i := strings.LastIndexByte(base, '/'); i >= 0 {
			base = base[i+1:]
		}
		switch base {
		case "login.go", "user.go", "multi.go", "user.ts":
			// ok
		default:
			t.Errorf("pkg-only hit out of package: %s", h.File)
		}
	}
}

func TestLookupSortedByFilePath(t *testing.T) {
	r, _ := loadFixture(t)
	hits := r.Lookup("auth", "")
	for i := 1; i < len(hits); i++ {
		if hits[i-1].File > hits[i].File {
			t.Errorf("hits not sorted by file: %q > %q", hits[i-1].File, hits[i].File)
		}
	}
}

func TestLookupMostSpecificWins(t *testing.T) {
	// Charge() exists in payments; nothing in auth called Charge.
	r, _ := loadFixture(t)
	if got := r.Lookup("auth", "Charge"); len(got) != 0 {
		t.Errorf("Lookup(auth, Charge) = %+v, want empty (Charge is in payments)", got)
	}
	if got := r.Lookup("payments", "Charge"); len(got) != 1 {
		t.Errorf("Lookup(payments, Charge) = %d hits, want 1", len(got))
	}
}

func TestLookupEmpty(t *testing.T) {
	r, _ := loadFixture(t)
	// Both empty: every symbol.
	hits := r.Lookup("", "")
	// login.go (2) + user.go (2: type + method) + multi.go (4: 2 types + 2
	// methods) + payments/pay.go (2) = 10 symbols.
	if len(hits) < 10 {
		t.Errorf("Lookup(\"\", \"\") = %d hits, want >=10: %+v", len(hits), hits)
	}
}

func TestLookupMiss(t *testing.T) {
	r, _ := loadFixture(t)
	if got := r.Lookup("auth", "NotExisting"); len(got) != 0 {
		t.Errorf("Lookup miss = %+v, want empty", got)
	}
}

func TestLocateSymbolFile(t *testing.T) {
	r, fix := loadFixture(t)
	relPath := "auth/login.go"
	id := id.File(relPath)
	file, sym, ok, err := r.LocateSymbol(id)
	if err != nil {
		t.Fatalf("LocateSymbol(file): %v", err)
	}
	if !ok {
		t.Fatal("LocateSymbol(file) ok=false")
	}
	if file != fix.LoginPath {
		t.Errorf("file = %q, want %q", file, fix.LoginPath)
	}
	if sym.Kind != "source_file" {
		t.Errorf("sym kind = %q, want source_file", sym.Kind)
	}
	if sym.StartRow != 0 {
		t.Errorf("sym.StartRow = %d, want 0", sym.StartRow)
	}
}

func TestLocateSymbolFunction(t *testing.T) {
	r, _ := loadFixture(t)
	id := id.Function("auth", "", "Login")
	file, sym, ok, err := r.LocateSymbol(id)
	if err != nil {
		t.Fatalf("LocateSymbol(fn): %v", err)
	}
	if !ok || sym.Name != "Login" {
		t.Errorf("got (%v, %+v), want Login", ok, sym)
	}
	if sym.Kind != "function_declaration" {
		t.Errorf("kind = %q, want function_declaration", sym.Kind)
	}
	if !endsWith(file, "login.go") {
		t.Errorf("file = %q, want login.go", file)
	}
}

func TestLocateSymbolClass(t *testing.T) {
	r, _ := loadFixture(t)
	id := id.Class("auth", "Session")
	_, sym, ok, err := r.LocateSymbol(id)
	if err != nil {
		t.Fatalf("LocateSymbol(class): %v", err)
	}
	if !ok {
		t.Fatal("ok=false")
	}
	if sym.Name != "Session" {
		t.Errorf("name = %q, want Session", sym.Name)
	}
	if sym.Kind != "type_declaration" {
		t.Errorf("kind = %q, want type_declaration", sym.Kind)
	}
}

func TestLocateSymbolMethod(t *testing.T) {
	r, _ := loadFixture(t)
	id := id.Method("auth", "User", "Greet")
	_, sym, ok, err := r.LocateSymbol(id)
	if err != nil {
		t.Fatalf("LocateSymbol(method): %v", err)
	}
	if !ok || sym.Name != "Greet" {
		t.Errorf("got (%v, %+v), want Greet", ok, sym)
	}
	if sym.Kind != "method_declaration" {
		t.Errorf("kind = %q, want method_declaration", sym.Kind)
	}
	if sym.Receiver != "User" {
		t.Errorf("sym.Receiver = %q, want User", sym.Receiver)
	}
}

func TestLocateSymbolMethodDisambiguatesSameName(t *testing.T) {
	// auth/multi.go declares both Alpha.Ping and Beta.Ping. The receiver
	// captured at parse time is what makes this work: a name-only match in
	// the same file would otherwise pick whichever the loop hit first.
	r, fix := loadFixture(t)
	cases := []struct {
		class string
	}{
		{"Alpha"},
		{"Beta"},
	}
	for _, tc := range cases {
		t.Run(tc.class, func(t *testing.T) {
			nodeID := id.Method("auth", tc.class, "Ping")
			file, sym, ok, err := r.LocateSymbol(nodeID)
			if err != nil {
				t.Fatalf("LocateSymbol: %v", err)
			}
			if !ok {
				t.Fatalf("ok=false for %s.Ping", tc.class)
			}
			if sym.Receiver != tc.class {
				t.Errorf("sym.Receiver = %q, want %q", sym.Receiver, tc.class)
			}
			if sym.Name != "Ping" {
				t.Errorf("sym.Name = %q, want Ping", sym.Name)
			}
			if file != fix.MultiPath {
				t.Errorf("file = %q, want %q", file, fix.MultiPath)
			}
		})
	}
}

func TestLocateSymbolMethodUnknownClass(t *testing.T) {
	// Unknown receiver type → ErrNotFound, never a wrong-method return.
	r, _ := loadFixture(t)
	nodeID := id.Method("auth", "NoSuchType", "Ping")
	_, _, _, err := r.LocateSymbol(nodeID)
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestLocateSymbolNotFound(t *testing.T) {
	r, _ := loadFixture(t)
	id := id.Function("auth", "", "DoesNotExist")
	_, _, _, err := r.LocateSymbol(id)
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestLocateSymbolRepoKind(t *testing.T) {
	// Repo-kind IDs aren't locatable within a repo; should produce ErrNotFound.
	r, _ := loadFixture(t)
	id := id.Repo("/some/path")
	_, _, _, err := r.LocateSymbol(id)
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("repo id err = %v, want ErrNotFound", err)
	}
}

func TestLocateSymbolMalformedFunctionID(t *testing.T) {
	// Document the boundary: Parse() rejects empty bodies; callers should
	// branch on the parse error before invoking LocateSymbol.
	if _, err := id.Parse("fn:"); err == nil {
		t.Fatal("Parse should reject empty fn body")
	}
	r, _ := loadFixture(t)
	// Parsed-but-malformed (no dotted receiver/name) propagates the parse
	// error from FunctionParts.
	malformed := id.MustParse("fn:onlyname")
	_, _, _, err := r.LocateSymbol(malformed)
	if err == nil {
		t.Error("expected error for fn id without dotted receiver/name")
	}
}

// endsWith reports whether s ends with suffix. Tiny helper to keep the test
// file's path comparisons readable.
func endsWith(s, suffix string) bool {
	if len(s) < len(suffix) {
		return false
	}
	return s[len(s)-len(suffix):] == suffix
}
