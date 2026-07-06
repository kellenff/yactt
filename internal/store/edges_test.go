package store

import (
	"context"
	"strings"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
	golanggrammar "github.com/smacker/go-tree-sitter/golang"
)

// TestWalkExpr_DeepTree is the regression guard for the iterative-walk
// finding: the recursive WalkExpr consumes one stack frame per node,
// so a deeply-nested parse tree (e.g., a 10 000-deep chain of `f(f(...))`)
// blows the goroutine stack. The iterative version bounds stack growth
// at one frame.
func TestWalkExpr_DeepTree(t *testing.T) {
	const depth = 10_000
	src := strings.Repeat("f(", depth) + "x" + strings.Repeat(")", depth)

	root, err := sitter.ParseCtx(context.Background(), []byte(src), golanggrammar.GetLanguage())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var count int
	WalkExpr(root, 0, 0, func(_ *sitter.Node) bool {
		count++
		return true
	})
	// The walk must visit every named node without crashing. A correct
	// iterative walk counts in the thousands here; a broken (still
	// recursive) walk panics with "stack overflow" — the test would
	// fail there.
	if count < depth {
		t.Errorf("walked %d nodes, want >= %d", count, depth)
	}
}

// TestWalkExpr_ShortCircuitPreserved pins the second contract of WalkExpr:
// returning false from visit must skip the rest of the subtree. Used by
// callers that want to bound the search at the first match.
func TestWalkExpr_ShortCircuitPreserved(t *testing.T) {
	src := `package x
func a() {}
func b() {}
`
	root, err := sitter.ParseCtx(context.Background(), []byte(src), golanggrammar.GetLanguage())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var visited int
	WalkExpr(root, 0, 0, func(_ *sitter.Node) bool {
		visited++
		return false
	})
	if visited != 1 {
		t.Errorf("visit called %d times with short-circuit, want 1", visited)
	}
}