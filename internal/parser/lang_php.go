package parser

// PHP-extracted symbols. Tree-sitter-php emits one node shape per
// declaration kind, paralleling how the JavaScript and TypeScript
// grammars work — but with PHP-specific quirks worth knowing:
//
//   * function_definition   → top-level `function foo() {}`. The
//     function name lives in a dedicated `name` child (not `identifier`),
//     so the helper reads that field explicitly rather than scanning
//     for `identifier` children.
//   * class_declaration      → `class Foo {}`. Body is `declaration_list`,
//     same shape as the interface and trait bodies.
//   * interface_declaration  → `interface Foo {}`. Body is
//     `declaration_list`; methods inside are `method_declaration`.
//   * trait_declaration      → `trait Foo {}`. Body is `declaration_list`
//     containing `method_declaration` nodes.
//   * enum_declaration       → `enum Foo {}` (PHP 8.1+). Cases are
//     `enum_case` children of `enum_declaration_list`; cases are not
//     surfaced as their own symbols (matches the Rust precedent — enum
//     variants ride on the enum's own declaration).
//
// `method_declaration` is a PHP-specific node kind that appears ONLY
// inside class / interface / trait bodies; free functions are always
// `function_definition`. The walker handles that distinction by routing
// class-body children through the method path and the file root through
// the function path.
//
// `namespace_definition` is deliberately skipped — PHP namespaces are
// navigation nodes best inferred from the file's `namespace` clause, and
// the symbol graph doesn't need a separate per-namespace node today.

import (
	sitter "github.com/smacker/go-tree-sitter"

	phpgrammar "github.com/smacker/go-tree-sitter/php"
)

// PHP is the tree-sitter driver for the PHP programming language.
type PHP struct{}

// Name implements Language.
func (PHP) Name() Name { return LangPHP }

// Grammar implements Language.
func (PHP) Grammar() *sitter.Language { return phpgrammar.GetLanguage() }

// FileExtensions implements Language. `.php` covers regular sources;
// `.phtml`, `.php3/4/5/7`, and `.phps` cover templated / versioned /
// highlight-only files that share the same grammar shapes.
func (PHP) FileExtensions() []string {
	return []string{"php", "phtml", "php3", "php4", "php5", "php7", "phps"}
}

// ModulePath returns "" — PHP namespace is a `namespace_definition`
// child of the file root, not a file-local module name we surface today.
// Mirrors the Python / JavaScript / Rust precedent.
func (PHP) ModulePath(_ *sitter.Node, _ []byte) string {
	return ""
}

// extractPHPSymbols walks the top level of a PHP source file and emits
// a Symbol per declaration. Methods inside class / interface / trait
// bodies are emitted as method_declaration symbols by walking those
// bodies inline.
func extractPHPSymbols(root *sitter.Node, source []byte) []Symbol {
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
		case "function_definition":
			if s, ok := phpFunctionSymbol(ch, source, ""); ok {
				out = append(out, s)
			}
		case "class_declaration":
			if s, ok := phpNamedTypeSymbol(ch, source, "class_declaration"); ok {
				out = append(out, s)
			}
			out = append(out, phpBodyMethods(ch, source)...)
		case "interface_declaration":
			if s, ok := phpNamedTypeSymbol(ch, source, "interface_declaration"); ok {
				out = append(out, s)
			}
			out = append(out, phpBodyMethods(ch, source)...)
		case "trait_declaration":
			if s, ok := phpNamedTypeSymbol(ch, source, "trait_declaration"); ok {
				out = append(out, s)
			}
			out = append(out, phpBodyMethods(ch, source)...)
		case "enum_declaration":
			if s, ok := phpNamedTypeSymbol(ch, source, "enum_declaration"); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// phpBodyMethods walks a class / interface / trait declaration's body
// and emits one method_declaration symbol per `method_declaration`
// child. The parent type name fills Receiver so downstream
// disambiguation matches the JavaScript / Python / Rust precedent.
func phpBodyMethods(decl *sitter.Node, source []byte) []Symbol {
	if decl == nil {
		return nil
	}
	typeName := phpTypeName(decl, source)
	if typeName == "" {
		return nil
	}
	body := phpBodyNode(decl)
	if body == nil {
		return nil
	}
	out := make([]Symbol, 0, 4)
	for i := 0; i < int(body.ChildCount()); i++ {
		ch := body.Child(i)
		if ch == nil {
			continue
		}
		if ch.Type() != "method_declaration" {
			continue
		}
		if s, ok := phpMethodSymbol(ch, source, typeName); ok {
			out = append(out, s)
		}
	}
	return out
}

// phpBodyNode resolves the body of a class / interface / trait
// declaration. The grammar tags it as `declaration_list` via the
// `body` field; we fall back to a child scan in case the field name
// changes in a future grammar revision.
func phpBodyNode(decl *sitter.Node) *sitter.Node {
	if decl == nil {
		return nil
	}
	if body := decl.ChildByFieldName("body"); body != nil {
		return body
	}
	for i := 0; i < int(decl.ChildCount()); i++ {
		c := decl.Child(i)
		if c == nil {
			continue
		}
		if c.Type() == "declaration_list" {
			return c
		}
	}
	return nil
}

// phpFunctionSymbol reads a function_definition's name + line range.
// PHP puts the function name in a dedicated `name` child rather than
// under `identifier`, so we read that field explicitly. receiverClass
// is empty for free functions; PHP has no free-function receiver but
// we keep the signature symmetric with the JS / Python / Rust drivers
// so future PHP-syntax support (e.g. arrow functions on a receiver)
// can reuse the same helper.
func phpFunctionSymbol(node *sitter.Node, source []byte, receiverClass string) (Symbol, bool) {
	if node == nil {
		return Symbol{}, false
	}
	name := phpTypeName(node, source)
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

// phpMethodSymbol reads a method_declaration's name + line range. PHP
// methods live inside class / interface / trait bodies; the receiver
// is always populated by the caller.
func phpMethodSymbol(node *sitter.Node, source []byte, receiverClass string) (Symbol, bool) {
	return phpFunctionSymbol(node, source, receiverClass)
}

// phpNamedTypeSymbol reads class_declaration / interface_declaration /
// trait_declaration / enum_declaration and produces a Symbol of the
// requested kind. All four expose the type name via a `name` child.
func phpNamedTypeSymbol(node *sitter.Node, source []byte, kind string) (Symbol, bool) {
	if node == nil {
		return Symbol{}, false
	}
	name := phpTypeName(node, source)
	if name == "" {
		return Symbol{}, false
	}
	start := int(node.StartPoint().Row)
	end := int(node.EndPoint().Row) + 1
	return Symbol{Kind: kind, Name: name, StartRow: start, EndRow: end}, true
}

// phpTypeName returns the `name` child's text, or "" if there isn't
// one. Used uniformly for class / interface / trait / enum / function
// / method names — the PHP grammar tags all of them with `name`.
func phpTypeName(node *sitter.Node, source []byte) string {
	if node == nil {
		return ""
	}
	if n := node.ChildByFieldName("name"); n != nil {
		return n.Content(source)
	}
	return ""
}