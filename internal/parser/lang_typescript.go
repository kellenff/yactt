package parser

// TypeScript-extracted symbols. Mirrors the JavaScript walker and adds the
// three TS-only top-level declaration shapes:
//
//   * interface_declaration → named type, surfaced as class_declaration
//   * type_alias_declaration → named type alias, surfaced as class_declaration
//   * enum_declaration → named enum, surfaced as class_declaration
//
// All three map to KindClass downstream (single source of truth in
// SymbolKind). The KindClass bucket is the aggregator's "named type" —
// interfaces, type aliases, and enums are semantically equivalent to a Go
// `type Foo struct { ... }` declaration for graph purposes.

import (
	sitter "github.com/smacker/go-tree-sitter"

	typescriptgrammar "github.com/smacker/go-tree-sitter/typescript/typescript"
)

// TypeScript is the tree-sitter driver for the TypeScript programming language.
type TypeScript struct{}

// Name implements Language.
func (TypeScript) Name() Name { return LangTypeScript }

// Grammar implements Language.
func (TypeScript) Grammar() *sitter.Language { return typescriptgrammar.GetLanguage() }

// FileExtensions implements Language.
func (TypeScript) FileExtensions() []string {
	return []string{"ts", "mts", "cts", "tsx"}
}

// ModulePath returns "" — TypeScript has no file-local module name.
func (TypeScript) ModulePath(_ *sitter.Node, _ []byte) string {
	return ""
}

// extractTypeScriptSymbols walks the top level of a TypeScript source file
// and emits a Symbol per declaration.
func extractTypeScriptSymbols(root *sitter.Node, source []byte) []Symbol {
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
		case "interface_declaration", "type_alias_declaration", "enum_declaration":
			if s, ok := tsNamedTypeSymbol(ch, source); ok {
				out = append(out, s)
			}
		case "export_statement":
			out = append(out, tsExportedTopLevel(ch, source)...)
		}
	}
	return out
}

// tsExportedTopLevel peels one level of an `export` wrapper and re-dispatches
// across the full TS grammar (including the TS-only shapes).
func tsExportedTopLevel(export *sitter.Node, source []byte) []Symbol {
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
		case "interface_declaration", "type_alias_declaration", "enum_declaration":
			if s, ok := tsNamedTypeSymbol(ch, source); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// tsNamedTypeSymbol reads interface / type-alias / enum declarations and
// produces a class_declaration symbol for the named type. All three use
// `type_identifier` for the name child.
func tsNamedTypeSymbol(node *sitter.Node, source []byte) (Symbol, bool) {
	if node == nil {
		return Symbol{}, false
	}
	var name string
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		if c.Type() == "type_identifier" || c.Type() == "identifier" {
			name = c.Content(source)
			break
		}
	}
	if name == "" {
		return Symbol{}, false
	}
	start := int(node.StartPoint().Row)
	end := int(node.EndPoint().Row) + 1
	return Symbol{Kind: "class_declaration", Name: name, StartRow: start, EndRow: end}, true
}