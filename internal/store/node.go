package store

import (
	"context"
	"errors"
	"strings"
	"time"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/id"
	"github.com/kellenff/yactt/internal/lsp"
	"github.com/kellenff/yactt/internal/parser"
	"github.com/kellenff/yactt/internal/source"
)

// ErrCannotResolve is returned by MaterializeNode when the ID is well-formed
// but the symbol cannot be located in the repo (e.g. cross-package import).
var ErrCannotResolve = errors.New("store: cannot resolve node id in repo")

// BodyFor returns just the body layer for a node ID, suitable for find_symbol
// when include_body=true. Cheaper than MaterializeNode when only the body is
// needed.
func BodyFor(r *Repo, nodeIDString string) (*domain.FunctionBody, error) {
	parsed, err := id.Parse(nodeIDString)
	if err != nil {
		return nil, err
	}
	n, err := MaterializeNode(r, parsed, map[domain.LayerName]bool{domain.LayerBody: true})
	if err != nil {
		return nil, err
	}
	if n.Body == nil {
		return &domain.FunctionBody{Types: map[string]any{}, ControlFlow: "linear", Provenance: *prov()}, nil
	}
	return n.Body, nil
}

// QuickNode returns a summary-only node — cheap, no LSP-roundtrip equivalent.
// Used by find_symbol to populate the per-match Node view.
func QuickNode(r *Repo, nodeIDString string) (*domain.Node, error) {
	parsed, err := id.Parse(nodeIDString)
	if err != nil {
		return nil, err
	}
	return MaterializeNode(r, parsed, map[domain.LayerName]bool{domain.LayerSummary: true})
}

// prov is the per-layer provenance line. Single source of truth lives in
// domain; this is a thin alias for store call sites.
func prov() *domain.Provenance { return domain.TreeSitterProvenancePtr() }

// MaterializeNode resolves a node ID and fills the requested layers.
//
// layerSet is the set of layer names to populate; missing layers stay nil so
// callers can distinguish "fetched and empty" from "not requested".
//
// The function is the main entry point for `node_get`. Each layer is computed
// independently using the parsed file + symbol metadata; provenance is set to
// `tree-sitter` for syntactic layers and `summarizer` for the summary layer
// (per design §5.5 — tree-sitter owns everything that's syntactic, summarizer
// owns first-line-of-doc-comment; LSP/SCIP would replace these later in
// the fallback chain).
func MaterializeNode(r *Repo, nodeID id.ID, layerSet map[domain.LayerName]bool) (*domain.Node, error) {
	filePath, sym, ok, err := r.LocateSymbol(nodeID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrCannotResolve
	}
	f, ferr := r.CachedFile(filePath)
	if ferr != nil {
		return nil, ferr
	}

	out := &domain.Node{ID: nodeID.String(), Name: domain.SanitizeName(sym.Name)}
	switch nodeID.Kind {
	case id.KindFile:
		out.Kind = domain.KindFile
	case id.KindFunction:
		out.Kind = domain.KindFunction
	case id.KindMethod:
		out.Kind = domain.KindMethod
	case id.KindClass, id.KindModule:
		out.Kind = domain.KindClass
		if nodeID.Kind == id.KindModule {
			out.Kind = domain.KindModule
		}
	default:
		out.Kind = domain.KindPackage
	}

	// Pull the doc comment once. It is ATTACKER-AUTHORED prose — only ever
	// surfaced through the opt-in LayerDocs gate (Node.Docs and, when
	// LayerDocs is requested, Signature.Docs). The summary layer must
	// NEVER see it. See docs/security.md §5 (AST05).
	doc := extractDocComment(f, sym)
	if layerSet[domain.LayerSummary] {
		firstLine := ""
		if sym.StartRow < sym.EndRow {
			line, err := f.Slice(domain.LineRange{Start: sym.StartRow, End: sym.StartRow + 1})
			if err == nil {
				firstLine = line
			}
		}
		text, prov := SummaryMaterializer(out.Kind, out.Name, "", firstLine)
		out.Summary = text
		out.SummaryProvenance = prov
	}
	if layerSet[domain.LayerSignature] {
		text, types, prov := r.signatureMaterializer(f, sym)
		sigDocs := ""
		if layerSet[domain.LayerDocs] {
			sigDocs = doc
		}
		sig := &domain.Signature{Text: text, Docs: sigDocs, Provenance: *prov}
		if len(types) > 0 {
			sig.Types = types
		}
		out.Signature = sig
	}
	if layerSet[domain.LayerBody] {
		stmts, types, ctrl, prov := r.bodyMaterializer(f, sym)
		body := &domain.FunctionBody{Stmts: stmts, ControlFlow: ctrl, Provenance: *prov}
		if len(types) > 0 {
			body.Types = types
		}
		out.Body = body
	}
	if layerSet[domain.LayerSource] {
		text, prov := SourceMaterializer(f, sym)
		out.Source = &domain.Source{
			Text:       text,
			LineRange:  domain.LineRange{Start: sym.StartRow, End: sym.EndRow},
			Encoding:   "utf-8",
			Provenance: *prov,
		}
	}
	if layerSet[domain.LayerTokens] {
		toks, err := f.Tokens(domain.LineRange{Start: sym.StartRow, End: sym.EndRow})
		if err == nil {
			out.Tokens = toks
		}
	}
	if layerSet[domain.LayerDocs] {
		out.Docs = doc
		out.DocsProvenance = domain.NewProvenance("tree-sitter", "v0.0.0-20240827").Ptr()
	}
	return out, nil
}

// signatureMaterializer is the Tier-1-or-tree-sitter signature path.
//
// When an LSP client is wired for the file's language, we ask its
// `textDocument/hover` for the typed answer and stamp provenance
// `Tool: <server-name>` (gopls for Go, typescript-language-server for
// TS/JS). The Tier-1 stamp requires a *parseable* hover: when the value
// carries no recognisable function-signature shape, we treat the server's
// answer as prose (gopls sometimes returns documentation-style text with
// no `func ... (...)` substring) and fall through to tree-sitter with
// `FallbackUsed: "lsp-no-types"`. Honest provenance, not "answered by
// gopls" on a tree-sitter value.
//
// Returns the signature text, an optional parameter/result-types map (LSP
// only; nil otherwise), and the provenance line.
func (r *Repo) signatureMaterializer(f *source.File, sym parser.Symbol) (string, map[string]any, *domain.Provenance) {
	client, tool, version := r.LSPForFile(f.Path)
	if client != nil {
		col, ok := symbolNameColumn(f, sym)
		if ok {
			ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
			defer cancel()
			h, herr := client.Hover(ctx, f.Path, sym.StartRow, col)
			if herr == nil && h.Contents.Value != "" {
				// Only commit to the LSP provenance line when the hover
				// value parses into a typed signature. A non-empty reply
				// with no `(...)` substring is prose (gopls returns
				// docstring-style text when it has no type info to
				// surface) — fall back rather than stamping gopls on a
				// tier-2 answer.
				if types := parseHoverTypes(h.Contents.Value); types != nil {
					return strings.TrimRight(h.Contents.Value, "\n"), types, domain.LSPProvenance(tool, version).Ptr()
				}
				text, _ := treeSitterSignature(f, sym)
				return text, nil, domain.TreeSitterProvenance().WithFallback("lsp-no-types").Ptr()
			} else if herr != nil {
				// Honest about the failure: tree-sitter fallback
				// with the LSP fallback marker stamped on.
				tsText, _ := treeSitterSignature(f, sym)
				reason := lsp.FallbackReason(herr)
				return tsText, nil, domain.TreeSitterProvenance().WithFallback(reason).Ptr()
			}
		}
	}
	text, prov := treeSitterSignature(f, sym)
	return text, nil, prov
}

// bodyMaterializer is the Tier-1-or-tree-sitter body path. Mirrors
// `signatureMaterializer` for the body layer; calls hover for the enclosing
// function and, when LSP returns a *parseable typed* answer, populates
// `Body.Types` AND stamps gopls provenance. Without parseable types the body
// itself is a tree-sitter slice; lying with "answered by gopls" provenance
// is the tier-1 regression — fall through and stamp tree-sitter with
// `FallbackUsed: "lsp-no-types"` instead.
//
// Call-site ref resolution is deferred to `scanCallers`/`scanCallees` in
// the tool layer.
func (r *Repo) bodyMaterializer(f *source.File, sym parser.Symbol) ([]domain.Stmt, map[string]any, string, *domain.Provenance) {
	client, tool, version := r.LSPForFile(f.Path)
	if client != nil {
		col, ok := symbolNameColumn(f, sym)
		if ok {
			ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
			defer cancel()
			h, herr := client.Hover(ctx, f.Path, sym.StartRow, col)
			if herr == nil && h.Contents.Value != "" {
				if types := parseHoverTypes(h.Contents.Value); types != nil {
					stmts, ctrl, _ := treeSitterStmts(f, sym)
					return stmts, types, ctrl, domain.LSPProvenance(tool, version).Ptr()
				}
				// Hover returned prose (no parseable signature). Body
				// comes from tree-sitter; provenance must agree.
				stmts, ctrl, prov := treeSitterStmtsWithProv(f, sym)
				return stmts, map[string]any{}, ctrl, prov.WithFallback("lsp-no-types").Ptr()
			}
		}
	}
	stmts, ctrl, prov := treeSitterStmtsWithProv(f, sym)
	return stmts, map[string]any{}, ctrl, prov
}

// treeSitterSignature is the fallback Tier-2 signature: line-slice from
// StartRow to EndRow with optional paren/brace line augmentation.
func treeSitterSignature(f *source.File, sym parser.Symbol) (string, *domain.Provenance) {
	if sym.EndRow <= sym.StartRow {
		return "", prov()
	}
	body, err := f.Slice(domain.LineRange{Start: sym.StartRow, End: sym.EndRow})
	if err != nil {
		return "", prov()
	}
	return strings.TrimRight(body, "\n"), prov()
}

// treeSitterStmts produces the coarse statement view shared by Tier 2
// body and the LSP-success-but-no-types path.
func treeSitterStmts(f *source.File, sym parser.Symbol) ([]domain.Stmt, string, bool) {
	stmts := []domain.Stmt{}
	if sym.StartRow >= sym.EndRow {
		return stmts, "linear", false
	}
	lines, err := f.Lines()
	if err != nil {
		return stmts, "linear", false
	}
	start, end := sym.StartRow, sym.EndRow
	if start >= len(lines) {
		start = len(lines)
	}
	if end > len(lines) {
		end = len(lines)
	}
	control := "linear"
	branchRe := false
	for _, raw := range lines[start:end] {
		trim := strings.TrimSpace(stripComments(string(raw)))
		if trim == "" {
			continue
		}
		stmts = append(stmts, domain.Stmt{Kind: stmtKind(trim), Text: trim})
		if !branchRe && (strings.Contains(trim, "if ") || strings.HasPrefix(trim, "switch ") || strings.HasPrefix(trim, "for ") || strings.HasPrefix(trim, "select ")) {
			branchRe = true
		}
	}
	if branchRe {
		control = "branching"
	}
	return stmts, control, true
}

// treeSitterStmtsWithProv is `treeSitterStmts` plus the canonical
// tree-sitter provenance line. Kept as a separate small helper so callers
// in the LSP path can stay allocation-free on the success branch.
func treeSitterStmtsWithProv(f *source.File, sym parser.Symbol) ([]domain.Stmt, string, *domain.Provenance) {
	stmts, ctrl, _ := treeSitterStmts(f, sym)
	return stmts, ctrl, prov()
}

// symbolNameColumn walks the parsed tree and returns the column where the
// symbol's name starts (0-based; LSP convention). Returns ok=false when
// the declaration row has no name (e.g. package-level blank declarations).
//
// We re-walk at hover time instead of storing the column on
// parser.Symbol to keep the symbol type language-agnostic.
func symbolNameColumn(f *source.File, sym parser.Symbol) (int, bool) {
	if f == nil || f.Root == nil {
		return 0, false
	}
	return findNameColumn(f.Root, sym.StartRow, sym.Name, f.Bytes)
}

// findNameColumn walks the tree breadth-first looking for a node whose
// start row matches `row`, whose content equals `name` (or matches the
// function-declaration's `identifier`/`field_identifier` child), and
// returns its column.
func findNameColumn(root *sitter.Node, row int, name string, src []byte) (int, bool) {
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
			// Match: either the node IS the name (identifier / field_identifier)
			// or the node is the declaration whose first identifier child is the
			// name. Both cases pick the leftmost matching identifier.
			switch n.Type() {
			case "identifier", "field_identifier":
				if n.Content(src) == name {
					return int(n.StartPoint().Column), true
				}
			case "function_declaration", "method_declaration", "type_declaration":
				// Drill into children looking for the name.
				for i := int(n.ChildCount()) - 1; i >= 0; i-- {
					c := n.Child(i)
					if c != nil {
						stack = append(stack, c)
					}
				}
			}
		}
		// Continue descending into named children regardless of row match —
		// the declaration spans multiple rows, the name is on StartRow.
		for i := int(n.ChildCount()) - 1; i >= 0; i-- {
			c := n.Child(i)
			if c != nil {
				stack = append(stack, c)
			}
		}
	}
	return 0, false
}

// parseHoverTypes extracts a tentative parameter/result type map from a
// hover string. gopls typically returns either a single-markup-content
// string ("func f(x int, y string) error") or a `{kind: markdown, value:
// …}` payload; we just parse the trailing `(...) (...)` and `... var`
// shapes. Returns nil for hovers with no recognizable type suffix; the
// caller treats that as "no types to populate".
//
// This is a Tier-1 best-effort — accurate typed answers arrive with
// Definition/TypeDefinition wired in Tier 1.5.
func parseHoverTypes(hover string) map[string]any {
	out := map[string]any{}
	if hover == "" {
		return nil
	}
	// Heuristic: split on the first "(" and grab everything from there.
	i := strings.Index(hover, "(")
	if i < 0 {
		return nil
	}
	close := strings.Index(hover[i:], ")")
	if close < 0 {
		return nil
	}
	params := hover[i+1 : i+close]
	if params != "" {
		out["params"] = params
	}
	rest := strings.TrimSpace(hover[i+close+1:])
	if rest != "" {
		out["result"] = rest
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// SourceMaterializer returns the lossless byte slice for a symbol, or an
// empty string if the slice is unavailable.
func SourceMaterializer(f *source.File, sym parser.Symbol) (string, *domain.Provenance) {
	if sym.StartRow >= sym.EndRow {
		return "", prov()
	}
	body, err := f.Slice(domain.LineRange{Start: sym.StartRow, End: sym.EndRow})
	if err != nil {
		return "", prov()
	}
	return body, prov()
}

func stmtKind(line string) string {
	switch {
	case strings.HasPrefix(line, "if "):
		return "if"
	case strings.HasPrefix(line, "for "):
		return "for"
	case strings.HasPrefix(line, "switch "):
		return "switch"
	case strings.HasPrefix(line, "return "):
		return "return"
	case strings.HasPrefix(line, "return"):
		return "return"
	case strings.HasPrefix(line, "go "):
		return "go"
	case strings.HasPrefix(line, "defer "):
		return "defer"
	case strings.HasPrefix(line, "var "):
		return "decl"
	case strings.HasPrefix(line, "const "):
		return "decl"
	}
	if strings.HasSuffix(line, "{") || strings.HasSuffix(line, "}") {
		return "block"
	}
	return "expr"
}

// stripComments removes inline `//` comments and block comment tails.
// Lightweight — designed for body summarisation, not source fidelity.
func stripComments(line string) string {
	if i := strings.Index(line, "//"); i >= 0 {
		line = line[:i]
	}
	if i := strings.Index(line, "/*"); i >= 0 {
		if j := strings.Index(line[i:], "*/"); j >= 0 {
			line = line[:i] + line[i+j+2:]
		}
	}
	return line
}

// extractDocComment peels a contiguous `//` comment block off the lines above
// the declaration row. A blank line breaks the block.
func extractDocComment(f *source.File, sym parser.Symbol) string {
	if sym.StartRow <= 0 {
		return ""
	}
	lines, err := f.Lines()
	if err != nil {
		return ""
	}
	var b strings.Builder
	for i := sym.StartRow - 1; i >= 0; i-- {
		trim := strings.TrimSpace(string(lines[i]))
		if trim == "" {
			if b.Len() > 0 {
				break
			}
			continue
		}
		if !strings.HasPrefix(trim, "//") {
			break
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(strings.TrimPrefix(trim, "//"))
	}
	// We collected top-down; reverse to canonical doc-comment order.
	out := strings.Split(b.String(), "\n")
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
