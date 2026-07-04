package store

import (
	"sort"
	"strings"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/id"
	"github.com/kellenff/yactt/internal/parser"
	"github.com/kellenff/yactt/internal/summarizer"
)

// symbolKey is the (package, name) pair used to index declarations across the
// repo for symbol-addressed lookups. The pair is keyed only when both are
// present so we can also do name-only fallbacks.
type symbolKey struct {
	pkg  string
	name string
}

// SymbolLookup is one match for a (pkg, name) lookup. Exported so tool
// handlers and the search backend can build on top of the same shape.
type SymbolLookup struct {
	File string
	Sym  parser.Symbol
}

// symbolEntry is the package-private counterpart kept around for the
// internal index. Lookup exports the public shape so tools don't need to
// import unexported fields.
type symbolEntry struct {
	file string
	sym  parser.Symbol
}

func toLookup(e symbolEntry) SymbolLookup { return SymbolLookup{File: e.file, Sym: e.sym} }

// symIndex is an immutable-once-built index of every declaration in the repo.
// We rebuild it on full Load and on ReloadInvalidate; reads are lockless.
type symIndex struct {
	byName map[symbolKey][]symbolEntry
}

func (r *Repo) rebuildIndex() {
	// Snapshot the symbol map under the read lock, then drop it before
	// publishing the new index. RWMutex does not support lock upgrades, so the
	// index build happens on a local copy; readers see the previous index
	// until publication, which is correct (temporary miss is harmless).
	snapshot := make(map[string][]parser.Symbol)
	r.mu.RLock()
	for k, v := range r.symbolsByPath {
		snapshot[k] = v
	}
	r.mu.RUnlock()

	idx := &symIndex{byName: make(map[symbolKey][]symbolEntry, len(snapshot)*4)}
	for path, syms := range snapshot {
		pkg := pkgFromPath(r.root, path, r.rootPkg)
		for _, s := range syms {
			// Index under (pkg, name) AND under ("", name) so name-only
			// lookups can find it — most of the symbol-level tools (search,
			// find_symbol, node_edges callees) operate on names, not full
			// paths.
			idx.byName[symbolKey{pkg, s.Name}] = append(idx.byName[symbolKey{pkg, s.Name}], symbolEntry{file: path, sym: s})
			idx.byName[symbolKey{"", s.Name}] = append(idx.byName[symbolKey{"", s.Name}], symbolEntry{file: path, sym: s})
		}
	}
	r.mu.Lock()
	r.index = idx
	r.mu.Unlock()
}

// pkgFromPath infers the (Go) package name for a file under root. Thin alias
// over PackagePath; kept for clarity in the resolver call site.
func pkgFromPath(root, path, _ string) string {
	return PackagePath(root, path)
}

// Lookup returns the package-exported view of declarations matching (pkg, name).
// Either argument may be empty. Results are sorted by file path for stability.
//
// When pkg is empty, the lookup is purely by name; the rebuild step stores
// each symbol under both (pkg, name) and ("", name), so we don't double-index
// here — we just consult the empty-pkg bucket.
func (r *Repo) Lookup(pkg, name string) []SymbolLookup {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.index == nil {
		return nil
	}
	var raw []symbolEntry
	switch {
	case pkg != "" && name != "":
		raw = r.index.byName[symbolKey{pkg, name}]
	case name != "":
		raw = r.index.byName[symbolKey{"", name}]
	case pkg != "":
		// pkg-only is unusual; fall back to walking every entry with the
		// matching package. Linear, but the index is fully in memory so this
		// is fast for typical repos.
		for k, vs := range r.index.byName {
			if k.pkg == pkg {
				raw = append(raw, vs...)
			}
		}
	default:
		// Both empty: emit everything — used by find_symbol's wildcard path.
		for _, vs := range r.index.byName {
			raw = append(raw, vs...)
		}
	}
	sort.Slice(raw, func(i, j int) bool { return raw[i].file < raw[j].file })
	out := make([]SymbolLookup, len(raw))
	for i, e := range raw {
		out[i] = toLookup(e)
	}
	return out
}

// LocateSymbol returns the (file, Symbol) for an id.ID of code-kind. File-
// kind IDs point at the containing file without a symbol.
//
// Returns ErrNotFound if the ID has no shape we can resolve against the local
// index (cross-package / cross-module is a Phase 2 capability).
func (r *Repo) LocateSymbol(nodeID id.ID) (file string, sym parser.Symbol, foundSymbol bool, err error) {
	if r.index == nil {
		return "", parser.Symbol{}, false, ErrNotFound
	}
	switch nodeID.Kind {
	case id.KindFile:
		f, ferr := r.CachedFile(idToFilePath(r.root, nodeID))
		if ferr != nil {
			return "", parser.Symbol{}, false, ferr
		}
		return f.Path, parser.Symbol{Kind: "source_file", Name: filepathBase(f.Path), StartRow: 0, EndRow: f.LineCount()}, true, nil
	case id.KindFunction:
		pkg, _, name, perr := nodeID.FunctionParts()
		if perr != nil {
			return "", parser.Symbol{}, false, perr
		}
		entries := r.Lookup(pkg, name)
		if len(entries) == 0 {
			// Fallback to name-only lookup.
			entries = r.Lookup("", name)
		}
		if len(entries) == 0 {
			return "", parser.Symbol{}, false, ErrNotFound
		}
		// Prefer the exact-package match when available.
		for _, e := range entries {
			if e.Sym.Kind == "function_declaration" {
				return e.File, e.Sym, true, nil
			}
		}
		return entries[0].File, entries[0].Sym, true, nil
	case id.KindMethod:
		pkg, class, name, perr := nodeID.MethodParts()
		if perr != nil {
			return "", parser.Symbol{}, false, perr
		}
		entries := r.Lookup(pkg, class)
		if len(entries) == 0 {
			return "", parser.Symbol{}, false, ErrNotFound
		}
		// Find a method inside the class's source range.
		for _, e := range entries {
			if e.Sym.Kind != "type_declaration" {
				continue
			}
			if _, ferr := r.CachedFile(e.File); ferr != nil {
				continue
			}
			for _, m := range r.Symbols(e.File) {
				if m.Kind != "method_declaration" || m.Name != name {
					continue
				}
				if m.StartRow >= e.Sym.StartRow && m.EndRow <= e.Sym.EndRow {
					return e.File, m, true, nil
				}
			}
		}
		return "", parser.Symbol{}, false, ErrNotFound
	case id.KindClass, id.KindModule:
		var pkg, name string
		var perr error
		if nodeID.Kind == id.KindClass {
			pkg, name, perr = nodeID.ClassParts()
		} else {
			pkg, name, _ = nodeID.ClassParts() // module uses the same shape
		}
		if perr != nil {
			return "", parser.Symbol{}, false, perr
		}
		entries := r.Lookup(pkg, name)
		if len(entries) == 0 {
			entries = r.Lookup("", name)
		}
		for _, e := range entries {
			if e.Sym.Kind == "type_declaration" {
				return e.File, e.Sym, true, nil
			}
		}
		if len(entries) > 0 {
			return entries[0].File, entries[0].Sym, true, nil
		}
		return "", parser.Symbol{}, false, ErrNotFound
	}
	return "", parser.Symbol{}, false, ErrNotFound
}

// filepathBase mirrors path/filepath.Base without the import to keep this
// package's dependency surface tight (and avoid forcing the caller to pass
// through tofilepath).
func filepathBase(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// idToFilePath converts a file:<path> ID back to an absolute path inside root.
// The ID body is already repo-relative per Appendix A.
func idToFilePath(root string, fid id.ID) string {
	return joinPath(root, fid.Body)
}

// joinPath is a single-separator joiner that normalises leading separators.
func joinPath(root, rel string) string {
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		return root
	}
	return root + "/" + rel
}

// SummaryMaterializer produces a deterministic summary for the symbol. It is
// exported because the tool handlers pick the layer up directly.
func SummaryMaterializer(kind domain.NodeKind, name, docComment, firstLine string) (string, *domain.Provenance) {
	label := kindLabel(kind)
	text := summarizer.Summarize(label, docComment, firstLine)
	return text, domain.NewProvenance("summarizer", "v1").Ptr()
}

func kindLabel(k domain.NodeKind) string {
	switch k {
	case domain.KindFunction:
		return "Function"
	case domain.KindMethod:
		return "Method"
	case domain.KindClass:
		return "Class"
	case domain.KindModule:
		return "Module"
	case domain.KindFile:
		return "File"
	case domain.KindPackage:
		return "Package"
	}
	return "Symbol"
}
