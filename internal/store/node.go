package store

import (
	"errors"
	"strings"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/id"
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

	out := &domain.Node{ID: nodeID.String(), Name: sym.Name}
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

	// Pull the doc comment so we can build the summary layer.
	doc := extractDocComment(f, sym)
	if layerSet[domain.LayerSummary] {
		firstLine := ""
		if sym.StartRow < sym.EndRow {
			line, err := f.Slice(domain.LineRange{Start: sym.StartRow, End: sym.StartRow + 1})
			if err == nil {
				firstLine = line
			}
		}
		text, prov := SummaryMaterializer(out.Kind, sym.Name, doc, firstLine)
		out.Summary = text
		out.SummaryProvenance = prov
	}
	if layerSet[domain.LayerSignature] {
		text, prov := SignatureMaterializer(f, sym)
		out.Signature = &domain.Signature{Text: text, Docs: doc, Provenance: *prov}
	}
	if layerSet[domain.LayerBody] {
		stmts, types, ctrl, prov := BodyMaterializer(f, sym)
		out.Body = &domain.FunctionBody{Stmts: stmts, Types: types, ControlFlow: ctrl, Provenance: *prov}
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
	return out, nil
}

// SignatureMaterializer extracts the signature line for a symbol: the
// declaration line, plus the parameter list, plus the result. This is the
// tree-sitter-only answer — LSP would supply a typed signature with hover.
func SignatureMaterializer(f *source.File, sym parser.Symbol) (string, *domain.Provenance) {
	if sym.EndRow <= sym.StartRow {
		return "", prov()
	}
	// One-line signatures: just the first line of the declaration.
	if sym.EndRow == sym.StartRow+1 {
		text, err := f.Slice(domain.LineRange{Start: sym.StartRow, End: sym.EndRow})
		if err == nil {
			text = strings.TrimRight(text, "\n")
			return text, prov()
		}
	}
	// Multi-line: take the first line plus every line containing an
	// opening/closing paren, brace, or angle bracket — a coarse approximation
	// of the full signature that tree-sitter can produce without a typed
	// resolver.
	body, err := f.Slice(domain.LineRange{Start: sym.StartRow, End: sym.EndRow})
	if err != nil {
		return "", prov()
	}
	return strings.TrimRight(body, "\n"), prov()
}

// BodyMaterializer extracts a coarse statement list, types map, and a
// control-flow classification. Tree-sitter can give us a syntactic stmts-only
// view; we leave Types empty and label control-flow as "linear" until LSP/SCIP
// raise confidence.
func BodyMaterializer(f *source.File, sym parser.Symbol) ([]domain.Stmt, map[string]any, string, *domain.Provenance) {
	stmts := []domain.Stmt{}
	if sym.StartRow >= sym.EndRow {
		return stmts, map[string]any{}, "linear", prov()
	}
	lines, err := f.Lines()
	if err != nil {
		return stmts, map[string]any{}, "linear", prov()
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
	return stmts, map[string]any{}, control, prov()
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
