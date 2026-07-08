package tool

import (
	"strings"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/entity"
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

// entityFromSymbol is the tool layer's single source of truth for
// lifting a parser.Symbol into an entity.Entity. It builds the entity
// (kind triple + identity) and resolves the receiver via the repo's
// symbol index, so callers get one value with everything pre-computed.
//
// ponytail: the receiver lookup is best-effort (try-with-pkg, then
// without) — unresolvable receivers stay nil and the wire omits the
// receiver field. Don't narrow this until a real consumer hits misses.
func entityFromSymbol(s parser.Symbol, file string, repo *store.Repo) entity.Entity {
	e := entity.FromParser(s, file, packagePath(repo.Root(), file))
	if s.Receiver == "" {
		return e
	}
	pkg := packagePath(repo.Root(), file)
	// Try with the enclosing package first; fall back to a package-less
	// search for TS/JS-style receivers and Go receivers in same-package
	// types. Mirrors findsymbol.go's matchByNamePattern two-pass logic.
	for _, m := range repo.Lookup(pkg, s.Receiver) {
		cand := entity.FromParser(m.Sym, m.File, packagePath(repo.Root(), m.File))
		if cand.DomainKind().IsCode() {
			return *e.WithReceiver(&cand)
		}
	}
	for _, m := range repo.Lookup("", s.Receiver) {
		cand := entity.FromParser(m.Sym, m.File, packagePath(repo.Root(), m.File))
		if cand.DomainKind().IsCode() {
			return *e.WithReceiver(&cand)
		}
	}
	return e
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