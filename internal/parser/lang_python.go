package parser

// Python-extracted symbols. Tree-sitter-python emits one node shape per
// declaration kind, with two important wrappers around them:
//
//   * function_definition   → covers both `def foo():` and `async def foo():`
//   * class_definition      → contains a nested `block` of function_definitions
//     (the methods). Unlike Go/JS/TS, methods are NOT lifted to file scope —
//     they're textually nested inside the class body.
//   * decorated_definition  → wraps a function_definition or class_definition
//     to attach one or more `@decorator` lines. The decorator itself isn't a
//     symbol we surface; we peel the wrapper and dispatch on the inner node.
//
// Top-level shapes the walker handles: function_definition, class_definition,
// decorated_definition. Module-level `class` and `def` live at the file root;
// the walker only descends into class bodies to find methods.

import (
	sitter "github.com/smacker/go-tree-sitter"

	pythonggrammar "github.com/smacker/go-tree-sitter/python"
)

// Python is the tree-sitter driver for the Python programming language.
type Python struct{}

// Name implements Language.
func (Python) Name() Name { return LangPython }

// Grammar implements Language.
func (Python) Grammar() *sitter.Language { return pythonggrammar.GetLanguage() }

// FileExtensions implements Language. `.py` covers regular sources; `.pyi`
// covers PEP 484 stub files (mypy / pyright), which use the same grammar
// shapes — function_definition, class_definition — so the same walker
// handles both.
func (Python) FileExtensions() []string {
	return []string{"py", "pyi"}
}

// ModulePath returns "" — Python has no file-local module name. Mirrors the
// JavaScript / TypeScript precedent.
func (Python) ModulePath(_ *sitter.Node, _ []byte) string {
	return ""
}

// extractPythonSymbols walks the top level of a Python source file and emits
// a Symbol per declaration. Methods inside class bodies are emitted as
// method_declaration symbols by walking class bodies inline.
func extractPythonSymbols(root *sitter.Node, source []byte) []Symbol {
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
			if s, ok := pyFunctionSymbol(ch, source, ""); ok {
				out = append(out, s)
			}
		case "class_definition":
			if s, ok := pyClassSymbol(ch, source); ok {
				out = append(out, s)
			}
			out = append(out, pyClassMethods(ch, source)...)
		case "decorated_definition":
			out = append(out, pyDecoratedTopLevel(ch, source)...)
		}
	}
	return out
}

// pyDecoratedTopLevel peels one level of a `decorated_definition` wrapper and
// re-dispatches on the inner function_definition or class_definition. The
// decorator lines themselves aren't surfaced — only the thing they decorate.
func pyDecoratedTopLevel(decorated *sitter.Node, source []byte) []Symbol {
	if decorated == nil {
		return nil
	}
	out := make([]Symbol, 0, 1)
	for i := 0; i < int(decorated.NamedChildCount()); i++ {
		ch := decorated.NamedChild(i)
		if ch == nil {
			continue
		}
		switch ch.Type() {
		case "function_definition":
			if s, ok := pyFunctionSymbol(ch, source, ""); ok {
				out = append(out, s)
			}
		case "class_definition":
			if s, ok := pyClassSymbol(ch, source); ok {
				out = append(out, s)
			}
			out = append(out, pyClassMethods(ch, source)...)
		}
	}
	return out
}

// pyFunctionSymbol reads a function_definition's name + line range. The
// Python grammar uses `identifier` for the function name on both sync and
// async definitions (the `async` keyword is a separate preceding token, not
// part of the function name).
func pyFunctionSymbol(node *sitter.Node, source []byte, receiverClass string) (Symbol, bool) {
	if node == nil {
		return Symbol{}, false
	}
	name := pyNodeName(node, source)
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

// pyClassSymbol reads a class_definition's name + line range. Python classes
// name themselves via the first `identifier` child — `superclasses` is a
// separate argument_list child we deliberately skip.
func pyClassSymbol(node *sitter.Node, source []byte) (Symbol, bool) {
	name := pyNodeName(node, source)
	if name == "" {
		return Symbol{}, false
	}
	start := int(node.StartPoint().Row)
	end := int(node.EndPoint().Row) + 1
	return Symbol{Kind: "class_declaration", Name: name, StartRow: start, EndRow: end}, true
}

// pyClassMethods walks a class_definition's body and emits one
// method_declaration symbol per `function_definition` child. Decoration
// wrappers (decorated_definition inside the class body) are peeled so that
// `@staticmethod`-decorated methods still surface with the underlying
// function's name.
//
// ponytail: one-level walk, matching jsClassMethods. Python supports nested
// classes whose methods we deliberately ignore — they show up at top level
// only if you reach them through `Outer.Inner.method`, which is out of scope
// for the syntactic graph we surface today. Revisit when nested-class call
// edges are in the roadmap.
func pyClassMethods(class *sitter.Node, source []byte) []Symbol {
	if class == nil {
		return nil
	}
	className := pyNodeName(class, source)
	if className == "" {
		return nil
	}
	body := pyClassBody(class)
	if body == nil {
		return nil
	}
	out := make([]Symbol, 0, 4)
	for i := 0; i < int(body.ChildCount()); i++ {
		ch := body.Child(i)
		if ch == nil {
			continue
		}
		switch ch.Type() {
		case "function_definition":
			if s, ok := pyFunctionSymbol(ch, source, className); ok {
				out = append(out, s)
			}
		case "decorated_definition":
			out = append(out, pyClassDecoratedMethod(ch, source, className)...)
		}
	}
	return out
}

// pyClassDecoratedMethod peels a decorated_definition inside a class body
// and emits any function_definition inside as a method. Mirrors the
// top-level `pyDecoratedTopLevel` but stamps the enclosing class name as
// Receiver so the method routes correctly through the symbol graph.
func pyClassDecoratedMethod(decorated *sitter.Node, source []byte, className string) []Symbol {
	if decorated == nil {
		return nil
	}
	out := make([]Symbol, 0, 1)
	for i := 0; i < int(decorated.NamedChildCount()); i++ {
		ch := decorated.NamedChild(i)
		if ch == nil {
			continue
		}
		if ch.Type() == "function_definition" {
			if s, ok := pyFunctionSymbol(ch, source, className); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// pyNodeName returns the first `identifier` child's text, or "" if there
// isn't one. Used uniformly for function_definition (name) and
// class_definition (name) — the Python grammar tags both with `identifier`,
// not `type_identifier` like TypeScript does.
func pyNodeName(node *sitter.Node, source []byte) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		if c.Type() == "identifier" {
			return c.Content(source)
		}
	}
	return ""
}

// pyClassBody resolves a class_definition's body node. The grammar tags the
// `body` field on the class_definition; we also fall back to scanning
// children for a `block` node in case a future grammar version changes the
// field name.
func pyClassBody(class *sitter.Node) *sitter.Node {
	if class == nil {
		return nil
	}
	if body := class.ChildByFieldName("body"); body != nil {
		return body
	}
	for i := 0; i < int(class.ChildCount()); i++ {
		c := class.Child(i)
		if c == nil {
			continue
		}
		if c.Type() == "block" {
			return c
		}
	}
	return nil
}