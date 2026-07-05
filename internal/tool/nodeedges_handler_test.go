package tool

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/kellenff/yactt/internal/parser"
	"github.com/kellenff/yactt/internal/store"
	"github.com/kellenff/yactt/internal/store/repofixture"
)

// TestNodeEdges_DefaultsLimitAndKinds exercises the two LIVED-CONDITIONALS
// guards at lines 76 (`a.Limit <= 0` → defaults to 50) and 80
// (`len(kinds) == 0` → defaults to callers/callees/tests). We patch
// login.go so it actually references Charge, then call NodeEdges with
// a zero Limit and empty Kinds. The fixture's Repo's tree-sitter scan
// is the only resolution path (gopls detached for determinism).
func TestNodeEdges_DefaultsLimitAndKinds(t *testing.T) {
	fx := repofixture.New(t)
	// Patch login.go so Login's body references Charge. repofixture's
	// default Login doesn't mention Charge; without this, the scan
	// finds zero callees regardless of limit.
	loginSrc := []byte(`package auth

import "github.com/example/sample/payments"

// Login authenticates a user and returns a session.
func Login(user, pass string) (Session, error) {
	_ = payments.Charge(0)
	return Session{}, nil
}

// Session holds a user's auth state.
type Session struct {
	User string
	Tok  string
}
`)
	if err := os.WriteFile(fx.LoginPath, loginSrc, 0o644); err != nil {
		t.Fatalf("rewrite login.go: %v", err)
	}
	r, _, err := store.Load(fx.Root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = r.Close() }()
	r.DetachLSPForTest() // force the tree-sitter path; tier-1 isn't under test

	// Empty Limit + empty Kinds: defaults must kick in. We expect at
	// least one callee edge (payments.Charge from Login).
	out, err := NodeEdges(r)(context.Background(), json.RawMessage(
		`{"id":"fn:auth.Login"}`,
	))
	if err != nil {
		t.Fatalf("NodeEdges: %v", err)
	}
	edges, ok := out.([]NodeEdgesResult)
	if !ok {
		t.Fatalf("NodeEdges return type: got %T", out)
	}
	// With defaults applied, the tree-sitter pass should find the
	// payments.Charge call inside Login and emit a CALLEES edge.
	foundCallee := false
	for _, e := range edges {
		if e.EdgeKind == "CALLEES" && e.TargetID == "fn:payments.Charge" {
			foundCallee = true
		}
	}
	if !foundCallee {
		t.Errorf("expected a CALLEES edge for fn:payments.Charge under defaults; got %d edges", len(edges))
	}
}

// TestNodeEdges_RejectsEmptyID covers the `a.ID == ""` guard at line 65.
// Empty id must return an error before the rest of the handler runs.
func TestNodeEdges_RejectsEmptyID(t *testing.T) {
	fx := repofixture.New(t)
	r, _, err := store.Load(fx.Root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = r.Close() }()

	out, err := NodeEdges(r)(context.Background(), json.RawMessage(
		`{"id":""}`,
	))
	if err == nil {
		t.Fatalf("expected error for empty id, got nil; out=%v", out)
	}
}

// TestNodeEdges_RejectsMalformedID covers the id.Parse error path
// (line 69-71). A non-conforming id like "notanid" must return an
// error from id.Parse.
func TestNodeEdges_RejectsMalformedID(t *testing.T) {
	fx := repofixture.New(t)
	r, _, err := store.Load(fx.Root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = r.Close() }()

	out, err := NodeEdges(r)(context.Background(), json.RawMessage(
		`{"id":"notanid"}`,
	))
	if err == nil {
		t.Fatalf("expected error for malformed id, got nil; out=%v", out)
	}
}

// TestNodeEdges_UnknownID covers the lookup-miss path at line 73-75.
// A well-formed id that doesn't match any symbol returns an error.
func TestNodeEdges_UnknownID(t *testing.T) {
	fx := repofixture.New(t)
	r, _, err := store.Load(fx.Root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = r.Close() }()

	out, err := NodeEdges(r)(context.Background(), json.RawMessage(
		`{"id":"fn:auth.NoSuchFunc"}`,
	))
	if err == nil {
		t.Fatalf("expected error for unknown id, got nil; out=%v", out)
	}
}

// TestCallerIDAt_NegativeLine covers the `if line < 0` guard at line 293.
// Negative line is invalid and returns ("", false) immediately.
func TestCallerIDAt_NegativeLine(t *testing.T) {
	fx := repofixture.New(t)
	r, _, err := store.Load(fx.Root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = r.Close() }()

	id, ok := callerIDAt(fx.LoginPath, -1, 0, r)
	if ok || id != "" {
		t.Errorf("negative line: got (%q, %v), want (\"\", false)", id, ok)
	}
}

// TestCallerIDAt_LineOutsideAnyFunction returns ("", false) when the
// line falls between two function declarations.
func TestCallerIDAt_LineOutsideAnyFunction(t *testing.T) {
	fx := repofixture.New(t)
	r, _, err := store.Load(fx.Root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = r.Close() }()

	// Line 1 is the "package auth" declaration — outside any function.
	id, ok := callerIDAt(fx.LoginPath, 1, 0, r)
	if ok || id != "" {
		t.Errorf("line outside any fn: got (%q, %v), want (\"\", false)", id, ok)
	}
}

// TestCallerIDAt_InsideFunction returns the fn: id when the line falls
// inside a function declaration (function_declaration branch).
func TestCallerIDAt_InsideFunction(t *testing.T) {
	fx := repofixture.New(t)
	r, _, err := store.Load(fx.Root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = r.Close() }()

	// Login spans rows 4-6 in the patched login.go. Line 5 is inside it.
	id, ok := callerIDAt(fx.LoginPath, 5, 0, r)
	if !ok {
		t.Fatalf("inside Login: ok=false")
	}
	if id != "fn:auth.Login" {
		t.Errorf("inside Login: id=%q, want fn:auth.Login", id)
	}
}

// TestCallerIDAt_InsideMethod returns the meth: id when the line is
// inside a method_declaration (method_declaration branch with non-empty
// Receiver).
func TestCallerIDAt_InsideMethod(t *testing.T) {
	fx := repofixture.New(t)
	r, _, err := store.Load(fx.Root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = r.Close() }()

	// User.Greet is a method_declaration on User. Find its row range.
	var greet parser.Symbol
	for _, s := range r.Symbols(fx.UserPath) {
		if s.Name == "Greet" && s.Kind == "method_declaration" {
			greet = s
			break
		}
	}
	if greet.Name == "" {
		t.Fatal("Greet method not found in fixture")
	}
	id, ok := callerIDAt(fx.UserPath, greet.StartRow+1, 0, r)
	if !ok {
		t.Fatalf("inside Greet: ok=false")
	}
	if id != "meth:auth.User.Greet" {
		t.Errorf("inside Greet: id=%q, want meth:auth.User.Greet", id)
	}
}

// TestScanTests_ReturnsTestSymbols covers the `_test.go` discovery branch
// in scanTests. The fixture's regular .go files don't end in _test.go,
// so we create one synthetically and verify it's picked up.
func TestScanTests_ReturnsTestSymbols(t *testing.T) {
	fx := repofixture.New(t)

	// Synthesize a test file. Use a different package to avoid naming
	// conflicts with the fixture.
	testSrc := []byte(`package auth

import "testing"

func TestSmoke(t *testing.T) {
	_ = t
}
`)
	testPath := fx.Root + string(os.PathSeparator) + "auth" + string(os.PathSeparator) + "smoke_test.go"
	if err := os.WriteFile(testPath, testSrc, 0o644); err != nil {
		t.Fatalf("write smoke_test.go: %v", err)
	}

	r, _, err := store.Load(fx.Root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = r.Close() }()

	// Drive NodeEdges with kinds=["tests"] on fn:auth.Login. The test
	// file declares TestSmoke; scanTests must surface it as a TESTS
	// edge for the symbol we're asking about.
	out, err := NodeEdges(r)(context.Background(), json.RawMessage(
		`{"id":"fn:auth.Login","kinds":["tests"],"limit":50}`,
	))
	if err != nil {
		t.Fatalf("NodeEdges: %v", err)
	}
	edges, ok := out.([]NodeEdgesResult)
	if !ok {
		t.Fatalf("NodeEdges return type: got %T", out)
	}
	// We expect at least one TESTS edge. The fixture declares
	// TestSmoke in smoke_test.go; scanTests walks every file and
	// surfaces functions in *_test.go files.
	foundTest := false
	for _, e := range edges {
		if e.EdgeKind == "TESTS" {
			foundTest = true
			break
		}
	}
	if !foundTest {
		t.Errorf("expected at least one TESTS edge, got %d edges", len(edges))
	}
}

// TestNodeEdges_RespectsLimit asserts the per-kind limit cap is
// applied. The fixture has multiple functions; ask for limit=1 and
// verify we get at most one edge per kind.
func TestNodeEdges_RespectsLimit(t *testing.T) {
	fx := repofixture.New(t)
	r, _, err := store.Load(fx.Root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = r.Close() }()
	r.DetachLSPForTest()

	// Ask for everything with limit=1. We expect at most 1 edge.
	out, err := NodeEdges(r)(context.Background(), json.RawMessage(
		`{"id":"fn:auth.Login","limit":1}`,
	))
	if err != nil {
		t.Fatalf("NodeEdges: %v", err)
	}
	edges, ok := out.([]NodeEdgesResult)
	if !ok {
		t.Fatalf("NodeEdges return type: got %T", out)
	}
	// Note: limit applies per kind; with 3 kinds active and limit=1,
	// the total cap is 3. We don't pin an exact count — we just
	// verify the cap doesn't explode (each kind contributes at most
	// its limit).
	callees, callers, tests := 0, 0, 0
	for _, e := range edges {
		switch e.EdgeKind {
		case "CALLEES":
			callees++
		case "CALLERS":
			callers++
		case "TESTS":
			tests++
		}
	}
	if callees > 1 {
		t.Errorf("callees = %d, want <= 1", callees)
	}
	if callers > 1 {
		t.Errorf("callers = %d, want <= 1", callers)
	}
	if tests > 1 {
		t.Errorf("tests = %d, want <= 1", tests)
	}
}
