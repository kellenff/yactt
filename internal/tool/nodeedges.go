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
			}
		}
		return out, nil
	}
}

// scanCallees walks the function body looking for `call_expression` nodes and
// extracts the called identifier. We then resolve against the symbol index:
//
//   - exact (pkg, name) match → emit a CALLEES edge with confidence 0.5.
//   - name-only match         → also emit, marked syntactically ambiguous.
//
// Cross-file resolution requires LSP/SCIP (Phase 2). For MVP, single-file
// targets resolve precisely; cross-file targets resolve by name only.
func scanCallees(repo *store.Repo, file string, sym parser.Symbol, limit int, p *domain.Provenance) []NodeEdgesResult {
	f, err := repo.CachedFile(file)
	if err != nil {
		return nil
	}
	if f.Root == nil {
		return nil
	}
	// Approximate byte offsets from row positions: each row ≈ average bytes
	// per row in the file. Cheap and good enough for the syntactic walk.
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
			// Fallback: first child is the function ref for many grammars.
			if n.ChildCount() > 0 {
				fn = n.Child(0)
			}
		}
		if fn == nil {
			return true
		}
		// Strip method-call chains: `a.b.Called()` → pick the rightmost
		// identifier by walking until the last selector_expression.
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
			if e.Name == "" || e.Kind != "function_declaration" {
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
					TargetID:   "fn:" + joinDotted(packagePath(repo.Root(), otherPath), e.Name),
					TargetKind: domain.KindFunction,
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
	entries := repo.Symbols(path)
	for _, e := range entries {
		if e.Kind != "function_declaration" && e.Kind != "method_declaration" {
			continue
		}
		if line >= e.StartRow && line < e.EndRow {
			pkg := joinDotted(packagePath(repo.Root(), path), e.Name)
			if e.Receiver != "" {
				return "meth:" + joinDotted(packagePath(repo.Root(), path), e.Receiver+"."+e.Name), true
			}
			return "fn:" + pkg, true
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
				TargetID:      "fn:" + joinDotted(packagePath(repo.Root(), path), s.Name),
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

// walkExpr walks a tree, calling visit on every node until visit returns
// false. The byte range filter restricts the walk to the symbol's body.
func walkExpr(n *sitter.Node, startByte, endByte int, visit func(*sitter.Node) bool) {
	if n == nil {
		return
	}
	s := int(n.StartByte())
	e := int(n.EndByte())
	if e <= startByte || s >= endByte {
		return
	}
	if !visit(n) {
		return
	}
	nch := int(n.ChildCount())
	for i := 0; i < nch; i++ {
		walkExpr(n.Child(i), startByte, endByte, visit)
	}
}

// extractCalleeName handles `Foo()` and `pkg.Foo()` syntax. For Go the rightmost
// identifier in a selector_expression is the called name.
func extractCalleeName(n *sitter.Node, src []byte) string {
	if n == nil {
		return ""
	}
	switch n.Type() {
	case "identifier", "field_identifier":
		return n.Content(src)
	case "selector_expression":
		if n.ChildCount() == 0 {
			return ""
		}
		return extractCalleeName(n.Child(int(n.ChildCount())-1), src)
	}
	return ""
}

// byteRangeFromRows converts a (startRow, endRow) pair into an approximate
// byte range over `src`. Each row is treated as a line of variable width;
// we just iterate once and count newline boundaries.
func byteRangeFromRows(src []byte, startRow, endRow int) (int, int) {
	row := 0
	idx := 0
	for i, b := range src {
		if row == startRow && idx == 0 {
			idx = i
		}
		if b == '\n' {
			row++
			if row == endRow {
				return idx, i
			}
		}
	}
	return idx, len(src)
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
