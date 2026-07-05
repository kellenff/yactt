package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/id"
	"github.com/kellenff/yactt/internal/lsp"
	"github.com/kellenff/yactt/internal/parser"
	"github.com/kellenff/yactt/internal/store"
)

// NodeEdgesArgs is the typed input for node_edges.
type NodeEdgesArgs struct {
	ID    string   `json:"id"`
	Kinds []string `json:"kinds"`
	Limit int      `json:"limit"`
}

// NodeEdgesResult is one or more typed edges. Confidence is in [0, 1].
type NodeEdgesResult struct {
	EdgeKind      domain.EdgeKind   `json:"edgeKind"`
	TargetID      string            `json:"targetId"`
	TargetKind    domain.NodeKind   `json:"targetKind,omitempty"`
	TargetSummary string            `json:"targetSummary,omitempty"`
	Location      domain.Location   `json:"location"`
	Confidence    float64           `json:"confidence"`
	Provenance    domain.Provenance `json:"provenance"`
}

// NodeEdgesSchema is the JSON Schema for node_edges. Mirrors design §4.4.
var NodeEdgesSchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "id": { "type": "string" },
    "kinds": {
      "type": "array",
      "items": { "enum": ["callers", "callees", "tests", "overrides", "imports"] },
      "default": ["callers", "callees", "tests"]
    },
    "limit": { "type": "integer", "default": 50 }
  },
  "required": ["id"],
  "additionalProperties": false
}`)

// NodeEdges returns a Handler that emits typed cross-references for an ID.
//
// MVP backend: tree-sitter pass for syntactic call detection. Confidence is
// flagged 0.5 per design §4.4 ("syntactic — no cross-file resolution").
// LSP/SCIP resolution would raise confidence to 1.0 once wired.
func NodeEdges(repo *store.Repo) func(ctx context.Context, args json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		var a NodeEdgesArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("invalid node_edges args: %w", err)
		}
		if a.ID == "" {
			return nil, fmt.Errorf("node_edges: id is required")
		}
		nodeID, err := id.Parse(a.ID)
		if err != nil {
			return nil, fmt.Errorf("node_edges: %w", err)
		}
		file, sym, ok, lerr := repo.LocateSymbol(nodeID)
		if lerr != nil || !ok {
			return nil, fmt.Errorf("node_edges: cannot locate %s", a.ID)
		}
		if a.Limit <= 0 {
			a.Limit = 50
		}
		kinds := a.Kinds
		if len(kinds) == 0 {
			kinds = []string{"callers", "callees", "tests"}
		}
		out := []NodeEdgesResult{}
		p := prov()
		for _, k := range kinds {
			switch k {
			case "callees":
				out = append(out, scanCallees(repo, file, sym, a.Limit, p)...)
			case "callers":
				out = append(out, scanCallers(repo, file, sym, a.Limit, p)...)
			case "tests":
				out = append(out, scanTests(repo, file, sym, a.Limit, p)...)
			case "imports":
				out = append(out, scanImports(repo, file, sym, a.Limit, p)...)
			case "overrides":
				out = append(out, scanOverrides(repo, file, sym, a.Limit, p)...)
			}
		}
		return out, nil
	}
}

// scanCallees resolves the requested symbol's callees. Tier 0 is the
// persisted call-edge index built at Load time (see internal/store/edges.go);
// Tier 1 is the live AST walker, used as a fallback when the index has not
// captured this caller — typically a file edit since Load without a matching
// ReloadInvalidate.
//
// Both paths emit the same NodeEdgesResult shape with the same confidence
// (0.5 — name-only resolution is syntactic); the index is purely an
// optimisation.
func scanCallees(repo *store.Repo, file string, sym parser.Symbol, limit int, p *domain.Provenance) []NodeEdgesResult {
	if sym.Name == "" {
		return nil
	}
	if entries := repo.EdgesByCaller(file, sym); len(entries) > 0 {
		return scanCalleesFromIndex(repo, file, sym, limit, p, entries)
	}
	return scanCalleesLive(repo, file, sym, limit, p)
}

// scanCalleesFromIndex renders CALLEES edges from pre-built index entries.
// Each entry is a unique (caller, callee-name) pair — the index has already
// deduped per-caller. We resolve each callee name against the symbol index
// and dedup targets by (package-path, sym-name) so two different lookup
// matches at the same target collapse to one edge.
func scanCalleesFromIndex(repo *store.Repo, file string, sym parser.Symbol, limit int, p *domain.Provenance, entries []store.EdgeEntry) []NodeEdgesResult {
	loc := location(file, sym.StartRow, sym.EndRow)
	out := make([]NodeEdgesResult, 0, len(entries))
	seen := make(map[string]bool)
	for _, e := range entries {
		if len(out) >= limit {
			break
		}
		for _, l := range repo.Lookup("", e.Callee) {
			targetKey := joinDotted(packagePath(repo.Root(), l.File), l.Sym.Name)
			if seen[targetKey] {
				continue
			}
			seen[targetKey] = true
			out = append(out, NodeEdgesResult{
				EdgeKind:      domain.EdgeCallees,
				TargetID:      targetIDForLookup(repo, l),
				TargetKind:    symbolKind(l.Sym),
				TargetSummary: symbolSummary(l.Sym),
				Location:      loc,
				Confidence:    0.5,
				Provenance:    *p,
			})
			if len(out) >= limit {
				break
			}
		}
	}
	return out
}

// scanCalleesLive is the unconditional fallback for scanCallees: walks the
// function body looking for `call_expression` nodes and extracts the called
// identifier. Used when the index has no entry for this caller (file edited
// without ReloadInvalidate since Load).
func scanCalleesLive(repo *store.Repo, file string, sym parser.Symbol, limit int, p *domain.Provenance) []NodeEdgesResult {
	f, err := repo.CachedFile(file)
	if err != nil {
		return nil
	}
	if f.Root == nil {
		return nil
	}
	startByte, endByte := byteRangeFromRows(f.Bytes, sym.StartRow, sym.EndRow)
	loc := location(file, sym.StartRow, sym.EndRow)
	seen := make(map[string]bool)
	out := make([]NodeEdgesResult, 0, 4)
	walkExpr(f.Root, startByte, endByte, func(n *sitter.Node) bool {
		if n.Type() != "call_expression" {
			return true
		}
		fn := n.ChildByFieldName("function")
		if fn == nil {
			if n.ChildCount() > 0 {
				fn = n.Child(0)
			}
		}
		if fn == nil {
			return true
		}
		name := extractCalleeName(fn, f.Bytes)
		if name == "" {
			return true
		}
		entries := repo.Lookup("", name)
		for _, e := range entries {
			targetKey := joinDotted(packagePath(repo.Root(), e.File), e.Sym.Name)
			if seen[targetKey] {
				continue
			}
			seen[targetKey] = true
			out = append(out, NodeEdgesResult{
				EdgeKind:      domain.EdgeCallees,
				TargetID:      targetIDForLookup(repo, e),
				TargetKind:    symbolKind(e.Sym),
				TargetSummary: symbolSummary(e.Sym),
				Location:      loc,
				Confidence:    0.5,
				Provenance:    *p,
			})
		}
		return len(out) < limit
	})
	return out
}

// scanCallers walks every other file's declarations and reports functions
// whose bodies reference this symbol. Tree-sitter pass — confidence 0.5 for
// cross-file syntactic only.
//
// Tier 1 short-circuit: when the repo has a live LSP client, we ask
// `textDocument/references` once at the symbol's name position. The
// returned locations are converted to node IDs via `repo.LocateSymbol`
// (best-effort cross-package) and emitted at confidence 1.0. The
// tree-sitter pass below is the unconditional fallback.
func scanCallers(repo *store.Repo, file string, sym parser.Symbol, limit int, p *domain.Provenance) []NodeEdgesResult {
	if sym.Name == "" {
		return nil
	}
	// Tier 1 attempt. If it produces any callers, return those — the
	// tree-sitter scan on the same set of files would only add noise.
	if client, _, _ := repo.LSPForFile(file); client != nil {
		if col, ok := nameColumnFor(repo, file, sym); ok {
			refs, rerr := client.References(referencesCtx(), file, sym.StartRow, col, true)
			if rerr == nil && len(refs) > 0 {
				out := make([]NodeEdgesResult, 0, len(refs))
				lprov := lspProvenance(repo, file)
				for _, ref := range refs {
					if len(out) >= limit {
						break
					}
					tPath := lsp.PathOf(ref.URI)
					nodeID, ok2 := callerIDAt(tPath, ref.Range.Start.Line, ref.Range.Start.Character, repo)
					if !ok2 {
						continue
					}
					out = append(out, NodeEdgesResult{
						EdgeKind:   domain.EdgeCallers,
						TargetID:   nodeID,
						TargetKind: domain.KindFunction,
						Location:   location(tPath, ref.Range.Start.Line, ref.Range.End.Line),
						Confidence: 1.0,
						Provenance: lprov,
					})
				}
				if len(out) > 0 {
					return out
				}
			} else if rerr != nil {
				// Fall through to tree-sitter; honest provenance
				// belongs on the tree-sitter answer (not on the
				// caller edges we'd then NOT emit).
				_ = lsp.FallbackReason(rerr)
			}
		}
	}

	out := []NodeEdgesResult{}
	for _, otherPath := range repo.Files() {
		if otherPath == file {
			continue
		}
		entries := repo.Symbols(otherPath)
		for _, e := range entries {
			if e.Name == "" || (e.Kind != "function_declaration" && e.Kind != "method_declaration") {
				continue
			}
			f, err := repo.CachedFile(otherPath)
			if err != nil || f.Root == nil {
				continue
			}
			startByte, endByte := byteRangeFromRows(f.Bytes, e.StartRow, e.EndRow)
			found := false
			walkExpr(f.Root, startByte, endByte, func(n *sitter.Node) bool {
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
				if extractCalleeName(fn, f.Bytes) == sym.Name {
					found = true
					return false
				}
				return true
			})
			if found {
				out = append(out, NodeEdgesResult{
					EdgeKind:   domain.EdgeCallers,
					TargetID:   id.For(e, packagePath(repo.Root(), otherPath)),
					TargetKind: symbolKind(e),
					Location:   location(otherPath, e.StartRow, e.EndRow),
					Confidence: 0.5,
					Provenance: *p,
				})
				if len(out) >= limit {
					return out
				}
			}
		}
	}
	return out
}

// referencesCtx returns a context bounded by ~750ms — enough for a warm
// gopls round trip, below the per-tool-call budget (1.5s for node_edges
// per design §5.1). Tests inject shorter contexts via the same options.
func referencesCtx() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	// cancelCtx prevents a goroutine leak on the success path; we
	// explicitly hold a CancelFunc captured into the closure.
	_ = cancel
	return ctx
}

// nameColumnFor returns the byte-column where `sym.Name` lives on its
// declaration row in `file`. Reuses the same logic as
// `store.symbolNameColumn`; we keep a private local copy because the
// internal-package helper isn't exported.
func nameColumnFor(repo *store.Repo, file string, sym parser.Symbol) (int, bool) {
	f, err := repo.CachedFile(file)
	if err != nil || f == nil || f.Root == nil {
		return 0, false
	}
	return storeNameColumn(f.Root, sym.StartRow, sym.Name, f.Bytes)
}

// callerIDAt asks the store to locate the function containing the
// reference at (line, col) in `path`. Returns ("", false) when the
// reference is not inside a function declaration (e.g. a top-level
// constant, a type method body, or something the resolver doesn't index).
func callerIDAt(path string, line, col int, repo *store.Repo) (string, bool) {
	if line < 0 {
		return "", false
	}
	// Resolve the containing function by walking the symbol index.
	// This is the cheapest correct option given the repo's current
	// lookups; it costs at most one full pass over the file's symbols.
	pkg := packagePath(repo.Root(), path)
	entries := repo.Symbols(path)
	for _, e := range entries {
		if e.Kind != "function_declaration" && e.Kind != "method_declaration" {
			continue
		}
		if line >= e.StartRow && line < e.EndRow {
			return id.For(e, pkg), true
		}
	}
	return "", false
}

// lspProvenance returns the canonical LSP provenance for edges emitted via
// the Tier-1 path. Tool name (gopls / typescript-language-server) and
// version are captured at Load.
func lspProvenance(repo *store.Repo, file string) domain.Provenance {
	_, tool, version := repo.LSPForFile(file)
	return domain.LSPProvenance(tool, version)
}

// storeNameColumn is an exported re-export for the in-package symbol
// column helper. We re-implement (small) here to avoid coupling the tool
// package to internal layout of `store`.
func storeNameColumn(root *sitter.Node, row int, name string, src []byte) (int, bool) {
	if root == nil {
		return 0, false
	}
	var stack []*sitter.Node
	stack = append(stack, root)
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n == nil {
			continue
		}
		if int(n.StartPoint().Row) == row {
			switch n.Type() {
			case "identifier", "field_identifier":
				if n.Content(src) == name {
					return int(n.StartPoint().Column), true
				}
			case "function_declaration", "method_declaration", "type_declaration":
				for i := int(n.ChildCount()) - 1; i >= 0; i-- {
					c := n.Child(i)
					if c != nil {
						stack = append(stack, c)
					}
				}
			}
		}
		for i := int(n.ChildCount()) - 1; i >= 0; i-- {
			c := n.Child(i)
			if c != nil {
				stack = append(stack, c)
			}
		}
	}
	return 0, false
}

// scanTests looks for files matching the test convention (`_test.go`,
// `*_test.go`) and emits TESTS edges for functions declared in them. This is
// the design's convention-based discovery; SCIP symbol-table linking is a
// Phase 2 upgrade.
func scanTests(repo *store.Repo, _ string, sym parser.Symbol, limit int, p *domain.Provenance) []NodeEdgesResult {
	if sym.Name == "" {
		return nil
	}
	out := []NodeEdgesResult{}
	for _, path := range repo.Files() {
		base := path
		if i := strings.LastIndexByte(path, '/'); i >= 0 {
			base = path[i+1:]
		}
		isTest := strings.HasSuffix(base, "_test.go")
		if !isTest {
			continue
		}
		for _, s := range repo.Symbols(path) {
			out = append(out, NodeEdgesResult{
				EdgeKind:      domain.EdgeTests,
				TargetID:      id.For(s, packagePath(repo.Root(), path)),
				TargetKind:    symbolKind(s),
				TargetSummary: symbolSummary(s),
				Location:      location(path, s.StartRow, s.EndRow),
				Confidence:    0.7,
				Provenance:    *p,
			})
			if len(out) >= limit {
				return out
			}
		}
	}
	return out
}

// scanImports walks the top level of the node's containing file and
// emits one IMPORTS edge per imported package. Syntactic only — no
// cross-package resolution to local files or `pkg:` indexes, so
// confidence sits at 0.4 (lower than CALLEES's 0.5 because we don't
// even attempt identifier lookup against the symbol index).
//
// The target ID is `pkg:<import-path>` regardless of whether the path
// resolves to anything in the repo — the wire format keeps the path
// string as the stable address. Resolving to local files is a
// Phase 1.5 upgrade (design §5.1 cross-language import graph).
//
// Deduplicates by path within a single file — grouped `import ("a"; "a")`
// in Go or duplicate specifiers in TS produce one edge per unique path.
func scanImports(repo *store.Repo, file string, _ parser.Symbol, limit int, p *domain.Provenance) []NodeEdgesResult {
	f, err := repo.CachedFile(file)
	if err != nil || f.Root == nil {
		return nil
	}
	out := []NodeEdgesResult{}
	seen := make(map[string]bool)
	root := f.Root
	for i := 0; i < int(root.ChildCount()); i++ {
		ch := root.Child(i)
		if ch == nil {
			continue
		}
		if ch.Type() != "import_declaration" && ch.Type() != "import_statement" {
			continue
		}
		path := store.ExtractImportPath(ch, f.Bytes)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, NodeEdgesResult{
			EdgeKind:      domain.EdgeImports,
			TargetID:      "pkg:" + path,
			TargetKind:    domain.KindPackage,
			TargetSummary: path,
			Location:      location(file, int(ch.StartPoint().Row), int(ch.EndPoint().Row)+1),
			Confidence:    0.4,
			Provenance:    *p,
		})
		if len(out) >= limit {
			return out
		}
	}
	return out
}

// scanOverrides walks the node's containing file for a class
// declaration matching sym.Receiver; for each, looks up parent classes
// via the `class_heritage` clause and emits one OVERRIDES edge per
// parent method with the same name as sym.
//
// Languages:
//   - TS/JS: walks `class_declaration` AST and emits one edge per
//     parent method with a matching name. Confidence 0.4 because we
//     don't resolve parent method bodies or types.
//   - Go: no override semantics (no `extends` keyword); emits nil.
//
// Same-file inheritance only — cross-file parent resolution requires
// package-graph support, deferred to Phase 1.5.
func scanOverrides(repo *store.Repo, file string, sym parser.Symbol, limit int, p *domain.Provenance) []NodeEdgesResult {
	if sym.Receiver == "" || sym.Kind != "method_declaration" {
		return nil
	}
	f, err := repo.CachedFile(file)
	if err != nil || f.Root == nil {
		return nil
	}
	pkg := packagePath(repo.Root(), file)
	src := f.Bytes

	// 1. Find the class declaration whose name matches sym.Receiver.
	ownClass := findClassByName(f.Root, sym.Receiver, src)
	if ownClass == nil {
		return nil
	}

	// 2. Walk that class's class_heritage to extract parent class names.
	parents := parentClassNames(ownClass, src)
	if len(parents) == 0 {
		return nil
	}

	// 3. For each parent declared in this file, look for a method with
	//    the same name as sym; emit one OVERRIDES edge per match.
	//    Recurse through `export_statement` wrappers since TS/JS
	//    often wrap classes in `export class ...`.
	out := []NodeEdgesResult{}
	seen := make(map[string]bool)
	var eachClass func(*sitter.Node)
	eachClass = func(n *sitter.Node) {
		if n == nil {
			return
		}
		if n.Type() == "class_declaration" {
			parentName := className(n, src)
			if parentName != "" {
				isParent := false
				for _, p := range parents {
					if p == parentName {
						isParent = true
						break
					}
				}
				if isParent {
					body := n.ChildByFieldName("body")
					if body != nil {
						for j := 0; j < int(body.ChildCount()); j++ {
							m := body.Child(j)
							if m == nil || m.Type() != "method_definition" {
								continue
							}
							mName := methodName(m, src)
							if mName == "" || mName != sym.Name {
								continue
							}
							key := parentName + "." + mName
							if seen[key] {
								continue
							}
							seen[key] = true
							out = append(out, NodeEdgesResult{
								EdgeKind:      domain.EdgeOverrides,
								TargetID:      id.Method(pkg, parentName, mName).String(),
								TargetKind:    domain.KindMethod,
								TargetSummary: mName,
								Location:      location(file, int(m.StartPoint().Row), int(m.EndPoint().Row)+1),
								Confidence:    0.4,
								Provenance:    *p,
							})
							if len(out) >= limit {
								return
							}
						}
					}
				}
			}
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			eachClass(n.Child(i))
		}
	}
	eachClass(f.Root)
	return out
}

// findClassByName walks the AST and returns the first class_declaration
// whose name child matches want. Used by scanOverrides to locate the
// node's containing class. Tree-sitter TS/JS both expose class names as
// `identifier` or `type_identifier` children.
func findClassByName(root *sitter.Node, want string, src []byte) *sitter.Node {
	var found *sitter.Node
	var walk func(*sitter.Node) bool
	walk = func(n *sitter.Node) bool {
		if n == nil {
			return false
		}
		if n.Type() == "class_declaration" && className(n, src) == want {
			found = n
			return true
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			if walk(n.Child(i)) {
				return true
			}
		}
		return false
	}
	walk(root)
	return found
}

// className returns the name of a class_declaration. Tree-sitter TS/JS
// place the name as the first identifier-like child of the declaration.
func className(class *sitter.Node, src []byte) string {
	for i := 0; i < int(class.ChildCount()); i++ {
		ch := class.Child(i)
		if ch == nil {
			continue
		}
		if ch.Type() == "identifier" || ch.Type() == "type_identifier" {
			return ch.Content(src)
		}
	}
	return ""
}

// parentClassNames returns the class names listed in a
// class_declaration's `class_heritage` clause (TS/JS `extends X`).
// Empty when the class has no parent. Tree-sitter TS wraps `extends`
// and the parent identifier inside an `extends_clause` child, so we
// recursively descend into that as well.
func parentClassNames(class *sitter.Node, src []byte) []string {
	var parents []string
	var walk func(*sitter.Node) bool
	walk = func(n *sitter.Node) bool {
		if n == nil {
			return false
		}
		switch n.Type() {
		case "class_heritage", "extends_clause":
			// Search inside clause for the parent identifier.
			for i := 0; i < int(n.ChildCount()); i++ {
				walk(n.Child(i))
			}
		case "identifier", "type_identifier":
			parents = append(parents, n.Content(src))
		}
		return false
	}
	for i := 0; i < int(class.ChildCount()); i++ {
		ch := class.Child(i)
		if ch != nil && ch.Type() == "class_heritage" {
			walk(ch)
		}
	}
	return parents
}

// methodName returns the name of a method_definition. Tree-sitter
// TS/JS place it as a `property_identifier` child of the method.
func methodName(method *sitter.Node, src []byte) string {
	for i := 0; i < int(method.ChildCount()); i++ {
		ch := method.Child(i)
		if ch != nil && ch.Type() == "property_identifier" {
			return ch.Content(src)
		}
	}
	return ""
}

// walkExpr walks a tree, calling visit on every node until visit returns
// false. The byte range filter restricts the walk to the symbol's body.
//
// Thin wrapper over store.WalkExpr so the persisted call-edge index and the
// live AST walker share one implementation. Kept package-local so existing
// test callers (nodeedges_test.go) don't have to switch call sites.
func walkExpr(n *sitter.Node, startByte, endByte int, visit func(*sitter.Node) bool) {
	store.WalkExpr(n, startByte, endByte, visit)
}

// extractCalleeName handles `Foo()` and `pkg.Foo()` syntax. For Go the rightmost
// identifier in a selector_expression is the called name.
//
// Thin wrapper over store.ExtractCalleeName — same single-source-of-truth
// rationale as walkExpr.
func extractCalleeName(n *sitter.Node, src []byte) string {
	return store.ExtractCalleeName(n, src)
}

// byteRangeFromRows converts a (startRow, endRow) pair into an approximate
// byte range over `src`.
//
// Thin wrapper over store.ByteRangeFromRows.
func byteRangeFromRows(src []byte, startRow, endRow int) (int, int) {
	return store.ByteRangeFromRows(src, startRow, endRow)
}

// location builds a domain.Location for an edge.
func location(file string, start, end int) domain.Location {
	return domain.Location{
		File: file,
		LineRange: domain.LineRange{
			Start: start,
			End:   end,
		},
	}
}

// targetIDForLookup renders the canonical node ID for a symbol lookup.
func targetIDForLookup(repo *store.Repo, e store.SymbolLookup) string {
	return symbolID(e.File, e.Sym, repo.Root())
}
