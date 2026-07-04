package store_test

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kellenff/yactt/internal/parser"
	"github.com/kellenff/yactt/internal/store"
	"github.com/kellenff/yactt/internal/store/repofixture"
)

func loadFixture(t *testing.T) (*store.Repo, *repofixture.Fixture) {
	t.Helper()
	fix := repofixture.New(t)
	r, errs, err := store.Load(fix.Root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Per-file failures (e.g. parse errors) shouldn't pop up here — the
	// fixture is hand-crafted to be clean.
	for _, e := range errs {
		t.Errorf("Load per-file err: %v", e)
	}
	return r, fix
}

func TestLoadWalksGoFiles(t *testing.T) {
	r, fix := loadFixture(t)
	files := r.Files()

	want := []string{fix.LoginPath, fix.PaymentPath, fix.UserPath, fix.MultiPath}
	sort.Strings(files)
	sort.Strings(want)
	if len(files) != len(want) {
		t.Fatalf("Files() = %v, want %v", files, want)
	}
	for i, p := range want {
		if files[i] != p {
			t.Errorf("files[%d] = %q, want %q", i, files[i], p)
		}
	}

	// notes.txt and .foo/should_skip.go must be absent.
	for _, p := range files {
		if strings.HasSuffix(p, "notes.txt") {
			t.Errorf("non-Go file indexed: %s", p)
		}
		if strings.Contains(p, ".foo") {
			t.Errorf("hidden file indexed: %s", p)
		}
	}
}

func TestLoadSkipsHiddenDirs(t *testing.T) {
	r, fix := loadFixture(t)
	for _, p := range r.Files() {
		if strings.Contains(p, ".git") {
			t.Errorf("file under .git indexed: %s", p)
		}
		if strings.Contains(p, ".foo") {
			t.Errorf("file under .foo indexed: %s", p)
		}
		if p == fix.HiddenGoPath {
			t.Errorf("hidden Go file indexed: %s", p)
		}
	}
}

func TestLoadInvalidRoot(t *testing.T) {
	if _, _, err := store.Load("/nonexistent-path-please-do-not-create-me"); err == nil {
		t.Fatal("expected error for nonexistent root")
	}
}

func TestLoadNotADirectory(t *testing.T) {
	f := filepath.Join(t.TempDir(), "a_file")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Load(f); err == nil {
		t.Fatal("expected error for non-directory root")
	}
}

func TestSymbolsByFile(t *testing.T) {
	r, fix := loadFixture(t)

	loginSyms := r.Symbols(fix.LoginPath)
	if len(loginSyms) != 2 {
		t.Fatalf("login.go symbols = %d, want 2: %+v", len(loginSyms), loginSyms)
	}
	var hasLogin, hasSession bool
	for _, s := range loginSyms {
		switch s.Name {
		case "Login":
			hasLogin = true
			if s.Kind != "function_declaration" {
				t.Errorf("Login kind = %q", s.Kind)
			}
		case "Session":
			hasSession = true
			if s.Kind != "type_declaration" {
				t.Errorf("Session kind = %q", s.Kind)
			}
		}
	}
	if !hasLogin || !hasSession {
		t.Errorf("missing symbols: hasLogin=%v hasSession=%v", hasLogin, hasSession)
	}

	userSyms := r.Symbols(fix.UserPath)
	var hasMethod bool
	for _, s := range userSyms {
		if s.Kind == "method_declaration" && s.Name == "Greet" {
			hasMethod = true
		}
	}
	if !hasMethod {
		t.Errorf("User.Greet method not in user.go: %+v", userSyms)
	}
}

func TestSymbolsByPathSnapshot(t *testing.T) {
	r, _ := loadFixture(t)
	all := r.SymbolsByPath()
	// Four files: login.go, user.go, multi.go (auth/multi.go declares two
	// same-named methods for disambiguation tests), payments/pay.go.
	if len(all) != 4 {
		t.Errorf("SymbolsByPath size = %d, want 4", len(all))
	}
	for path, syms := range all {
		if len(syms) == 0 {
			t.Errorf("%s: no symbols", path)
		}
	}
}

func TestCachedFileRoundTrip(t *testing.T) {
	r, fix := loadFixture(t)
	f, err := r.CachedFile(fix.LoginPath)
	if err != nil {
		t.Fatalf("CachedFile: %v", err)
	}
	if f.Path != fix.LoginPath {
		t.Errorf("Path = %q, want %q", f.Path, fix.LoginPath)
	}
	if f.Root == nil {
		t.Error("Root should be non-nil")
	}
}

func TestCachedFileNotInRepo(t *testing.T) {
	r, _ := loadFixture(t)
	_, err := r.CachedFile("/totally/unrelated/path.go")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestCachedFileRereadsOnMtimeChange(t *testing.T) {
	r, fix := loadFixture(t)

	// Initial read.
	f1, err := r.CachedFile(fix.LoginPath)
	if err != nil {
		t.Fatalf("CachedFile initial: %v", err)
	}

	// Touch the file to advance its mtime; modify content to make the change observable.
	time.Sleep(2 * time.Millisecond) // ensure the new mtime is strictly greater
	original, err := os.ReadFile(fix.LoginPath)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(original), "func Login", "func LoginV2", 1)
	if err := os.WriteFile(fix.LoginPath, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.WriteFile(fix.LoginPath, original, 0o644)

	f2, err := r.CachedFile(fix.LoginPath)
	if err != nil {
		t.Fatalf("CachedFile post-modify: %v", err)
	}
	if string(f2.Bytes) == string(f1.Bytes) {
		t.Error("CachedFile should have re-read after mtime change")
	}
	if !strings.Contains(string(f2.Bytes), "LoginV2") {
		t.Errorf("re-read content missing LoginV2: %q", f2.Bytes)
	}
}

func TestProvenanceSet(t *testing.T) {
	r, _ := loadFixture(t)
	p := r.Provenance()
	if p.Tool != "tree-sitter" {
		t.Errorf("Tool = %q, want tree-sitter", p.Tool)
	}
	if p.Version == "" {
		t.Error("Version should be set")
	}
	if p.FetchedAt == "" {
		t.Error("FetchedAt should be set")
	}
}

func TestPackagePath(t *testing.T) {
	fix := repofixture.New(t)
	cases := []struct {
		abs  string
		want string
	}{
		{fix.LoginPath, "auth"},
		{fix.PaymentPath, "payments"},
		{fix.Root + "/main.go", ""}, // top-level file → empty
	}
	for _, tc := range cases {
		if got := store.PackagePath(fix.Root, tc.abs); got != tc.want {
			t.Errorf("PackagePath(%q) = %q, want %q", tc.abs, got, tc.want)
		}
	}
}

func TestRoot(t *testing.T) {
	r, fix := loadFixture(t)
	if got := r.Root(); got != fix.Root {
		t.Errorf("Root = %q, want %q", got, fix.Root)
	}
}

// Sanity: Parse-test for symbol rows so we know the index isn't empty.
func TestSymbolsHaveForwardRanges(t *testing.T) {
	r, _ := loadFixture(t)
	for path, syms := range r.SymbolsByPath() {
		for _, s := range syms {
			if s.EndRow <= s.StartRow {
				t.Errorf("%s: %s has non-forward range [%d,%d)", path, s.Name, s.StartRow, s.EndRow)
			}
		}
	}
}

// Sanity: parser.SymbolKind works for the produced symbols (catches a
// missing kind enum value).
func TestSymbolsHaveValidKinds(t *testing.T) {
	r, _ := loadFixture(t)
	for path, syms := range r.SymbolsByPath() {
		for _, s := range syms {
			dk := parser.SymbolKind(s)
			switch dk {
			case "FUNCTION", "METHOD", "CLASS", "MODULE", "REPO", "PACKAGE", "FILE":
				// OK
			default:
				t.Errorf("%s: %s mapped to unexpected kind %q", path, s.Name, dk)
			}
		}
	}
}

func TestDocComments(t *testing.T) {
	r, fix := loadFixture(t)

	// login.go: Login has a doc comment, Session has a doc comment.
	loginSyms := r.Symbols(fix.LoginPath)
	if len(loginSyms) == 0 {
		t.Fatal("login.go has no symbols")
	}
	docs := r.DocComments(fix.LoginPath, loginSyms)
	if docs == nil {
		t.Fatal("DocComments returned nil")
	}
	if got, want := docs["Login"], "Login authenticates a user and returns a session."; got != want {
		t.Errorf("docs[Login] = %q, want %q", got, want)
	}
	if got, want := docs["Session"], "Session holds a user's auth state."; got != want {
		t.Errorf("docs[Session] = %q, want %q", got, want)
	}

	// payments/pay.go: Charge and Refund each have a doc.
	paySyms := r.Symbols(fix.PaymentPath)
	payDocs := r.DocComments(fix.PaymentPath, paySyms)
	if got, want := payDocs["Charge"], "Charge processes a payment."; got != want {
		t.Errorf("payDocs[Charge] = %q, want %q", got, want)
	}
	if got, want := payDocs["Refund"], "Refund reverses a charge."; got != want {
		t.Errorf("payDocs[Refund] = %q, want %q", got, want)
	}
}

func TestDocCommentsUnknownPath(t *testing.T) {
	// An unknown path yields an empty map (not an error). Callers can treat
	// the absence as "no doc available" without a separate branch.
	r, _ := loadFixture(t)
	docs := r.DocComments("/not/a/real/file.go", nil)
	if len(docs) != 0 {
		t.Errorf("docs for unknown path = %+v, want empty", docs)
	}
}

func TestDocCommentsEmptyDecls(t *testing.T) {
	// Empty declaration list still triggers a CachedFile call (no entries to
	// walk); the result is an empty map, not nil.
	r, fix := loadFixture(t)
	docs := r.DocComments(fix.LoginPath, nil)
	if len(docs) != 0 {
		t.Errorf("docs for empty decls = %+v, want empty", docs)
	}
}

func TestDocCommentMatchesSingleCall(t *testing.T) {
	// The bulk helper must agree with the per-symbol helper so search and
	// ad-hoc callers get identical results.
	r, fix := loadFixture(t)
	syms := r.Symbols(fix.LoginPath)
	bulk := r.DocComments(fix.LoginPath, syms)
	for _, s := range syms {
		if s.Name == "" {
			continue
		}
		if got, want := bulk[s.Name], r.DocComment(fix.LoginPath, s); got != want {
			t.Errorf("DocComments[%s] = %q, DocComment = %q", s.Name, got, want)
		}
	}
}
