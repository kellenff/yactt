package tool

import (
	"strings"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/parser"
	"github.com/kellenff/yactt/internal/store"
)

// prov returns the per-layer tree-sitter provenance line. Single source of
// truth lives in domain; this is a thin alias for handler call sites.
func prov() *domain.Provenance { return domain.TreeSitterProvenancePtr() }

// packagePath returns the dotted package path for a file under root. Delegates
// to store — single source of truth.
func packagePath(root, p string) string { return store.PackagePath(root, p) }

// relPath returns the repo-relative path for an absolute file path.
func relPath(root, p string) string {
	rel := strings.TrimPrefix(p, root)
	return strings.TrimPrefix(rel, "/")
}

// baseName is the last path element, used for display labels.
func baseName(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// symbolID builds a canonical node ID for a parser.Symbol. The package part
// uses the dotted directory path so `node_get(fn:auth.login.Login)` resolves
// back to the right file.
func symbolID(file string, s parser.Symbol, root string) string {
	pkg := packagePath(root, file)
	prefix := "fn:"
	if s.Kind == "type_declaration" {
		prefix = "class:"
	}
	return prefix + joinDotted(pkg, s.Name)
}

// symbolKind maps a parser.Symbol kind to a domain.NodeKind.
func symbolKind(s parser.Symbol) domain.NodeKind {
	return parser.SymbolKind(s)
}

// symbolSummary produces a "Function: Name" style summary without a doc
// comment (search re-uses the same pattern).
func symbolSummary(s parser.Symbol) string {
	return parser.SymbolSummary(s)
}

// joinDotted combines pkg + name into a dotted id body, dropping empty
// components so we never end up with leading/trailing dots.
func joinDotted(pkg, name string) string {
	if pkg == "" {
		return name
	}
	if name == "" {
		return pkg
	}
	return pkg + "." + name
}
