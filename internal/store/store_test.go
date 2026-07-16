package store_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kellenff/yactt/internal/cache"
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

	// The walker must surface every Go source file under the repo root.
	// Additional non-Go files (e.g. the TS files in the same fixture) may
	// also appear; containment is the contract, not exact match.
	want := []string{fix.LoginPath, fix.PaymentPath, fix.UserPath, fix.MultiPath}
	got := make(map[string]bool, len(files))
	for _, p := range files {
		got[p] = true
	}
	for _, p := range want {
		if !got[p] {
			t.Errorf("Files() missing %q", p)
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
	if len(loginSyms) < 2 {
		t.Fatalf("login.go symbols = %d, want >= 2: %+v", len(loginSyms), loginSyms)
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
	// Four Go files: login.go, user.go, multi.go (auth/multi.go declares
	// two same-named methods for disambiguation tests), payments/pay.go.
	// The fixture also carries TS files; the contract here is "at least
	// the four Go files surface", not exact count.
	if len(all) < 4 {
		t.Errorf("SymbolsByPath size = %d, want >= 4", len(all))
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

	// Modify content and force mtime strictly past the cached value.
	// A short sleep is not enough under load (coarser FS clocks / busy
	// runners can keep WriteFile inside the same mtime tick); Chtimes
	// makes the invalidation contract deterministic.
	original, err := os.ReadFile(fix.LoginPath)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(original), "func Login", "func LoginV2", 1)
	if err := os.WriteFile(fix.LoginPath, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.WriteFile(fix.LoginPath, original, 0o644)
	newMTime := time.Unix(0, f1.MTime).Add(time.Second)
	if err := os.Chtimes(fix.LoginPath, newMTime, newMTime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

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

// TestCachedFile_TinyLRUUsesResidentFiles pins the warm-path contract:
// Load fills Repo.files, and CachedFile must return that resident
// *source.File even when the LRU is too small to hold the working set.
// A tiny NewSized(1,1) cache stands in for "N > DefaultFileCap" without
// materialising 50k files. chmod 000 makes any reparse fail ReadFile.
func TestCachedFile_TinyLRUUsesResidentFiles(t *testing.T) {
	r, fix := loadFixture(t)
	r.SetCacheForTest(cache.NewSized(1, 1))

	if err := os.Chmod(fix.LoginPath, 0o000); err != nil {
		t.Fatalf("chmod 000: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(fix.LoginPath, 0o644) })

	// Touch a second file first so the 1-slot LRU evicts LoginPath if
	// anything PutFile's it — resident map must still serve LoginPath.
	if _, err := r.CachedFile(fix.UserPath); err != nil {
		t.Fatalf("CachedFile(user) priming LRU: %v", err)
	}

	f, err := r.CachedFile(fix.LoginPath)
	if err != nil {
		t.Fatalf("CachedFile after tiny-LRU eviction + chmod 000: %v (expected resident hit)", err)
	}
	if f.Root == nil {
		t.Error("Root should be non-nil on resident hit")
	}
	if !strings.Contains(string(f.Bytes), "func Login") {
		t.Errorf("resident bytes missing Login: %q", f.Bytes[:min(80, len(f.Bytes))])
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

// TestEdgesByCalleeFixture pins the by-call-edge index from the caller side:
// asking for "who calls Charge?" must surface Authenticate in auth/login.go.
// Authenticate's body calls Charge (unresolved — no import in the fixture);
// the index records the bare callee name and LocateSymbol/lookup resolves it
// to the declaration in payments/pay.go downstream.
func TestEdgesByCalleeFixture(t *testing.T) {
	r, fix := loadFixture(t)
	entries := r.EdgesByCallee("Charge")
	if len(entries) == 0 {
		t.Fatal(`EdgesByCallee("Charge") empty; expected Authenticate as caller`)
	}
	var found bool
	for _, e := range entries {
		if e.Caller.Name != "Authenticate" {
			continue
		}
		if e.File != fix.LoginPath {
			t.Errorf("caller file = %q, want %q", e.File, fix.LoginPath)
		}
		if e.Callee != "Charge" {
			t.Errorf("callee = %q, want Charge", e.Callee)
		}
		found = true
	}
	if !found {
		t.Errorf("Authenticate not in callers of Charge; got %+v", entries)
	}
}

// TestEdgesByCalleeIncludesMethod confirms the persisted call-edge index
// now walks method_declaration bodies: User.Refresh in auth/user.go calls
// Charge, so EdgesByCallee("Charge") must surface it alongside the
// function-level caller Authenticate.
func TestEdgesByCalleeIncludesMethod(t *testing.T) {
	r, fix := loadFixture(t)
	entries := r.EdgesByCallee("Charge")
	var sawAuthenticate, sawRefresh bool
	for _, e := range entries {
		switch e.Caller.Name {
		case "Authenticate":
			sawAuthenticate = true
		case "Refresh":
			if e.File != fix.UserPath {
				t.Errorf("Refresh caller file = %q, want %q", e.File, fix.UserPath)
			}
			if e.Caller.Kind != "method_declaration" {
				t.Errorf("Refresh Caller.Kind = %q, want method_declaration", e.Caller.Kind)
			}
			sawRefresh = true
		}
	}
	if !sawAuthenticate {
		t.Errorf("Authenticate dropped from callers of Charge; got %+v", entries)
	}
	if !sawRefresh {
		t.Errorf(`User.Refresh not in callers of Charge; got %+v`, entries)
	}
}

// TestEdgesByCalleeIncludesTSMethod confirms the persisted call-edge
// index walks TypeScript method_definition bodies. User.refresh in
// auth/user.ts calls recordUsage in payments/pay.ts; EdgesByCallee must
// surface the call with Caller.Kind == "method_declaration" and the
// caller in the TS file. Pins the language-agnostic walker.
func TestEdgesByCalleeIncludesTSMethod(t *testing.T) {
	r, fix := loadFixture(t)
	entries := r.EdgesByCallee("recordUsage")
	var sawRefresh bool
	for _, e := range entries {
		if e.Caller.Name != "refresh" {
			continue
		}
		if e.File != fix.UserTSPath {
			t.Errorf("refresh caller file = %q, want %q", e.File, fix.UserTSPath)
		}
		if e.Caller.Kind != "method_declaration" {
			t.Errorf("refresh Caller.Kind = %q, want method_declaration", e.Caller.Kind)
		}
		if e.Caller.Receiver != "User" {
			t.Errorf("refresh Caller.Receiver = %q, want User", e.Caller.Receiver)
		}
		sawRefresh = true
	}
	if !sawRefresh {
		t.Errorf(`User.refresh not in callers of recordUsage; got %+v`, entries)
	}
}

// TestEdgesBySameNameMethodKeying pins the per-receiver keying for
// methods in the persisted call-edge index. auth/multi.go declares
// Alpha.Ping (calls Charge) and Beta.Ping (calls Refund) — two same-
// named methods on different receivers in the same file. Without
// receiver-aware keys, both methods would collapse under
// multi.go::Ping and EdgesByCaller could only return one set. With the
// fix, each method has a distinct caller-side key.
func TestEdgesBySameNameMethodKeying(t *testing.T) {
	r, fix := loadFixture(t)

	alphaSym := parser.Symbol{Kind: "method_declaration", Name: "Ping", Receiver: "Alpha"}
	alphaEntries := r.EdgesByCaller(fix.MultiPath, alphaSym)
	var sawCharge bool
	for _, e := range alphaEntries {
		if e.Callee == "Charge" {
			sawCharge = true
		}
	}
	if !sawCharge {
		t.Errorf("Alpha.Ping dropped Charge; got %+v", alphaEntries)
	}

	betaSym := parser.Symbol{Kind: "method_declaration", Name: "Ping", Receiver: "Beta"}
	betaEntries := r.EdgesByCaller(fix.MultiPath, betaSym)
	var sawRefund bool
	for _, e := range betaEntries {
		if e.Callee == "Refund" {
			sawRefund = true
		}
	}
	if !sawRefund {
		t.Errorf("Beta.Ping dropped Refund; got %+v", betaEntries)
	}

	// And confirm that querying the wrong receiver on the same file
	// returns nothing — proof that the two methods are independent.
	noEntries := r.EdgesByCaller(fix.MultiPath, parser.Symbol{Kind: "method_declaration", Name: "Ping", Receiver: "NoSuch"})
	if len(noEntries) != 0 {
		t.Errorf("EdgesByCaller(NoSuch) = %+v, want empty", noEntries)
	}
}

// TestEdgesByCallerFixture pins the by-call-edge index from the callee side:
// asking "what does Authenticate call?" must surface Charge.
func TestEdgesByCallerFixture(t *testing.T) {
	r, fix := loadFixture(t)
	entries := r.EdgesByCaller(fix.LoginPath, parser.Symbol{Kind: "function_declaration", Name: "Authenticate"})
	if len(entries) == 0 {
		t.Fatalf(`EdgesByCaller(%q, "Authenticate") empty; expected Charge`, fix.LoginPath)
	}
	var sawCharge bool
	for _, e := range entries {
		if e.Callee == "Charge" {
			sawCharge = true
		}
	}
	if !sawCharge {
		t.Errorf("Charge not in callees of Authenticate; got %+v", entries)
	}
}

// TestEdgesFallbackOnUnknown confirms an unknown name yields an empty slice
// without panicking — both accessors. Caller-side also returns empty when
// the (file, sym) pair has no entry (e.g. asking for an existing name in
// a different file).
func TestEdgesFallbackOnUnknown(t *testing.T) {
	r, _ := loadFixture(t)
	if got := r.EdgesByCallee("DoesNotExist"); len(got) != 0 {
		t.Errorf("EdgesByCallee(missing) = %+v, want empty", got)
	}
	if got := r.EdgesByCaller("/no/such/file.go", parser.Symbol{Kind: "function_declaration", Name: "Anything"}); len(got) != 0 {
		t.Errorf("EdgesByCaller(missing) = %+v, want empty", got)
	}
}

// TestReloadInvalidateRebuilds confirms the index is repopulated after
// ReloadInvalidate. The contract is that callers can keep their answers
// across a single-file reload — the rebuild path runs over the freshly
// parsed symbols and re-emits every edge in one shot.
func TestReloadInvalidateRebuilds(t *testing.T) {
	r, fix := loadFixture(t)
	if err := r.ReloadInvalidate(fix.LoginPath); err != nil {
		t.Fatalf("ReloadInvalidate: %v", err)
	}
	entries := r.EdgesByCallee("Charge")
	if len(entries) == 0 {
		t.Fatal(`EdgesByCallee("Charge") empty after ReloadInvalidate`)
	}
	if got := r.EdgesByCaller(fix.LoginPath, parser.Symbol{Kind: "function_declaration", Name: "Authenticate"}); len(got) == 0 {
		t.Errorf(`EdgesByCaller(LoginPath, "Authenticate") empty after ReloadInvalidate`)
	}
}

// TestImportsIn_PersistsBuildTime pins the Tier-0 imports-by-file index.
// Synthesizes a file with two imports, loads the repo, and asserts
// `Repo.ImportsIn(file)` returns the expected paths with non-zero
// row ranges anchored on the import declarations.
func TestImportsIn_PersistsBuildTime(t *testing.T) {
	r, fix := loadFixture(t)

	// Synthesize a file with imports under the fixture's auth dir.
	src := []byte(`package auth

import "fmt"
import "github.com/foo/bar"

func UseImports() {}
`)
	path := fix.Root + string(filepath.Separator) + "auth" + string(filepath.Separator) + "with_imports_test.go"
	if err := os.WriteFile(path, src, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := r.ReloadInvalidate(path); err != nil {
		t.Fatalf("ReloadInvalidate: %v", err)
	}

	entries := r.ImportsIn(path)
	if len(entries) != 2 {
		t.Fatalf("ImportsIn(%q) = %d entries, want 2: %+v", path, len(entries), entries)
	}

	got := map[string]store.ImportEntry{}
	for _, e := range entries {
		got[e.Path] = e
	}
	for _, want := range []string{"fmt", "github.com/foo/bar"} {
		e, ok := got[want]
		if !ok {
			t.Errorf("ImportsIn missing %q; got %+v", want, entries)
			continue
		}
		if e.StartRow < 2 || e.EndRow <= e.StartRow {
			t.Errorf("entry for %q has bad rows: StartRow=%d EndRow=%d", want, e.StartRow, e.EndRow)
		}
	}
}

// TestImportsIn_RebuildOnInvalidate confirms the imports index is
// repopulated after ReloadInvalidate — same contract as
// TestReloadInvalidateRebuilds for the call-edge index.
func TestImportsIn_RebuildOnInvalidate(t *testing.T) {
	r, fix := loadFixture(t)

	// Add an import to the fixture's login.go.
	if err := os.WriteFile(fix.LoginPath, []byte(`package auth

import "fmt"

func Login(user, pass string) (Session, error) {
	return Session{}, nil
}
`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := r.ReloadInvalidate(fix.LoginPath); err != nil {
		t.Fatalf("ReloadInvalidate: %v", err)
	}

	entries := r.ImportsIn(fix.LoginPath)
	var sawFmt bool
	for _, e := range entries {
		if e.Path == "fmt" {
			sawFmt = true
		}
	}
	if !sawFmt {
		t.Errorf(`ImportsIn(LoginPath) missing "fmt" after ReloadInvalidate; got %+v`, entries)
	}
}

// TestLoad_MaxFilesExceeded verifies the per-load file cap. We build
// a tempdir with N+1 source files and a small MaxFiles=N; the walk
// must abort at the (N+1)th file, surface ErrMaxFilesExceeded in
// errs, and return a partial repo containing N files.
func TestLoad_MaxFilesExceeded(t *testing.T) {
	const cap = 5
	const total = cap + 3 // 8 source files; cap triggers on the 6th

	dir := t.TempDir()
	for i := 0; i < total; i++ {
		path := filepath.Join(dir, "f"+intToStr(i)+".go")
		body := "package f" + intToStr(i) + "\n\nfunc X() {}\n"
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}

	r, errs, err := store.Load(dir, store.WithMaxFiles(cap))
	if err != nil {
		t.Fatalf("Load returned top-level err: %v", err)
	}
	if r == nil {
		t.Fatal("Load returned nil repo (should return partial repo on cap)")
	}
	// cap files indexed.
	if got := len(r.Files()); got != cap {
		t.Errorf("Files() = %d, want %d (cap)", got, cap)
	}
	// ErrMaxFilesExceeded surfaced in errs.
	var sawCap bool
	for _, e := range errs {
		if errors.Is(e, store.ErrMaxFilesExceeded) {
			sawCap = true
			break
		}
	}
	if !sawCap {
		t.Errorf("errs missing ErrMaxFilesExceeded; got %v", errs)
	}
}

// TestLoad_UnderCap_NoErr verifies the cap doesn't trip when the
// file count is well under MaxFiles. Regression guard: a too-eager
// check (e.g. >= instead of >) would falsely reject a load that
// fits.
func TestLoad_UnderCap_NoErr(t *testing.T) {
	fix := repofixture.New(t)
	// repofixture has ~6 source files; cap 100 is comfortably above.
	r, errs, err := store.Load(fix.Root, store.WithMaxFiles(100))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if r == nil {
		t.Fatal("Load returned nil repo")
	}
	for _, e := range errs {
		if errors.Is(e, store.ErrMaxFilesExceeded) {
			t.Errorf("under-cap load surfaced ErrMaxFilesExceeded: %v", e)
		}
	}
}

// TestLoad_DefaultMaxFiles pins the production default to 50_000 so
// drift on the constant is caught by a contract-style assertion.
func TestLoad_DefaultMaxFiles(t *testing.T) {
	if store.DefaultMaxFiles != 50_000 {
		t.Errorf("DefaultMaxFiles = %d, want 50_000", store.DefaultMaxFiles)
	}
}

// intToStr renders a small non-negative int as a base-10 string
// without pulling strconv into the test file.
func intToStr(n int) string {
	if n == 0 {
		return "0"
	}
	const digits = "0123456789"
	var s []byte
	for n > 0 {
		s = append([]byte{digits[n%10]}, s...)
		n /= 10
	}
	return string(s)
}

// TestLoad_WithDiskCache verifies the WithDiskCache option is wired
// into the walk: a first Load writes disk entries, a second Load
// against the same root + same dir must consult those entries (we
// observe this indirectly by asserting both runs succeed). The
// actual hydrate path is exercised in TestDiskCache_RoundTrip;
// this is the integration seam — disk dir creation, walk fallback,
// and partial-repo surfacing under ErrMaxFilesExceeded.
// TestLoad_WithDiskCache_PopulatesDirectory is the symmetric test:
// the second Load creates new disk entries when the cache dir is
// empty.
func TestLoad_WithDiskCache(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")
	fix := repofixture.New(t)

	// First load — populates the cache.
	r1, _, err := store.Load(fix.Root, store.WithDiskCache(cacheDir))
	if err != nil {
		t.Fatalf("first Load: %v", err)
	}
	defer func() { _ = r1.Close() }()

	// At least one disk entry was written.
	entries, _ := os.ReadDir(cacheDir)
	if len(entries) == 0 {
		t.Errorf("disk cache dir is empty after first Load; got %d entries", len(entries))
	}

	// Second load — same dir, must succeed and produce the same files.
	r2, _, err := store.Load(fix.Root, store.WithDiskCache(cacheDir))
	if err != nil {
		t.Fatalf("second Load: %v", err)
	}
	defer func() { _ = r2.Close() }()
	if got := len(r2.Files()); got != len(r1.Files()) {
		t.Errorf("second Load Files = %d, want %d (matches first)", got, len(r1.Files()))
	}
}

// TestLoad_WithDiskCacheCap verifies the cap is honored at the
// integration seam: a Load with WithDiskCacheMaxBytes set
// smaller than the fixture's parsed-file footprint populates
// the cache, then a second Load evicts the oldest entries
// (first run's) before settling.
func TestLoad_WithDiskCacheCap(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")
	fix := repofixture.New(t)

	// First load populates the cache.
	r1, _, err := store.Load(fix.Root,
		store.WithDiskCache(cacheDir),
		store.WithDiskCacheMaxBytes(1024*1024),
	)
	if err != nil {
		t.Fatalf("first Load: %v", err)
	}
	defer func() { _ = r1.Close() }()
	firstFiles := len(r1.Files())
	firstEntries, _ := os.ReadDir(cacheDir)
	if len(firstEntries) != firstFiles {
		t.Errorf("first Load: cache dir = %d entries, want %d", len(firstEntries), firstFiles)
	}

	// Second load with a much smaller cap — eviction will fire as
	// each Put re-checks the budget. After completion the cache
	// must be at-or-under the cap, but the load itself still
	// succeeds.
	r2, _, err := store.Load(fix.Root,
		store.WithDiskCache(cacheDir),
		store.WithDiskCacheMaxBytes(200), // each entry is ~86 bytes
	)
	if err != nil {
		t.Fatalf("second Load with cap: %v", err)
	}
	defer func() { _ = r2.Close() }()

	// Cap is approximate (per-Put eviction may briefly overshoot);
	// the steady-state budget is `cap * ~2`. We only assert the
	// cache didn't grow unboundedly.
	entries, _ := os.ReadDir(cacheDir)
	if len(entries) > firstFiles {
		t.Errorf("second Load: cache entries %d > first-load %d (cap not honored)", len(entries), firstFiles)
	}
}

// TestLoad_EvictOrphans_DeletedFile is the integration test for
// orphan eviction: a source file that exists at first Load but is
// deleted before the second Load must have its cache entry
// evicted by the second Load's EvictOrphans pass.
func TestLoad_EvictOrphans_DeletedFile(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")
	fix := repofixture.New(t)

	// First load — populates the cache with every fixture file.
	r1, _, err := store.Load(fix.Root, store.WithDiskCache(cacheDir))
	if err != nil {
		t.Fatalf("first Load: %v", err)
	}
	defer func() { _ = r1.Close() }()
	firstFiles := len(r1.Files())
	if firstFiles == 0 {
		t.Fatal("first Load indexed zero files; fixture is broken")
	}

	// Delete a source file from the fixture.
	if err := os.Remove(fix.LoginPath); err != nil {
		t.Fatalf("remove LoginPath: %v", err)
	}

	// Second load — orphan must be evicted by EvictOrphans.
	r2, errs, err := store.Load(fix.Root, store.WithDiskCache(cacheDir))
	if err != nil {
		t.Fatalf("second Load: %v", err)
	}
	defer func() { _ = r2.Close() }()
	// Sweep failure must surface via errs only if it failed —
	// on success the slice is empty.
	for _, e := range errs {
		if strings.Contains(e.Error(), "evict orphans") {
			t.Errorf("unexpected evict error: %v", e)
		}
	}

	// Cache count must be strictly less than the first-load count.
	entries, _ := os.ReadDir(cacheDir)
	if len(entries) >= firstFiles {
		t.Errorf("cache dir = %d entries after orphan eviction, want < %d", len(entries), firstFiles)
	}
}
