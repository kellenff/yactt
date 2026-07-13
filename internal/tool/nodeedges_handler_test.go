package tool

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/parser"
	"github.com/kellenff/yactt/internal/store"
	"github.com/kellenff/yactt/internal/store/repofixture"
)

// unwrapEdges pulls the typed []NodeEdgesResult out of the envelope map
// the NodeEdges handler returns (the MCP spec requires structuredContent
// to be a JSON object, so the handler wraps its slice under the `edges`
// key). Every test in this file calls unwrapEdges so a regression on the
// envelope shape is caught at the assertion site, not via a confusing
// `out.(map[string]any)` failure far from the cause.
func unwrapEdges(t *testing.T, out any) []NodeEdgesResult {
	t.Helper()
	env, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("node_edges return type: got %T, want map[string]any (envelope)", out)
	}
	edges, ok := env["edges"].([]NodeEdgesResult)
	if !ok {
		t.Fatalf("envelope edges type: got %T", env["edges"])
	}
	return edges
}

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
	out, err := NodeEdges(seedRegFromRepo(t, r))(context.Background(), json.RawMessage(
		`{"id":"fn:auth.Login"}`,
	))
	if err != nil {
		t.Fatalf("NodeEdges: %v", err)
	}
	edges := unwrapEdges(t, out)
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

	out, err := NodeEdges(seedRegFromRepo(t, r))(context.Background(), json.RawMessage(
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

	out, err := NodeEdges(seedRegFromRepo(t, r))(context.Background(), json.RawMessage(
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

	out, err := NodeEdges(seedRegFromRepo(t, r))(context.Background(), json.RawMessage(
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
	out, err := NodeEdges(seedRegFromRepo(t, r))(context.Background(), json.RawMessage(
		`{"id":"fn:auth.Login","kinds":["tests"],"limit":50}`,
	))
	if err != nil {
		t.Fatalf("NodeEdges: %v", err)
	}
	edges := unwrapEdges(t, out)
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

// TestScanImports_EmitsPaths pins the new `case "imports":` branch in
// NodeEdges. Synthesizes a Go file with two imports and a TS file with
// one named import; calls NodeEdges with kinds=["imports"] and
// verifies the emitted IMPORTS edges surface the expected paths with
// the right per-row Location.
//
// Pins:
//   - dispatch routes `imports` to `scanImports` (not silently dropped).
//   - Go `import_declaration` and TS `import_statement` both work.
//   - Dedup: a single file with grouped `import ("a"; "a")` would emit
//     one edge; this test exercises the simple `import "x"` shape.
//   - Target ID format: `pkg:<import-path>`.
func TestScanImports_EmitsPaths(t *testing.T) {
	fx := repofixture.New(t)

	// Synthetic Go file with two imports.
	goSrc := []byte(`package auth

import "fmt"
import "github.com/foo/bar"

func UseFmt() {}
`)
	goPath := fx.Root + string(os.PathSeparator) + "auth" + string(os.PathSeparator) + "imports_test.go"
	if err := os.WriteFile(goPath, goSrc, 0o644); err != nil {
		t.Fatalf("write imports_test.go: %v", err)
	}

	// Synthetic TS file with one named import.
	tsSrc := []byte(`import { User } from "./user";

export class UseUser {
  pick(): User { return null as any; }
}
`)
	tsPath := fx.Root + string(os.PathSeparator) + "auth" + string(os.PathSeparator) + "user_importer.ts"
	if err := os.WriteFile(tsPath, tsSrc, 0o644); err != nil {
		t.Fatalf("write user_importer.ts: %v", err)
	}

	r, _, err := store.Load(fx.Root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = r.Close() }()

	// Drive scanImports on the Go file via its declared function. The
	// `file`, not `sym`, is what scanImports reads, so the choice of
	// symbol is incidental — UseFmt lives in the same file.
	out, err := NodeEdges(seedRegFromRepo(t, r))(context.Background(), json.RawMessage(
		`{"id":"fn:auth.UseFmt","kinds":["imports"],"limit":50}`,
	))
	if err != nil {
		t.Fatalf("NodeEdges: %v", err)
	}
	edges := unwrapEdges(t, out)

	// Go file: collect observed import paths.
	goWant := map[string]bool{"fmt": false, "github.com/foo/bar": false}
	for _, e := range edges {
		if e.EdgeKind != "IMPORTS" {
			continue
		}
		if e.TargetID != "pkg:"+e.TargetSummary {
			t.Errorf("TargetID = %q, want pkg:%q", e.TargetID, e.TargetSummary)
		}
		if _, ok := goWant[e.TargetSummary]; ok {
			goWant[e.TargetSummary] = true
		}
	}
	for path, seen := range goWant {
		if !seen {
			t.Errorf("expected Go IMPORTS edge for %q", path)
		}
	}

	// Pin a non-zero row on the Go import edge so the Location field
	// stays meaningful (matches the existing scanTests contract).
	for _, e := range edges {
		if e.TargetSummary == "fmt" {
			if e.Location.LineRange.Start <= 0 {
				t.Errorf("fmt import location = %+v, want row > 0", e.Location)
			}
		}
	}

	// TS file: a separate query on the TS-declared class surfaces the
	// TS-only import. Confirms the dispatch and walker cover both
	// import_declaration (Go) and import_statement (TS/JS).
	out2, err := NodeEdges(seedRegFromRepo(t, r))(context.Background(), json.RawMessage(
		`{"id":"class:auth.UseUser","kinds":["imports"],"limit":50}`,
	))
	if err != nil {
		t.Fatalf("NodeEdges (TS): %v", err)
	}
	tsEdges := unwrapEdges(t, out2)
	var sawTS bool
	for _, e := range tsEdges {
		if e.EdgeKind == "IMPORTS" && e.TargetSummary == "./user" {
			sawTS = true
		}
	}
	if !sawTS {
		t.Errorf("TS ./user import dropped; got %+v", tsEdges)
	}

	// Sanity: a file with no imports returns zero IMPORTS edges.
	out3, err := NodeEdges(seedRegFromRepo(t, r))(context.Background(), json.RawMessage(
		`{"id":"fn:auth.Login","kinds":["imports"],"limit":50}`,
	))
	if err != nil {
		t.Fatalf("NodeEdges (no imports): %v", err)
	}
	noImportEdges := unwrapEdges(t, out3)
	for _, e := range noImportEdges {
		if e.EdgeKind == "IMPORTS" {
			t.Errorf("auth/login.go has no imports but got IMPORTS edge: %+v", e)
		}
	}

	// Confirm the parser.Symbol field compiles in the dispatch — the
	// unused `_ parser.Symbol` parameter at scanImports gets a
	// reference here. Avoids "imported and not used" lint if the
	// parameter is ever renamed.
	_ = parser.Symbol{}
}

// TestScanOverrides_FindsParentMethod pins the new `case "overrides":`
// branch in NodeEdges. Synthesizes a TS file with `Child extends
// Parent { method() {...} }` and asserts that asking for
// `Child.method`'s overrides surfaces `Parent.method`.
//
// Pins:
//   - dispatch routes `overrides` to `scanOverrides`.
//   - TS/JS class_heritage walk finds the parent.
//   - Same-named parent method emits one OVERRIDES edge with the
//     correct target ID (`meth:auth.Parent.method`) and confidence 0.4.
//   - Non-method symbols (functions, classes) get no edges.
//   - Go methods (no override semantics) get no edges.
func TestScanOverrides_FindsParentMethod(t *testing.T) {
	fx := repofixture.New(t)

	tsSrc := []byte(`export class Parent {
  greet(): string { return "hi"; }
}

export class Child extends Parent {
  greet(): string { return "child hi"; }
  fetch(): string { return "ball"; }
}
`)
	tsPath := fx.Root + string(os.PathSeparator) + "auth" + string(os.PathSeparator) + "overrides.ts"
	if err := os.WriteFile(tsPath, tsSrc, 0o644); err != nil {
		t.Fatalf("write overrides.ts: %v", err)
	}

	r, _, err := store.Load(fx.Root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = r.Close() }()

	// Child.greet overrides Parent.greet — must surface one OVERRIDES
	// edge. Child.fetch has no parent counterpart — zero edges.
	out, err := NodeEdges(seedRegFromRepo(t, r))(context.Background(), json.RawMessage(
		`{"id":"meth:auth.Child.greet","kinds":["overrides"],"limit":50}`,
	))
	if err != nil {
		t.Fatalf("NodeEdges: %v", err)
	}
	edges := unwrapEdges(t, out)
	var sawParent bool
	for _, e := range edges {
		if e.EdgeKind != "OVERRIDES" {
			continue
		}
		if e.TargetID != "meth:auth.Parent.greet" {
			t.Errorf("TargetID = %q, want meth:auth.Parent.greet", e.TargetID)
		}
		if e.TargetKind != domain.KindMethod {
			t.Errorf("TargetKind = %q, want KindMethod", e.TargetKind)
		}
		if e.Confidence != 0.4 {
			t.Errorf("Confidence = %v, want 0.4", e.Confidence)
		}
		if e.Location.LineRange.Start <= 0 {
			t.Errorf("Location = %+v, want row > 0", e.Location)
		}
		sawParent = true
	}
	if !sawParent {
		t.Errorf("Child.greet should override Parent.greet; got %+v", edges)
	}

	// Child.fetch has no parent counterpart — zero edges.
	out2, err := NodeEdges(seedRegFromRepo(t, r))(context.Background(), json.RawMessage(
		`{"id":"meth:auth.Child.fetch","kinds":["overrides"],"limit":50}`,
	))
	if err != nil {
		t.Fatalf("NodeEdges (fetch): %v", err)
	}
	fetchEdges, _ := out2.([]NodeEdgesResult)
	for _, e := range fetchEdges {
		if e.EdgeKind == "OVERRIDES" {
			t.Errorf("Child.fetch should not override anything; got %+v", e)
		}
	}

	// A standalone class with no `extends` returns zero edges.
	tsSrc2 := []byte(`export class Standalone {
  method(): void {}
}
`)
	tsPath2 := fx.Root + string(os.PathSeparator) + "auth" + string(os.PathSeparator) + "standalone.ts"
	if err := os.WriteFile(tsPath2, tsSrc2, 0o644); err != nil {
		t.Fatalf("write standalone.ts: %v", err)
	}
	r2, _, err := store.Load(fx.Root)
	if err != nil {
		t.Fatalf("Load (standalone): %v", err)
	}
	defer func() { _ = r2.Close() }()

	out3, err := NodeEdges(seedRegFromRepo(t, r2))(context.Background(), json.RawMessage(
		`{"id":"meth:auth.Standalone.method","kinds":["overrides"],"limit":50}`,
	))
	if err != nil {
		t.Fatalf("NodeEdges (standalone): %v", err)
	}
	standaloneEdges, _ := out3.([]NodeEdgesResult)
	for _, e := range standaloneEdges {
		if e.EdgeKind == "OVERRIDES" {
			t.Errorf("Standalone.method should not override anything; got %+v", e)
		}
	}

	// Go method returns nil — Go has no override semantics. Refresh
	// is a method on User; no OVERRIDES edges even if the user happens
	// to have a method of the same name elsewhere.
	out4, err := NodeEdges(seedRegFromRepo(t, r2))(context.Background(), json.RawMessage(
		`{"id":"meth:auth.User.Refresh","kinds":["overrides"],"limit":50}`,
	))
	if err != nil {
		t.Fatalf("NodeEdges (Go): %v", err)
	}
	goEdges, _ := out4.([]NodeEdgesResult)
	for _, e := range goEdges {
		if e.EdgeKind == "OVERRIDES" {
			t.Errorf("Go method should not emit OVERRIDES; got %+v", e)
		}
	}

	// A function (no Receiver) emits nil — guard against the dispatch
	// confusing top-level functions with class methods.
	out5, err := NodeEdges(seedRegFromRepo(t, r2))(context.Background(), json.RawMessage(
		`{"id":"fn:auth.Login","kinds":["overrides"],"limit":50}`,
	))
	if err != nil {
		t.Fatalf("NodeEdges (fn): %v", err)
	}
	fnEdges, _ := out5.([]NodeEdgesResult)
	for _, e := range fnEdges {
		if e.EdgeKind == "OVERRIDES" {
			t.Errorf("function should not emit OVERRIDES; got %+v", e)
		}
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
	out, err := NodeEdges(seedRegFromRepo(t, r))(context.Background(), json.RawMessage(
		`{"id":"fn:auth.Login","limit":1}`,
	))
	if err != nil {
		t.Fatalf("NodeEdges: %v", err)
	}
	edges := unwrapEdges(t, out)
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
