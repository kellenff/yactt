package parser

import (
	sitter "github.com/smacker/go-tree-sitter"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/summarizer"
)

// Symbol is a syntactic declaration extracted from a parsed source file.
//
// It is intentionally language-agnostic: the kind tag and the qualified-name
// pieces are filled in by the language-specific driver. Consumers downstream
// turn these into Node IDs and domain.Symbol entries.
type Symbol struct {
	// Kind is the syntactic kind of this declaration in the *grammar* — not
	// the same as domain.NodeKind. Examples: "function_declaration",
	// "method_declaration", "type_spec". The translator below maps these.
	Kind     string
	Name     string
	StartRow int // 0-based byte row (tree-sitter convention)
	EndRow   int // 0-based, exclusive

	// Receiver is the local name of the receiver type, populated only for
	// method_declaration symbols. For `func (s *Server) M()`, Receiver is
	// "Server" — the package-qualified prefix (e.g. `pkg.Server`) is
	// stripped, since cross-package resolution is out of scope for MVP.
	Receiver string
}

// SymbolKind maps a parser.Symbol's grammar kind to the canonical
// domain.NodeKind. Single source of truth — used by tool handlers and search.
func SymbolKind(s Symbol) domain.NodeKind {
	switch s.Kind {
	case "function_declaration":
		return domain.KindFunction
	case "method_declaration":
		return domain.KindMethod
	case "type_declaration", "class_declaration",
		"interface_declaration", "type_alias_declaration", "enum_declaration":
		return domain.KindClass
	}
	return domain.KindModule
}

// SymbolSummary produces a "<Kind>: <Name>" line via the summarizer package.
// Single source of truth — used by tool handlers and search.
//
// The Name is passed through domain.SanitizeName to strip zero-width and
// bidi-override Unicode from the rendered summary (AST05). Raw bytes remain
// in the tokens layer for callers that need them.
func SymbolSummary(s Symbol) string {
	return summarizer.Summarize(kindLabel(s.Kind), "", domain.SanitizeName(s.Name))
}

func kindLabel(kind string) string {
	switch kind {
	case "function_declaration":
		return "Function"
	case "method_declaration":
		return "Method"
	case "type_declaration", "class_declaration",
		"interface_declaration", "type_alias_declaration", "enum_declaration":
		return "Class"
	}
	return "Symbol"
}

// ExtractSymbols walks the parsed tree and returns top-level declarations.
// Returns ErrUnsupported if the language has no wired extractor.
func ExtractSymbols(lang Language, root *sitter.Node, source []byte) ([]Symbol, error) {
	switch lang.(type) {
	case Go:
		return extractGoSymbols(root, source), nil
	case TypeScript:
		return extractTypeScriptSymbols(root, source), nil
	case JavaScript:
		return extractJavaScriptSymbols(root, source), nil
	case Python:
		return extractPythonSymbols(root, source), nil
	}
	return nil, ErrUnsupported
}
