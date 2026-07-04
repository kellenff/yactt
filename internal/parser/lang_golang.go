package parser

// Go-extracted symbols: tree-sitter exposes the Go grammar with these top-level
// constructs of interest to the aggregator:
//
//   * function_declaration     → free function (or method, see below)
//   * method_declaration       → method on a receiver — same node shape as a
//     function_declaration in the Go grammar; we distinguish via the presence
//     of a `receiver` child.
//   * type_declaration         → type alias / spec; the inner `type_spec`
//     carries the actual kind (struct / interface / alias).
//
// We intentionally extract *syntactic* symbols only — LSP/SCIP would add type
// resolution and cross-file edges later, but per design §5.5 tree-sitter is
// the floor and it answers everything with confidence=0.5 in the provenance.

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// extractGoSymbols walks the top level of a Go source file and emits a Symbol
// per declaration. Nested declarations (struct fields are skipped; struct
// methods will be picked up as siblings because tree-sitter lifts method
// declarations to the file scope, not into the type_spec).
func extractGoSymbols(root *sitter.Node, source []byte) []Symbol {
	if root == nil {
		return nil
	}
	out := make([]Symbol, 0, 8)
	n := int(root.ChildCount())
	for i := 0; i < n; i++ {
		ch := root.Child(i)
		if ch == nil {
			continue
		}
		switch ch.Type() {
		case "function_declaration":
			if s, ok := goFunctionSymbol(ch, source, false); ok {
				out = append(out, s)
			}
		case "method_declaration":
			if s, ok := goFunctionSymbol(ch, source, true); ok {
				out = append(out, s)
			}
		case "type_declaration":
			if s, ok := goTypeSymbol(ch, source); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// goFunctionSymbol reads a function/method declaration's name + line range.
// isMethod is passed in by the caller based on whether the node is wrapped in a
// method_declaration — the Go grammar uses the same `function_declaration`
// shape inside both.
func goFunctionSymbol(node *sitter.Node, source []byte, isMethod bool) (Symbol, bool) {
	if node == nil {
		return Symbol{}, false
	}
	name := ""
	n := int(node.ChildCount())
	for i := 0; i < n; i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		// tree-sitter-go uses `identifier` for the function name in both
		// function_declaration and method_declaration after the optional
		// `receiver`.
		if c.Type() == "identifier" || c.Type() == "field_identifier" {
			name = c.Content(source)
			break
		}
	}
	if name == "" {
		return Symbol{}, false
	}
	start := int(node.StartPoint().Row)
	end := int(node.EndPoint().Row) + 1
	if isMethod {
		return Symbol{Kind: "method_declaration", Name: name, StartRow: start, EndRow: end}, true
	}
	return Symbol{Kind: "function_declaration", Name: name, StartRow: start, EndRow: end}, true
}

// goTypeSymbol reads a type_declaration node and extracts the type_spec child
// to produce a "class" symbol for structs (interpreter for MVP — interface /
// alias are documented as future refinements).
func goTypeSymbol(node *sitter.Node, source []byte) (Symbol, bool) {
	if node == nil {
		return Symbol{}, false
	}
	n := int(node.ChildCount())
	for i := 0; i < n; i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		if c.Type() != "type_spec" {
			continue
		}
		// Look for the type name — first `identifier` after the optional type
		// parameters list.
		var name string
		cn := int(c.ChildCount())
		for j := 0; j < cn; j++ {
			cc := c.Child(j)
			if cc == nil {
				continue
			}
			if cc.Type() == "type_identifier" || cc.Type() == "identifier" {
				name = cc.Content(source)
				break
			}
		}
		if name == "" {
			return Symbol{}, false
		}
		start := int(node.StartPoint().Row)
		end := int(node.EndPoint().Row) + 1
		return Symbol{Kind: "type_declaration", Name: name, StartRow: start, EndRow: end}, true
	}
	return Symbol{}, false
}
