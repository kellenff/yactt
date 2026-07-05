package store

import (
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/id"
	"github.com/kellenff/yactt/internal/parser"
	"github.com/kellenff/yactt/internal/source"
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
	byName     map[symbolKey][]symbolEntry
	byCallEdge map[edgeKey][]EdgeEntry
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

	idx := &symIndex{
		byName:     make(map[symbolKey][]symbolEntry, len(snapshot)*4),
		byCallEdge: make(map[edgeKey][]EdgeEntry),
	}
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

	// Second pass: walk every function_declaration and method_declaration
	// body, extract the bare callee name from each call_expression, and emit
	// one EdgeEntry per call site keyed both ways. The Tier-2 live walker in
	// tool/nodeedges.go mirrors this set (see scanCallers) so the persisted
	// index and the live fallback agree on what counts as a callable body.
	for path, syms := range snapshot {
		var f *source.File
		for _, s := range syms {
			if (s.Kind != "function_declaration" && s.Kind != "method_declaration") || s.Name == "" {
				continue
			}
			if f == nil {
				cf, err := r.CachedFile(path)
				if err != nil || cf == nil || cf.Root == nil {
					break
				}
				f = cf
			}
			startByte, endByte := ByteRangeFromRows(f.Bytes, s.StartRow, s.EndRow)
			seen := make(map[string]bool)
			WalkExpr(f.Root, startByte, endByte, func(n *sitter.Node) bool {
				if n.Type() != "call_expression" {
					return true
				}
				fn := n.ChildByFieldName("function")
				if fn == nil && n.ChildCount() > 0 {
					fn = n.Child(0)
				}
				if fn == nil {
					return true
				}
				callee := ExtractCalleeName(fn, f.Bytes)
				if callee == "" {
					return true
				}
				if seen[callee] {
					return true
				}
				seen[callee] = true
				entry := EdgeEntry{File: path, Caller: s, Callee: callee, Kind: domain.EdgeCallers}
				idx.byCallEdge[edgeKey{0, callee}] = append(idx.byCallEdge[edgeKey{0, callee}], entry)
				idx.byCallEdge[edgeKey{1, callerEdgeKey(path, s)}] = append(idx.byCallEdge[edgeKey{1, callerEdgeKey(path, s)}], entry)
				return true
			})
			f = nil
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

// EdgesByCallee returns every call-edge record pointing at a function named
// `name`. The tool layer inverts it into CALLERS edges: each entry is
// "someone called name".
//
// Result is sorted by (file, caller.StartRow) for stable presentation across
// rebuilds. Returns an empty slice when the index is empty (no Load yet) or
// when no caller references that name — the live walker in tool/nodeedges.go
// is the unconditional fallback in that case.
func (r *Repo) EdgesByCallee(name string) []EdgeEntry {
	if name == "" {
		return nil
	}
	r.mu.RLock()
	idx := r.index
	r.mu.RUnlock()
	if idx == nil {
		return nil
	}
	raw := idx.byCallEdge[edgeKey{0, name}]
	out := make([]EdgeEntry, len(raw))
	for i, e := range raw {
		out[i] = e
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Caller.StartRow < out[j].Caller.StartRow
	})
	return out
}

// EdgesByCaller returns every call-edge record emitted by the caller
// identified by `sym` in `file`. The tool layer inverts it into CALLEES
// edges: each entry is "sym called someone".
//
// Same empty-slice contract as EdgesByCallee when the index is empty or the
// caller is unindexed (e.g. the file was edited after Load without
// ReloadInvalidate). The symbol's Receiver is part of the key for methods —
// two same-named methods on different receivers in the same file no longer
// collapse.
func (r *Repo) EdgesByCaller(file string, sym parser.Symbol) []EdgeEntry {
	if file == "" || sym.Name == "" {
		return nil
	}
	r.mu.RLock()
	idx := r.index
	r.mu.RUnlock()
	if idx == nil {
		return nil
	}
	raw := idx.byCallEdge[edgeKey{1, callerEdgeKey(file, sym)}]
	out := make([]EdgeEntry, len(raw))
	for i, e := range raw {
		out[i] = e
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Callee < out[j].Callee
	})
	return out
}

// callerEdgeKey builds the by-call-edge caller-side key for a symbol.
// Methods include the receiver so two same-named methods on different
// receivers in the same file (e.g. Alpha.Ping vs Beta.Ping) get distinct
// entries. Functions use the bare name.
func callerEdgeKey(file string, s parser.Symbol) string {
	if s.Receiver != "" {
		return file + "::" + s.Receiver + "." + s.Name
	}
	return file + "::" + s.Name
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
		// Find a method with the requested name *and* the requested receiver
		// in the class's file. Two types in the same file can each declare a
		// same-named method (e.g. `func (a *Alpha) Ping()` and
		// `func (b *Beta) Ping()`); without the receiver check we'd return
		// whichever the iteration order hit first.
		for _, e := range entries {
			if e.Sym.Kind != "type_declaration" {
				continue
			}
			if _, ferr := r.CachedFile(e.File); ferr != nil {
				continue
			}
			for _, m := range r.Symbols(e.File) {
				if m.Kind == "method_declaration" && m.Name == name && m.Receiver == e.Sym.Name {
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
