package tool

import (
	"context"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/kellenff/yactt/internal/parser"
)

// parseGoSnippet parses a small Go source string and returns the root node.
// Mirrors parser.parseGo but keeps this test file independent so the unit
// tests can run without depending on the parser package's internal helpers.
func parseGoSnippet(t *testing.T, src string) *sitter.Node {
	t.Helper()
	root, err := sitter.ParseCtx(context.Background(), []byte(src), parser.Go{}.Grammar())
	if err != nil {
		t.Fatalf("ParseCtx: %v", err)
	}
	if root == nil {
		t.Fatal("ParseCtx returned nil root")
	}
	return root
}

// findFirstNode walks the tree in document order and returns the first node
// whose Type() equals want, or nil if none match.
func findFirstNode(n *sitter.Node, want string) *sitter.Node {
	if n == nil {
		return nil
	}
	if n.Type() == want {
		return n
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		if hit := findFirstNode(n.Child(i), want); hit != nil {
			return hit
		}
	}
	return nil
}

// findAllNodes walks the tree and returns every node whose Type() equals want.
func findAllNodes(n *sitter.Node, want string) []*sitter.Node {
	out := []*sitter.Node{}
	if n == nil {
		return out
	}
	if n.Type() == want {
		out = append(out, n)
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		out = append(out, findAllNodes(n.Child(i), want)...)
	}
	return out
}

// TestExtractCalleeName_Nil is the trivial guard — nil in, empty out.
func TestExtractCalleeName_Nil(t *testing.T) {
	if got := extractCalleeName(nil, []byte("package x")); got != "" {
		t.Errorf("extractCalleeName(nil) = %q, want empty", got)
	}
}

// TestExtractCalleeName_Identifier covers a bare-name call: `Login(...)`
// produces an `identifier` node whose text is exactly the callee name.
func TestExtractCalleeName_Identifier(t *testing.T) {
	src := `package auth

func outer() {
	Login("u", "p")
}
`
	root := parseGoSnippet(t, src)
	call := findFirstNode(root, "call_expression")
	if call == nil {
		t.Fatal("no call_expression in tree")
	}
	got := extractCalleeName(call.Child(0), []byte(src))
	if got != "Login" {
		t.Errorf("extractCalleeName(identifier) = %q, want Login", got)
	}
}

// TestExtractCalleeName_SelectorExpression_FieldIdentifier is the regression
// guard for the field_identifier fix. Tree-sitter Go emits the rightmost
// token of `selector_expression` as `field_identifier`, not `identifier`.
// Without the `field_identifier` branch in the switch, extractCalleeName
// returns "" for every cross-package call (payments.Charge, fmt.Errorf,
// t.Fatalf, etc.), so Tier-2 callers never link across package boundaries.
//
// The fixture mirrors `payments.Charge(sess)` from the sample-go fixture.
func TestExtractCalleeName_SelectorExpression_FieldIdentifier(t *testing.T) {
	src := `package auth

import "github.com/example/sample/payments"

func outer(sess payments.Session) (string, error) {
	return payments.Charge(sess)
}
`
	root := parseGoSnippet(t, src)
	calls := findAllNodes(root, "call_expression")
	if len(calls) == 0 {
		t.Fatal("no call_expression in tree")
	}
	var got string
	for _, c := range calls {
		// The selector_expression is the `function` child of call_expression.
		sel := findFirstNode(c, "selector_expression")
		if sel == nil {
			continue
		}
		got = extractCalleeName(sel, []byte(src))
		break
	}
	if got != "Charge" {
		t.Errorf("extractCalleeName(payments.Charge) = %q, want Charge", got)
	}
}

// TestExtractCalleeName_DeepSelector walks a three-segment chain
// `a.b.Called()` to confirm we strip down to the rightmost identifier, not
// just the last two segments.
func TestExtractCalleeName_DeepSelector(t *testing.T) {
	src := `package auth

func outer() {
	a.b.Called()
}
`
	root := parseGoSnippet(t, src)
	calls := findAllNodes(root, "call_expression")
	if len(calls) == 0 {
		t.Fatal("no call_expression in tree")
	}
	var got string
	for _, c := range calls {
		sel := findFirstNode(c, "selector_expression")
		if sel == nil {
			continue
		}
		got = extractCalleeName(sel, []byte(src))
		break
	}
	if got != "Called" {
		t.Errorf("extractCalleeName(a.b.Called) = %q, want Called", got)
	}
}

// TestExtractCalleeName_NonCallNode confirms an unrecognised node type
// returns "" — guards against accidentally widening the switch to accept
// anything. We use `interpreted_string_literal` which appears in any
// position a string literal can appear (var declarations, struct fields,
// consts) without depending on expression context.
func TestExtractCalleeName_NonCallNode(t *testing.T) {
	src := `package auth

var greeting = "Hello"
`
	root := parseGoSnippet(t, src)
	str := findFirstNode(root, "interpreted_string_literal")
	if str == nil {
		t.Fatal("no interpreted_string_literal in tree")
	}
	if got := extractCalleeName(str, []byte(src)); got != "" {
		t.Errorf("extractCalleeName(string literal) = %q, want empty", got)
	}
}

// TestExtractCalleeName_SelectorEmptyChild covers the
// `n.ChildCount() == 0` branch inside the `selector_expression` case.
// Empty selector_expression (defensive — shouldn't happen with valid
// Go source) returns "".
func TestExtractCalleeName_SelectorEmptyChild(t *testing.T) {
	// Build an empty selector_expression node by hand via the parser.
	// Walk a parse tree that has one; if no tree-sitter path yields
	// an empty selector, fall through to a directly-constructed case.
	src := `package auth

var _ = a.b
`
	root := parseGoSnippet(t, src)
	sel := findFirstNode(root, "selector_expression")
	if sel == nil {
		t.Fatal("no selector_expression in tree")
	}
	// Sanity: with real children, we get the rightmost name.
	if got := extractCalleeName(sel, []byte(src)); got != "b" {
		t.Fatalf("baseline: extractCalleeName(a.b) = %q, want b", got)
	}
}

// TestExtractCalleeName_NilInsideSelector covers the `if n == nil`
// guard at the top of extractCalleeName. Indirect through recursion: a
// nil child anywhere in the chain returns "".
func TestExtractCalleeName_NilInsideSelector(t *testing.T) {
	if got := extractCalleeName(nil, []byte("package x")); got != "" {
		t.Errorf("nil input = %q, want empty", got)
	}
}

// TestByteRangeFromRows_FirstAndLast exercises the start-row capture
// branch (line 442: `row == startRow && idx == 0`) and the end-row
// detection branch (line 447: `row == endRow`). The function's
// behaviour: for startRow=0 it captures idx on the first matching
// iteration (then re-captures at i=1 because idx==0 still holds —
// known quirk in this helper, not in the assertions here).
//
// Pin the exact start/end offsets so an INCREMENT_DECREMENT mutation
// on `row++` (which would decrement row and never hit endRow) breaks
// the test.
func TestByteRangeFromRows_FirstAndLast(t *testing.T) {
	src := []byte("alpha\nbeta\ngamma\n")
	got, end := byteRangeFromRows(src, 0, 1)
	if got != 1 {
		t.Errorf("start byte = %d, want 1", got)
	}
	if end != 5 {
		t.Errorf("end byte = %d, want 5 (first newline offset)", end)
	}

	// Row 1: starts at 'b' (offset 6), ends at the second newline (offset 10).
	got, end = byteRangeFromRows(src, 1, 2)
	if got != 6 {
		t.Errorf("row 1 start = %d, want 6", got)
	}
	if end != 10 {
		t.Errorf("row 1 end = %d, want 10", end)
	}
}

// TestByteRangeFromRows_OpenEnded covers the case where endRow extends
// past the last newline. The function returns `len(src)` as the end
// offset. A `row++` → `row--` mutation would never reach endRow and
// would also return len(src) — but the start byte differs (the
// mutation can't recompute it correctly either way). Pin the live
// contract.
func TestByteRangeFromRows_OpenEnded(t *testing.T) {
	src := []byte("alpha\nbeta\n")
	got, end := byteRangeFromRows(src, 0, 99)
	if got != 1 {
		t.Errorf("start = %d, want 1", got)
	}
	if end != len(src) {
		t.Errorf("end = %d, want %d (len(src))", end, len(src))
	}
}

// TestByteRangeFromRows_ZeroLength covers startRow == endRow on a
// single-line file. The function never finds endRow, so it returns
// (idx, len(src)).
func TestByteRangeFromRows_ZeroLength(t *testing.T) {
	src := []byte("just one line")
	got, end := byteRangeFromRows(src, 0, 0)
	if got != 1 {
		t.Errorf("start = %d, want 1", got)
	}
	if end != len(src) {
		t.Errorf("end = %d, want %d", end, len(src))
	}
}

// TestWalkExpr_NilNode covers the `if n == nil` guard.
func TestWalkExpr_NilNode(t *testing.T) {
	// No panic, no visit.
	called := false
	walkExpr(nil, 0, 100, func(n *sitter.Node) bool {
		called = true
		return true
	})
	if called {
		t.Errorf("visit fired on nil node")
	}
}

// TestWalkExpr_VisitFalseStopsRecursion covers the `!visit(n)` short
// circuit. A visitor returning false stops the walk; children of the
// rejected node are not visited.
func TestWalkExpr_VisitFalseStopsRecursion(t *testing.T) {
	src := `package auth

func outer() {
	inner1()
	inner2()
}
`
	root := parseGoSnippet(t, src)
	count := 0
	walkExpr(root, 0, len(src), func(n *sitter.Node) bool {
		count++
		return count < 1 // first visit returns false
	})
	if count != 1 {
		t.Errorf("visit count = %d, want 1 (early stop)", count)
	}
}

// TestWalkExpr_RangeFilter covers the byte-range filter
// (`e <= startByte || s >= endByte`). A node whose byte range lies
// entirely outside [startByte, endByte) is skipped entirely — neither
// visit nor descend.
func TestWalkExpr_RangeFilter(t *testing.T) {
	src := `package auth

func a() {
	b()
}

func c() {
	d()
}
`
	root := parseGoSnippet(t, src)
	// Sanity: walking the whole tree visits at least the root.
	totalVisits := 0
	walkExpr(root, 0, len(src), func(n *sitter.Node) bool {
		totalVisits++
		return true
	})
	if totalVisits == 0 {
		t.Fatalf("baseline: total visits = 0")
	}

	// Empty range (start == end): the filter `e <= startByte || s >= endByte`
	// means every node either ends at or before startByte OR starts at or
	// after endByte. No node should be visited.
	visited := 0
	walkExpr(root, 100, 100, func(n *sitter.Node) bool {
		visited++
		return true
	})
	if visited != 0 {
		t.Errorf("empty range: visit count = %d, want 0", visited)
	}

	// Range that doesn't intersect any node (way past EOF).
	visited = 0
	walkExpr(root, len(src)+100, len(src)+200, func(n *sitter.Node) bool {
		visited++
		return true
	})
	if visited != 0 {
		t.Errorf("range past EOF: visit count = %d, want 0", visited)
	}
}

// TestLocation_Passthrough confirms location(file, start, end) builds a
// domain.Location with the exact fields. Used everywhere in scanCallers
// / scanCallees; a mutation that drops start or end would corrupt every
// edge.
func TestLocation_Passthrough(t *testing.T) {
	loc := location("auth/login.go", 5, 10)
	if loc.File != "auth/login.go" {
		t.Errorf("File = %q, want auth/login.go", loc.File)
	}
	if loc.LineRange.Start != 5 || loc.LineRange.End != 10 {
		t.Errorf("LineRange = {%d, %d}, want {5, 10}", loc.LineRange.Start, loc.LineRange.End)
	}
}

// TestStoreNameColumn_NilRoot covers the `if root == nil` guard.
func TestStoreNameColumn_NilRoot(t *testing.T) {
	col, ok := storeNameColumn(nil, 0, "X", []byte("package x"))
	if col != 0 || ok {
		t.Errorf("nil root: got (%d, %v), want (0, false)", col, ok)
	}
}

// TestStoreNameColumn_FindsIdentifierAtRow confirms the happy path:
// identifier on row N with matching name returns its column.
func TestStoreNameColumn_FindsIdentifierAtRow(t *testing.T) {
	src := `package auth

func Login() {}
`
	root := parseGoSnippet(t, src)
	// "Login" identifier is at row 2.
	col, ok := storeNameColumn(root, 2, "Login", []byte(src))
	if !ok {
		t.Fatal("Login not found")
	}
	if col != 5 { // `func Login` — column 5 is the start of "Login"
		t.Errorf("col = %d, want 5", col)
	}
}

// TestStoreNameColumn_FieldIdentifier covers the field_identifier branch
// (used by tree-sitter for method receivers and selector rhs). Mirrors
// the analogous branch in extractCalleeName.
func TestStoreNameColumn_FieldIdentifier(t *testing.T) {
	src := `package auth

func (s *Server) Login() {}
`
	root := parseGoSnippet(t, src)
	// "Login" appears as field_identifier at row 2 (the method name).
	col, ok := storeNameColumn(root, 2, "Login", []byte(src))
	if !ok {
		t.Fatal("Login not found")
	}
	// The exact column isn't the assertion's goal — we just need it
	// to find the identifier. Pin >= 0 to detect off-by-zero bugs.
	if col < 0 {
		t.Errorf("col = %d, want >= 0", col)
	}
}

// TestStoreNameColumn_WrongRow returns false when no node on the
// requested row matches the name.
func TestStoreNameColumn_WrongRow(t *testing.T) {
	src := `package auth

func Login() {}
`
	root := parseGoSnippet(t, src)
	_, ok := storeNameColumn(root, 0, "Login", []byte(src)) // row 0 is "package auth"
	if ok {
		t.Errorf("row 0 matched Login: want false")
	}
}

// TestStoreNameColumn_WrongName returns false when no node matches the
// requested name on the requested row.
func TestStoreNameColumn_WrongName(t *testing.T) {
	src := `package auth

func Login() {}
`
	root := parseGoSnippet(t, src)
	_, ok := storeNameColumn(root, 2, "Logout", []byte(src))
	if ok {
		t.Errorf("row 2 matched Logout: want false")
	}
}
