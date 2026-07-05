package parser

// JavaScript-extracted symbols. Tree-sitter-javascript exposes the same top-
// level shapes we care about as the Go grammar, with one quirk: declarations
// lifted to file scope via `export` arrive wrapped in an `export_statement`.
//
// Top-level shapes:
//   * function_declaration / generator_function_declaration → free function
//   * class_declaration                                      → class
//   * export_statement                                       → wrapper around
//     a function_declaration, class_declaration, or default clause
//
// Methods are not at file scope — they live inside `class_body` as
// `method_definition` nodes. We walk class_bodies once and emit each method
// as a method_declaration symbol with the parent class name in Receiver.

import (
	sitter "github.com/smacker/go-tree-sitter"

	javascriptgrammar "github.com/smacker/go-tree-sitter/javascript"
)

// JavaScript is the tree-sitter driver for the JavaScript programming language.
type JavaScript struct{}

// Name implements Language.
func (JavaScript) Name() Name { return LangJavaScript }

// Grammar implements Language.
func (JavaScript) Grammar() *sitter.Language { return javascriptgrammar.GetLanguage() }

// FileExtensions implements Language.
func (JavaScript) FileExtensions() []string {
	return []string{"js", "mjs", "cjs", "jsx"}
}

// ModulePath returns "" — JavaScript has no file-local module name. Mirrors
// the Go driver's behavior when no package clause is present.
func (JavaScript) ModulePath(_ *sitter.Node, _ []byte) string {
	return ""
}

// extractJavaScriptSymbols walks the top level of a JavaScript source file
// and emits a Symbol per declaration. Nested class methods are emitted as
// method_declaration symbols by walking class bodies inline.
func extractJavaScriptSymbols(root *sitter.Node, source []byte) []Symbol {
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
		case "function_declaration", "generator_function_declaration":
			if s, ok := jsFunctionSymbol(ch, source, ""); ok {
				out = append(out, s)
			}
		case "class_declaration":
			if s, ok := jsClassSymbol(ch, source, ""); ok {
				out = append(out, s)
			}
			out = append(out, jsClassMethods(ch, source)...)
		case "export_statement":
			out = append(out, jsExportedTopLevel(ch, source)...)
		}
	}
	return out
}

// jsExportedTopLevel peels one level of an `export` wrapper and re-dispatches.
// `export function foo()` → function_declaration; `export class C {}` →
// class_declaration + its methods; `export default ...` is skipped (no name
// to surface at the symbol layer).
func jsExportedTopLevel(export *sitter.Node, source []byte) []Symbol {
	if export == nil {
		return nil
	}
	out := make([]Symbol, 0, 1)
	for i := 0; i < int(export.NamedChildCount()); i++ {
		ch := export.NamedChild(i)
		if ch == nil {
			continue
		}
		switch ch.Type() {
		case "function_declaration", "generator_function_declaration":
			if s, ok := jsFunctionSymbol(ch, source, ""); ok {
				out = append(out, s)
			}
		case "class_declaration":
			if s, ok := jsClassSymbol(ch, source, ""); ok {
				out = append(out, s)
			}
			out = append(out, jsClassMethods(ch, source)...)
		}
	}
	return out
}

// jsFunctionSymbol reads a function_declaration's name + line range.
// receiverClass is empty for top-level functions; populated when descending
// into a class member (e.g. an arrow-field becomes a method via class body walk).
func jsFunctionSymbol(node *sitter.Node, source []byte, receiverClass string) (Symbol, bool) {
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
		if c.Type() == "identifier" || c.Type() == "property_identifier" {
			name = c.Content(source)
			break
		}
	}
	if name == "" {
		return Symbol{}, false
	}
	start := int(node.StartPoint().Row)
	end := int(node.EndPoint().Row) + 1
	if receiverClass != "" {
		return Symbol{
			Kind:     "method_declaration",
			Name:     name,
			StartRow: start,
			EndRow:   end,
			Receiver: receiverClass,
		}, true
	}
	return Symbol{Kind: "function_declaration", Name: name, StartRow: start, EndRow: end}, true
}

// jsClassSymbol reads a class_declaration's name + line range.
func jsClassSymbol(node *sitter.Node, source []byte, _ string) (Symbol, bool) {
	if node == nil {
		return Symbol{}, false
	}
	name := jsClassName(node, source)
	if name == "" {
		return Symbol{}, false
	}
	start := int(node.StartPoint().Row)
	end := int(node.EndPoint().Row) + 1
	return Symbol{Kind: "class_declaration", Name: name, StartRow: start, EndRow: end}, true
}

// jsClassName resolves a class_declaration's name. Tree-sitter-js uses
// `identifier` for plain classes and `type_identifier` for class expressions
// used as type references. The first such child wins.
func jsClassName(node *sitter.Node, source []byte) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		if c.Type() == "identifier" || c.Type() == "type_identifier" {
			return c.Content(source)
		}
	}
	return ""
}

// jsClassMethods walks a class_declaration's body and emits one symbol per
// method_definition. The parent class name fills Receiver so downstream
// disambiguation matches the Go precedent.
func jsClassMethods(class *sitter.Node, source []byte) []Symbol {
	if class == nil {
		return nil
	}
	className := jsClassName(class, source)
	if className == "" {
		return nil
	}
	body := class.ChildByFieldName("body")
	if body == nil {
		return nil
	}
	out := make([]Symbol, 0, 4)
	for i := 0; i < int(body.ChildCount()); i++ {
		ch := body.Child(i)
		if ch == nil {
			continue
		}
		if ch.Type() != "method_definition" {
			continue
		}
		if s, ok := jsFunctionSymbol(ch, source, className); ok {
			out = append(out, s)
		}
	}
	return out
}