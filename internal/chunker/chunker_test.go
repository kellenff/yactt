package chunker_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/kellenff/yactt/internal/chunker"
	"github.com/kellenff/yactt/internal/parser"
	"github.com/kellenff/yactt/internal/store"
	"github.com/kellenff/yactt/internal/store/repofixture"
)

// loadChunkerFixture returns a loaded repo + the fixture paths the
// tests assert against. The chunker tests use the same fixture the
// store tests use so the symbol/edge plumbing is exercised end-to-end.
func loadChunkerFixture(t *testing.T) (*store.Repo, *repofixture.Fixture) {
	t.Helper()
	fix := repofixture.New(t)
	r, errs, err := store.Load(fix.Root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, e := range errs {
		t.Errorf("Load per-file err: %v", e)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r, fix
}

// runChunker runs the chunker with default options and decodes the
// NDJSON output. Returns the chunks in source order. Empty list (not
// nil) when nothing was emitted.
func runChunker(t *testing.T, r *store.Repo, opts chunker.Options) []chunker.Chunk {
	t.Helper()
	var buf bytes.Buffer
	n, err := chunker.Run(context.Background(), r, opts, &buf)
	if err != nil {
		t.Fatalf("Run: %v (after %d chunks)", err, n)
	}
	dec := json.NewDecoder(&buf)
	var out []chunker.Chunk
	for dec.More() {
		var c chunker.Chunk
		if err := dec.Decode(&c); err != nil {
			t.Fatalf("decode: %v", err)
		}
		out = append(out, c)
	}
	return out
}

// findChunk returns the first chunk whose ID equals the given value,
// failing the test if none match. Used when the test pins one specific
// emission.
func findChunk(t *testing.T, chunks []chunker.Chunk, id string) chunker.Chunk {
	t.Helper()
	for _, c := range chunks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no chunk with id %q (have %d)", id, len(chunks))
	return chunker.Chunk{}
}

// TestRun_FunctionPolicy_EmitsOneChunkPerCodeSymbol is the headline
// contract. The fixture has 6 Go functions (Login, Authenticate,
// Greet, Refresh, Charge, Refund) + 2 Go methods (User.Greet, User.Refresh).
// Note User.Greet is also counted as a method_declaration; the test
// counts by chunk emission, not symbol kind.
func TestRun_FunctionPolicy_EmitsOneChunkPerCodeSymbol(t *testing.T) {
	r, _ := loadChunkerFixture(t)
	chunks := runChunker(t, r, chunker.Options{Policy: chunker.PolicyFunction})

	// Filter to the auth/payments packages (exclude fixture TS files
	// for this assertion).
	var goChunks []chunker.Chunk
	for _, c := range chunks {
		if c.Language == "go" {
			goChunks = append(goChunks, c)
		}
	}
	// The fixture has: Login, Authenticate (auth/login.go), User.Greet,
	// User.Refresh (auth/user.go), Alpha.Ping, Beta.Ping (auth/multi.go),
	// Charge, Refund (payments/pay.go). That's 8 code symbols.
	if len(goChunks) != 8 {
		names := make([]string, 0, len(goChunks))
		for _, c := range goChunks {
			names = append(names, c.QualifiedName)
		}
		sort.Strings(names)
		t.Errorf("Go chunks = %d, want 8; got %v", len(goChunks), names)
	}
}

// TestRun_MethodHasReceiverInID pins the canonical-id contract for
// methods. meth:auth.User.Greet must be the wire id, not fn:auth.Greet.
func TestRun_MethodHasReceiverInID(t *testing.T) {
	r, _ := loadChunkerFixture(t)
	chunks := runChunker(t, r, chunker.Options{Policy: chunker.PolicyFunction})

	c := findChunk(t, chunks, "meth:auth.User.Greet")
	if c.Kind != "METHOD" {
		t.Errorf("Kind = %q, want METHOD", c.Kind)
	}
	if c.QualifiedName != "auth.User.Greet" {
		t.Errorf("QualifiedName = %q, want auth.User.Greet", c.QualifiedName)
	}
}

// TestRun_CallersCalleesPopulatedForCrossPackageCall pins the call-
// edge wiring. The fixture's auth/login.go has Authenticate calling
// payments.Charge. The chunk for fn:auth.Authenticate must list
// fn:payments.Charge in its callees.
func TestRun_CallersCalleesPopulatedForCrossPackageCall(t *testing.T) {
	r, _ := loadChunkerFixture(t)
	chunks := runChunker(t, r, chunker.Options{Policy: chunker.PolicyFunction})

	c := findChunk(t, chunks, "fn:auth.Authenticate")
	hasCharge := false
	for _, callee := range c.Callees {
		if callee == "fn:payments.Charge" {
			hasCharge = true
			break
		}
	}
	if !hasCharge {
		t.Errorf("fn:auth.Authenticate.Callees = %v, want fn:payments.Charge present", c.Callees)
	}
}

// TestRun_ClassPolicy_ListsMethodSignatures pins the class chunk's
// methods-listing behavior. The auth.User class's chunk must include
// Greet and Refresh signatures in its Text field.
func TestRun_ClassPolicy_ListsMethodSignatures(t *testing.T) {
	r, _ := loadChunkerFixture(t)
	chunks := runChunker(t, r, chunker.Options{Policy: chunker.PolicyClass})

	c := findChunk(t, chunks, "class:auth.User")
	if !strings.Contains(c.Text, "Methods:") {
		t.Errorf("class chunk text missing Methods: section; got:\n%s", c.Text)
	}
	if !strings.Contains(c.Text, "Greet") {
		t.Errorf("class chunk text missing Greet signature; got:\n%s", c.Text)
	}
	if !strings.Contains(c.Text, "Refresh") {
		t.Errorf("class chunk text missing Refresh signature; got:\n%s", c.Text)
	}
	// Class-level callees are the union over the methods'.
	hasCharge := false
	for _, callee := range c.Callees {
		if callee == "fn:payments.Charge" {
			hasCharge = true
			break
		}
	}
	if !hasCharge {
		t.Errorf("class:auth.User.Callees = %v, want fn:payments.Charge present (via User.Refresh)", c.Callees)
	}
}

// TestRun_ModulePolicy_EmitsOneChunkPerFile pins the file-level
// chunk count for the Go portion of the fixture.
func TestRun_ModulePolicy_EmitsOneChunkPerFile(t *testing.T) {
	r, _ := loadChunkerFixture(t)
	chunks := runChunker(t, r, chunker.Options{Policy: chunker.PolicyModule})

	// Filter to Go; the fixture has 4 Go source files (login.go,
	// user.go, multi.go, pay.go) but module policy emits one chunk
	// per file regardless of declarations.
	var goChunks []chunker.Chunk
	for _, c := range chunks {
		if c.Language == "go" {
			goChunks = append(goChunks, c)
		}
	}
	if len(goChunks) != 4 {
		t.Errorf("Go module chunks = %d, want 4", len(goChunks))
	}
	for _, c := range goChunks {
		if c.Kind != "MODULE" {
			t.Errorf("module chunk Kind = %q, want MODULE", c.Kind)
		}
	}
}

// TestRun_OutputIsDeterministic pins the "byte-identical on repeat"
// contract. Two runs against the same repo + options must produce
// the same NDJSON bytes.
func TestRun_OutputIsDeterministic(t *testing.T) {
	r, _ := loadChunkerFixture(t)
	opts := chunker.Options{Policy: chunker.PolicyFunction}

	var a, b bytes.Buffer
	if _, err := chunker.Run(context.Background(), r, opts, &a); err != nil {
		t.Fatalf("Run 1: %v", err)
	}
	if _, err := chunker.Run(context.Background(), r, opts, &b); err != nil {
		t.Fatalf("Run 2: %v", err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Errorf("non-deterministic output:\nfirst:\n%s\nsecond:\n%s", a.String(), b.String())
	}
}

// TestRun_RespectsWithTests confirms that test files are excluded by
// default and included when WithTests is true. The fixture itself
// has no _test.go files; we add one and assert the difference.
func TestRun_RespectsWithTests(t *testing.T) {
	r, fix := loadChunkerFixture(t)
	// Drop a test file into the fixture.
	testPath := filepath.Join(fix.Root, "auth", "login_test.go")
	if err := writeFile(t, testPath, "package auth\n\nfunc TestLogin(t *testing.T) {}\n"); err != nil {
		t.Fatalf("write test file: %v", err)
	}
	if err := r.ReloadInvalidate(testPath); err != nil {
		t.Fatalf("ReloadInvalidate: %v", err)
	}

	defaultChunks := runChunker(t, r, chunker.Options{Policy: chunker.PolicyFunction})
	testChunks := runChunker(t, r, chunker.Options{Policy: chunker.PolicyFunction, WithTests: true})

	hasTest := func(cs []chunker.Chunk) bool {
		for _, c := range cs {
			if strings.HasSuffix(c.File, "login_test.go") {
				return true
			}
		}
		return false
	}
	if hasTest(defaultChunks) {
		t.Errorf("test file present in default-options chunks (should be excluded)")
	}
	if !hasTest(testChunks) {
		t.Errorf("test file missing from WithTests=true chunks (should be included)")
	}
}

// TestRun_RespectsIncludeExclude confirms glob filtering.
func TestRun_RespectsIncludeExclude(t *testing.T) {
	r, _ := loadChunkerFixture(t)
	opts := chunker.Options{
		Policy:  chunker.PolicyFunction,
		Include: []string{"**/login.go"},
	}
	chunks := runChunker(t, r, opts)
	for _, c := range chunks {
		if !strings.HasSuffix(c.File, "login.go") {
			t.Errorf("Include filter let %s through", c.File)
		}
	}
	if len(chunks) == 0 {
		t.Errorf("Include filter excluded everything; expected at least 1 chunk from login.go")
	}
}

// TestRun_RespectsLanguageFilter pins per-language chunking. The
// fixture has both Go and TypeScript files; filtering to Go only
// should drop the TS chunks.
func TestRun_RespectsLanguageFilter(t *testing.T) {
	r, _ := loadChunkerFixture(t)
	chunks := runChunker(t, r, chunker.Options{
		Policy:    chunker.PolicyFunction,
		Languages: []parser.Name{parser.LangGo},
	})
	for _, c := range chunks {
		if c.Language != "go" {
			t.Errorf("Languages=[go] filter let %s through", c.File)
		}
	}
}

// TestChunk_RoundTripJSON pins the wire shape: every field present in
// Chunk must survive Marshal -> Unmarshal without loss.
func TestChunk_RoundTripJSON(t *testing.T) {
	r, _ := loadChunkerFixture(t)
	chunks := runChunker(t, r, chunker.Options{Policy: chunker.PolicyFunction})
	if len(chunks) == 0 {
		t.Fatal("no chunks emitted")
	}
	for i, c := range chunks {
		b, err := json.Marshal(c)
		if err != nil {
			t.Fatalf("chunk %d marshal: %v", i, err)
		}
		var got chunker.Chunk
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("chunk %d unmarshal: %v", i, err)
		}
		if got.ID != c.ID {
			t.Errorf("chunk %d ID drift: %q -> %q", i, c.ID, got.ID)
		}
		if got.Text != c.Text {
			t.Errorf("chunk %d Text drift (len %d -> %d)", i, len(c.Text), len(got.Text))
		}
		if !equalStringSlices(got.Callees, c.Callees) {
			t.Errorf("chunk %d Callees drift: %v -> %v", i, c.Callees, got.Callees)
		}
	}
}

// TestRun_NilInputs pins the error contract for invalid args. Both
// nil-repo and nil-writer must return an error, not panic.
func TestRun_NilInputs(t *testing.T) {
	if _, err := chunker.Run(context.Background(), nil, chunker.Options{}, &bytes.Buffer{}); err == nil {
		t.Error("Run(nil repo) returned no error")
	}
	r, _ := loadChunkerFixture(t)
	if _, err := chunker.Run(context.Background(), r, chunker.Options{}, nil); err == nil {
		t.Error("Run(nil writer) returned no error")
	}
}

// TestRun_DefaultPolicyIsFunction confirms the zero-value Options
// uses PolicyFunction. Without an explicit Policy set, the chunk
// shape must match what PolicyFunction emits.
func TestRun_DefaultPolicyIsFunction(t *testing.T) {
	r, _ := loadChunkerFixture(t)
	defaultChunks := runChunker(t, r, chunker.Options{})
	functionChunks := runChunker(t, r, chunker.Options{Policy: chunker.PolicyFunction})
	if len(defaultChunks) != len(functionChunks) {
		t.Errorf("default vs function-policy chunk count: %d vs %d", len(defaultChunks), len(functionChunks))
	}
	for i := range defaultChunks {
		if defaultChunks[i].ID != functionChunks[i].ID {
			t.Errorf("chunk %d: default id %q != function id %q", i, defaultChunks[i].ID, functionChunks[i].ID)
		}
		if defaultChunks[i].Policy != chunker.PolicyFunction {
			t.Errorf("chunk %d: default policy = %q, want function", i, defaultChunks[i].Policy)
		}
	}
}

// TestRun_SignatureIncludesDocComment pins the doc-comment-in-signature
// behavior. Login has a doc comment "Login authenticates a user…"
// which must appear in the signature field.
func TestRun_SignatureIncludesDocComment(t *testing.T) {
	r, _ := loadChunkerFixture(t)
	chunks := runChunker(t, r, chunker.Options{Policy: chunker.PolicyFunction})

	c := findChunk(t, chunks, "fn:auth.Login")
	if !strings.Contains(c.Signature, "Login authenticates a user") {
		t.Errorf("Signature missing doc comment; got %q", c.Signature)
	}
	if !strings.Contains(c.Signature, "func Login") {
		t.Errorf("Signature missing decl line; got %q", c.Signature)
	}
}

// writeFile is a small helper for the WithTests test. The fixture
// only writes Go sources; this lets us drop a test file in.
func writeFile(t *testing.T, path, content string) error {
	t.Helper()
	return os.WriteFile(path, []byte(content), 0o644)
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
