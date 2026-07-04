// Package domain holds the pure value types that the rest of the program talks about.
//
// Per the parser-not-validate guideline, every layer takes already-validated
// values at its boundary and writes against precise types. These structs are the
// vocabulary; every other package depends on this one and not the other way around.
package domain

import "time"

// NodeKind is the taxonomy of tree nodes. Mirror of the GraphQL enum in design §2.1.
type NodeKind string

const (
	KindRepo     NodeKind = "REPO"
	KindPackage  NodeKind = "PACKAGE"
	KindFile     NodeKind = "FILE"
	KindFunction NodeKind = "FUNCTION"
	KindMethod   NodeKind = "METHOD"
	KindClass    NodeKind = "CLASS"
	KindModule   NodeKind = "MODULE"
)

// IsCode reports whether the kind is a code symbol (function/method/class/module).
func (k NodeKind) IsCode() bool {
	switch k {
	case KindFunction, KindMethod, KindClass, KindModule:
		return true
	}
	return false
}

// EdgeKind is the taxonomy of cross-references between nodes. Mirror of GraphQL enum.
type EdgeKind string

const (
	EdgeCallers   EdgeKind = "CALLERS"
	EdgeCallees   EdgeKind = "CALLEES"
	EdgeTests     EdgeKind = "TESTS"
	EdgeOverrides EdgeKind = "OVERRIDES"
	EdgeImports   EdgeKind = "IMPORTS"
	EdgeTestsOf   EdgeKind = "TESTS_OF"
)

// AllEdges is the default edge set returned when callers do not specify.
var AllEdges = []EdgeKind{EdgeCallers, EdgeCallees, EdgeTests}

// LayerName names a single piece of information owned by a single tool.
type LayerName string

const (
	LayerSummary   LayerName = "summary"
	LayerSignature LayerName = "signature"
	LayerBody      LayerName = "body"
	LayerSource    LayerName = "source"
	LayerTokens    LayerName = "tokens"
)

// AllLayerNames lists the layer set in the order they appear in the design.
var AllLayerNames = []LayerName{LayerSummary, LayerSignature, LayerBody, LayerSource, LayerTokens}

// LineRange is the inclusive-exclusive line range [Start, End) of a source slice.
type LineRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// Contains reports whether an integer line falls inside the range.
func (r LineRange) Contains(line int) bool { return line >= r.Start && line < r.End }

// Length returns the number of lines in the range.
func (r LineRange) Length() int {
	if r.End <= r.Start {
		return 0
	}
	return r.End - r.Start
}

// Location is a file-anchored line range for an edge or token.
type Location struct {
	File      string    `json:"file"`
	LineRange LineRange `json:"range"`
}

// Provenance identifies which tool answered a particular layer, at which version, when.
//
// Per design §5.4 every layer must carry provenance — consumers need to know whether
// `body` came from a fully-resolved LSP/SCIP backend or a syntactic tree-sitter pass so
// they can reason about staleness and trust.
type Provenance struct {
	Tool         string `json:"tool"`
	Version      string `json:"version"`
	FetchedAt    string `json:"fetchedAt"`
	FallbackUsed string `json:"fallbackUsed,omitempty"`
}

// NewProvenance is a small constructor: callers always pass tool + version.
func NewProvenance(tool, version string) Provenance {
	return Provenance{Tool: tool, Version: version, FetchedAt: time.Now().UTC().Format(time.RFC3339)}
}

// WithFallback records the reason the primary tool was bypassed (e.g. "lsp-unavailable").
func (p Provenance) WithFallback(reason string) Provenance {
	p.FallbackUsed = reason
	return p
}

// Ptr returns a pointer to p. Used at call sites that need a *Provenance
// (e.g., pointers in result structs) while keeping the canonical value type.
func (p Provenance) Ptr() *Provenance { return &p }

// TreeSitterProvenance returns the standard tree-sitter provenance for layers
// answered by the syntactic pass. The version pin matches the bundled grammar.
func TreeSitterProvenance() Provenance {
	return NewProvenance("tree-sitter", "v0.0.0-20240827")
}

// TreeSitterProvenancePtr is the per-layer form: tree-sitter owned, with the
// fallback marker stamped on so callers see we have no LSP/SCIP in MVP.
func TreeSitterProvenancePtr() *Provenance {
	return TreeSitterProvenance().WithFallback("no-lsp-installed").Ptr()
}

// Signature is the typed signature layer.
type Signature struct {
	Text       string         `json:"text"`
	Docs       string         `json:"docs,omitempty"`
	Types      map[string]any `json:"types,omitempty"`
	Provenance Provenance     `json:"provenance"`
}

// Source is the lossless source layer — comments, whitespace, encoding intact.
type Source struct {
	Text       string     `json:"text"`
	LineRange  LineRange  `json:"lines"`
	Encoding   string     `json:"encoding"`
	Provenance Provenance `json:"provenance"`
}

// Token is one element of the CST tokens layer.
type Token struct {
	Kind      string    `json:"kind"`
	Value     string    `json:"value"`
	LineRange LineRange `json:"range"`
}

// Stmt is one statement in a parsed function body.
type Stmt struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
	Refs []Ref  `json:"refs,omitempty"`
}

// Ref is a call-site reference from a statement to another node.
type Ref struct {
	CalleeID   string    `json:"calleeId"`
	LineRange  LineRange `json:"range"`
	Confidence float64   `json:"confidence"` // 1.0 = resolved, 0.5 = syntactic
}

// FunctionBody is the typed body layer.
type FunctionBody struct {
	Stmts       []Stmt         `json:"stmts"`
	Types       map[string]any `json:"types"`
	ControlFlow string         `json:"controlFlow"` // "linear" | "branching" | "loop"
	Provenance  Provenance     `json:"provenance"`
}

// Edge is a typed cross-reference between two nodes.
type Edge struct {
	Kind          EdgeKind   `json:"kind"`
	TargetID      string     `json:"targetId"`
	TargetKind    NodeKind   `json:"targetKind,omitempty"`
	TargetSummary string     `json:"targetSummary,omitempty"`
	Location      Location   `json:"location"`
	Confidence    float64    `json:"confidence"`
	Provenance    Provenance `json:"provenance"`
}

// Symbol is the structural entry of a file or scope.
type Symbol struct {
	ID            string   `json:"id"`
	Kind          NodeKind `json:"kind"`
	Name          string   `json:"name"`
	QualifiedName string   `json:"qualifiedName,omitempty"`
	Summary       string   `json:"summary"`
	Children      []Symbol `json:"children,omitempty"`
}

// Node is the canonical aggregation of layers for a single identifier.
//
// Layers are pointers (nullable in JSON) so that "missing layer" is the default,
// matching the design's view that layers are independently fetchable and cached.
type Node struct {
	ID                string        `json:"id"`
	Kind              NodeKind      `json:"kind"`
	Name              string        `json:"name,omitempty"`
	Summary           string        `json:"summary,omitempty"`
	SummaryProvenance *Provenance   `json:"summaryProvenance,omitempty"`
	Signature         *Signature    `json:"signature,omitempty"`
	Body              *FunctionBody `json:"body,omitempty"`
	Source            *Source       `json:"source,omitempty"`
	Tokens            []Token       `json:"tokens,omitempty"`
}

// Repo is a fully-loaded repository view derived from a root path.
type Repo struct {
	Path        string     `json:"path"`
	Language    string     `json:"language"`
	RootPackage string     `json:"rootPackage,omitempty"`
	Provenance  Provenance `json:"provenance"`
}
