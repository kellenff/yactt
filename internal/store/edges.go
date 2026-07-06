package store

import (
	sitter "github.com/smacker/go-tree-sitter"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/parser"
)

// EdgeEntry is one record in the byCallEdge index — a call site captured at
// rebuild time. The same record is reachable from two keys (callee name; or
// "<file>::<callerName>") and emitted as either CALLERS or CALLEES depending
// on the query direction. `Kind` records which side the entry was found on
// when both sides apply (today always EdgeCallers, since the *caller* index
// side is what the cross-file scan emits naturally).
type EdgeEntry struct {
	File   string
	Caller parser.Symbol
	Callee string
	Kind   domain.EdgeKind
}

// edgeKey is the dual-side key for byCallEdge.
//
//	side=0 → keyed by callee name      ("who calls ValidateToken?")
//	side=1 → keyed by (file, caller)   ("what does Login in x.go call?")
type edgeKey struct {
	side uint8
	name string
}

// ByteRangeFromRows converts a (startRow, endRow) pair into an approximate
// byte range over `src`. Each row is treated as a line of variable width;
// we just iterate once and count newline boundaries. The approximation is
// good enough for bounding a tree-sitter walk to one symbol body — a
// straddling parent node is filtered out and the children that lie inside
// the target row range still match.
func ByteRangeFromRows(src []byte, startRow, endRow int) (int, int) {
	row := 0
	idx := 0
	for i, b := range src {
		if row == startRow && idx == 0 {
			idx = i
		}
		if b == '\n' {
			row++
			if row == endRow {
				return idx, i
			}
		}
	}
	return idx, len(src)
}

// WalkExpr visits every tree-sitter node in n whose byte range overlaps
// [startByte, endByte), calling visit on each. A zero [startByte, endByte)
// pair means "walk the whole tree" — used at index-build time when an exact
// byte filter is unnecessary.
//
// Returning false from visit short-circuits the subtree walk. n itself is
// skipped when its range is fully outside [startByte, endByte); the existing
// tool-layer walker accepts the same approximation.
//
// Iterative DFS over an explicit stack: one frame per (node, childIndex)
// instead of one frame per recursion. Go's growable stack absorbs the
// recursive version at any realistic tree-sitter depth, but the iterative
// form bounds memory by the tree breadth, not depth, and removes the
// question entirely.
func WalkExpr(n *sitter.Node, startByte, endByte int, visit func(*sitter.Node) bool) {
	if n == nil {
		return
	}
	type frame struct {
		node  *sitter.Node
		child int // next child index to descend into
		seen  bool // whether visit() has been called for this node yet
	}
	stack := []frame{{node: n}}
	for len(stack) > 0 {
		f := &stack[len(stack)-1]
		if !f.seen {
			if startByte != 0 || endByte != 0 {
				s := int(f.node.StartByte())
				e := int(f.node.EndByte())
				if e <= startByte || s >= endByte {
					stack = stack[:len(stack)-1]
					continue
				}
			}
			f.seen = true
			if !visit(f.node) {
				stack = stack[:len(stack)-1]
				continue
			}
		}
		if f.child < int(f.node.ChildCount()) {
			child := f.node.Child(f.child)
			f.child++
			if child != nil {
				stack = append(stack, frame{node: child})
			}
		} else {
			stack = stack[:len(stack)-1]
		}
	}
}

// ExtractCalleeName handles `Foo()` and `pkg.Foo()` syntax. Tree-sitter Go
// emits a call_expression's function ref as either:
//
//   - identifier (`Login(...)`),
//   - selector_expression (`payments.Charge(...)`), or
//   - field_identifier — the rightmost token of a selector is field_identifier,
//     not identifier; without this branch cross-package calls return "".
//
// Returns "" for unrecognised nodes so the caller can ignore them.
func ExtractCalleeName(n *sitter.Node, src []byte) string {
	if n == nil {
		return ""
	}
	switch n.Type() {
	case "identifier", "field_identifier":
		return n.Content(src)
	case "selector_expression":
		if n.ChildCount() == 0 {
			return ""
		}
		return ExtractCalleeName(n.Child(int(n.ChildCount())-1), src)
	}
	return ""
}
