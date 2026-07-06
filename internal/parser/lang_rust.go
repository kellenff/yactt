package parser

// Rust-extracted symbols. Tree-sitter-rust emits one node per declaration
// kind, mirroring how the Go and TypeScript grammars work:
//
//   * function_item   → top-level `fn foo() {}` (also covers `pub fn` and
//                       `async fn` — those keywords are separate preceding
//                       tokens, not part of the function name node).
//   * struct_item     → named product type, surfaced as struct_declaration.
//   * enum_item       → named sum type, surfaced as enum_declaration.
//   * trait_item      → named trait, surfaced as trait_declaration. Body is
//                       walked for `function_item` children so trait methods
//                       appear as method_declaration with the trait name
//                       filling Receiver.
//   * type_item       → `type Foo = Bar;` alias, surfaced as type_declaration.
//   * impl_item       → does NOT emit a symbol itself; the body is walked
//                       for `function_item` children so impl methods appear
//                       as method_declaration with the implementing type
//                       (last type_identifier) filling Receiver. For
//                       `impl Bar for Foo`, receiver is "Foo" (the type
//                       implementing the trait), not "Bar".
//
// `mod_item` is deliberately skipped — Rust modules are navigation nodes
// best inferred from the file path, mirroring the Python precedent where
// `mod foo {}` is also not surfaced as a standalone symbol. `const_item`
// and `static_item` are skipped per the JavaScript precedent (only
// callable declarations enter the symbol graph).
//
// ponytail: the receiver rule for `impl Bar for Foo` is "the implementer,
// not the trait". The resolver looks up methods by `(Receiver, Name)`, and
// a call site `foo.bar()` will be tied to the type doing the calling —
// which is the implementer. The trait is only relevant for trait-method
// resolution, which is out of scope for the syntactic graph today.

import (
	sitter "github.com/smacker/go-tree-sitter"

	rustgrammar "github.com/smacker/go-tree-sitter/rust"
)

// Rust is the tree-sitter driver for the Rust programming language.
type Rust struct{}

// Name implements Language.
func (Rust) Name() Name { return LangRust }

// Grammar implements Language.
func (Rust) Grammar() *sitter.Language { return rustgrammar.GetLanguage() }

// FileExtensions implements Language. `.rs` is the only Rust source extension.
func (Rust) FileExtensions() []string {
	return []string{"rs"}
}

// ModulePath returns "" — Rust has no file-local module name. Cargo.toml-
// aware crate extraction is a follow-up; the symbol graph doesn't need it
// to resolve identifiers within a crate (path-based module inference is
// the store's job).
func (Rust) ModulePath(_ *sitter.Node, _ []byte) string {
	return ""
}

// extractRustSymbols walks the top level of a Rust source file and emits
// a Symbol per declaration. Methods inside trait / impl bodies are
// emitted as method_declaration symbols by walking those bodies inline.
func extractRustSymbols(root *sitter.Node, source []byte) []Symbol {
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
		case "function_item":
			if s, ok := rustFunctionSymbol(ch, source, ""); ok {
				out = append(out, s)
			}
		case "struct_item":
			if s, ok := rustNamedTypeSymbol(ch, source, "struct_declaration"); ok {
				out = append(out, s)
			}
		case "enum_item":
			if s, ok := rustNamedTypeSymbol(ch, source, "enum_declaration"); ok {
				out = append(out, s)
			}
		case "trait_item":
			if s, ok := rustNamedTypeSymbol(ch, source, "trait_declaration"); ok {
				out = append(out, s)
			}
			out = append(out, rustTraitMethods(ch, source)...)
		case "type_item":
			if s, ok := rustNamedTypeSymbol(ch, source, "type_declaration"); ok {
				out = append(out, s)
			}
		case "impl_item":
			out = append(out, rustImplMethods(ch, source)...)
		}
	}
	return out
}

// rustFunctionSymbol reads a function_item's name + line range. The Rust
// grammar tags the function name as `identifier` (not `type_identifier`).
// receiverClass is empty for free functions; populated for trait / impl
// methods descended from a body walk.
func rustFunctionSymbol(node *sitter.Node, source []byte, receiverClass string) (Symbol, bool) {
	if node == nil {
		return Symbol{}, false
	}
	name := rustNodeName(node, source)
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

// rustNamedTypeSymbol reads struct_item / enum_item / trait_item / type_item
// and produces a Symbol of the requested kind. All four use `type_identifier`
// for the name child.
func rustNamedTypeSymbol(node *sitter.Node, source []byte, kind string) (Symbol, bool) {
	if node == nil {
		return Symbol{}, false
	}
	name := rustTypeName(node, source)
	if name == "" {
		return Symbol{}, false
	}
	start := int(node.StartPoint().Row)
	end := int(node.EndPoint().Row) + 1
	return Symbol{Kind: kind, Name: name, StartRow: start, EndRow: end}, true
}

// rustTraitMethods walks a trait_item's body and emits one
// method_declaration symbol per function_item child. The trait name fills
// Receiver. Mirrors the Python class-body walk: we only descend one level
// because Rust doesn't allow nested trait definitions.
func rustTraitMethods(trait *sitter.Node, source []byte) []Symbol {
	if trait == nil {
		return nil
	}
	traitName := rustTypeName(trait, source)
	if traitName == "" {
		return nil
	}
	body := rustImplBody(trait)
	if body == nil {
		return nil
	}
	return rustCollectMethods(body, source, traitName)
}

// rustImplMethods walks an impl_item's body and emits method_declaration
// symbols. The receiver is the IMPLEMENTING type — for `impl Bar for Foo`
// that's "Foo", not "Bar" (see package comment for why).
//
// We do not emit the impl_item itself as a symbol. Implementations are
// navigation edges between type and trait, not standalone declarations
// for the syntactic graph. The trait / type identifiers are still surfaced
// through their own declarations in the same file (or elsewhere).
func rustImplMethods(impl *sitter.Node, source []byte) []Symbol {
	if impl == nil {
		return nil
	}
	receiver := rustImplReceiver(impl, source)
	if receiver == "" {
		// No implementing type — anonymous impl (`impl Trait {}`); we
		// can't meaningfully attach a receiver, so skip the body.
		return nil
	}
	body := rustImplBody(impl)
	if body == nil {
		return nil
	}
	return rustCollectMethods(body, source, receiver)
}

// rustImplReceiver returns the implementing type name from an impl_item
// header. For `impl Foo` it's "Foo"; for `impl Bar for Foo` it's "Foo".
// Tree-sitter-rust exposes the implementing type as the `type` field on
// the impl_item node — using ChildByFieldName keeps the rule independent
// of header order (which would matter if the grammar ever changed how
// `for` is tokenised).
func rustImplReceiver(impl *sitter.Node, source []byte) string {
	if impl == nil {
		return ""
	}
	if t := impl.ChildByFieldName("type"); t != nil {
		return t.Content(source)
	}
	// Fallback: scan for the last type_identifier before the body. The
	// first type_identifier is the implementing type for `impl Foo {...}`
	// and the trait for `impl Bar for Foo {...}`; in both cases the
	// implementer appears at or after the `for` keyword, so picking the
	// last header identifier is the closest approximation to the `type`
	// field without leaning on it.
	var last string
	for i := 0; i < int(impl.ChildCount()); i++ {
		c := impl.Child(i)
		if c == nil {
			continue
		}
		if c.Type() == "declaration_list" {
			break
		}
		if c.Type() == "type_identifier" {
			last = c.Content(source)
		}
	}
	return last
}

// rustImplBody resolves the declaration_list child of an impl_item or
// trait_item. Falls back to a child scan if the field name changes in
// a future grammar revision.
func rustImplBody(node *sitter.Node) *sitter.Node {
	if node == nil {
		return nil
	}
	if body := node.ChildByFieldName("body"); body != nil {
		return body
	}
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		if c.Type() == "declaration_list" {
			return c
		}
	}
	return nil
}

// rustCollectMethods walks a declaration_list body and emits one
// method_declaration per function_item child. Shared between trait and
// impl walks to keep the receiver-stamping rule in one place.
func rustCollectMethods(body *sitter.Node, source []byte, receiver string) []Symbol {
	out := make([]Symbol, 0, 4)
	for i := 0; i < int(body.ChildCount()); i++ {
		ch := body.Child(i)
		if ch == nil {
			continue
		}
		if ch.Type() != "function_item" {
			continue
		}
		if s, ok := rustFunctionSymbol(ch, source, receiver); ok {
			out = append(out, s)
		}
	}
	return out
}

// rustNodeName returns the first `identifier` child's text, or "" if
// there isn't one. Used for function_item names — the Rust grammar tags
// function names with `identifier`, mirroring how Go does for func names.
func rustNodeName(node *sitter.Node, source []byte) string {
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

// rustTypeName returns the first `type_identifier` child's text, or ""
// if there isn't one. Used for struct / enum / trait / type-alias names.
func rustTypeName(node *sitter.Node, source []byte) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		if c.Type() == "type_identifier" {
			return c.Content(source)
		}
	}
	return ""
}
