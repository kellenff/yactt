package store_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/id"
	"github.com/kellenff/yactt/internal/store"
)

func TestMaterializeNodeSummaryOnly(t *testing.T) {
	r, _ := loadFixture(t)
	nodeID := id.Function("auth", "", "Login")
	n, err := store.MaterializeNode(r, nodeID, map[domain.LayerName]bool{domain.LayerSummary: true})
	if err != nil {
		t.Fatalf("MaterializeNode: %v", err)
	}
	if n.Summary == "" {
		t.Error("Summary should be populated")
	}
	if n.Signature != nil {
		t.Error("Signature should be nil for summary-only")
	}
	if n.Body != nil {
		t.Error("Body should be nil for summary-only")
	}
	if n.Source != nil {
		t.Error("Source should be nil for summary-only")
	}
	if len(n.Tokens) != 0 {
		t.Error("Tokens should be empty for summary-only")
	}
}

func TestMaterializeNodeAllLayers(t *testing.T) {
	r, _ := loadFixture(t)
	nodeID := id.Function("auth", "", "Login")
	layers := map[domain.LayerName]bool{
		domain.LayerSummary:   true,
		domain.LayerSignature: true,
		domain.LayerBody:      true,
		domain.LayerSource:    true,
		domain.LayerTokens:    true,
	}
	n, err := store.MaterializeNode(r, nodeID, layers)
	if err != nil {
		t.Fatalf("MaterializeNode: %v", err)
	}
	if n.Summary == "" {
		t.Error("Summary empty")
	}
	if n.Signature == nil {
		t.Error("Signature nil")
	}
	if n.Body == nil {
		t.Error("Body nil")
	}
	if n.Source == nil {
		t.Error("Source nil")
	}
	if len(n.Tokens) == 0 {
		t.Error("Tokens empty")
	}
}

func TestMaterializeNodeSummaryFromDocComment(t *testing.T) {
	r, _ := loadFixture(t)
	// Login has a doc comment ("// Login authenticates a user..."), so the
	// summary should be derived from it, not the declaration line.
	nodeID := id.Function("auth", "", "Login")
	n, err := store.MaterializeNode(r, nodeID, map[domain.LayerName]bool{domain.LayerSummary: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(n.Summary, "authenticates") {
		t.Errorf("Summary %q should mention doc-comment content", n.Summary)
	}
}

func TestMaterializeNodeSignatureMultiLine(t *testing.T) {
	r, _ := loadFixture(t)
	// Login's signature is multi-line: header + body lines.
	nodeID := id.Function("auth", "", "Login")
	n, err := store.MaterializeNode(r, nodeID, map[domain.LayerName]bool{domain.LayerSignature: true})
	if err != nil {
		t.Fatal(err)
	}
	if n.Signature == nil {
		t.Fatal("Signature nil")
	}
	if !strings.Contains(n.Signature.Text, "Login") {
		t.Errorf("Signature text missing Login: %q", n.Signature.Text)
	}
	// Multi-line signature should include the body content (function signature
	// materializer falls back to the full slice for EndRow > StartRow+1).
	if !strings.Contains(n.Signature.Text, "return Session") {
		t.Errorf("Signature should contain function body: %q", n.Signature.Text)
	}
}

func TestMaterializeNodeBodyControlFlowLinear(t *testing.T) {
	r, _ := loadFixture(t)
	// Login returns Session directly — no branching.
	nodeID := id.Function("auth", "", "Login")
	n, err := store.MaterializeNode(r, nodeID, map[domain.LayerName]bool{domain.LayerBody: true})
	if err != nil {
		t.Fatal(err)
	}
	if n.Body == nil {
		t.Fatal("Body nil")
	}
	if n.Body.ControlFlow != "linear" {
		t.Errorf("ControlFlow = %q, want linear", n.Body.ControlFlow)
	}
	if len(n.Body.Stmts) == 0 {
		t.Error("Stmts should be non-empty")
	}
}

func TestMaterializeNodeBodyControlFlowBranching(t *testing.T) {
	// Add a function with branching to a tempdir and reload — keeps the fixture
	// itself linear-friendly while still exercising the branching path.
	dir := t.TempDir()
	src := `package x

func Branch(x int) int {
	if x > 0 {
		return x
	}
	return -x
}
`
	if err := writeFile(t, dir, "branch.go", src); err != nil {
		t.Fatal(err)
	}
	r2, _, err := store.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	nodeID := id.Function("x", "", "Branch")
	n, err := store.MaterializeNode(r2, nodeID, map[domain.LayerName]bool{domain.LayerBody: true})
	if err != nil {
		t.Fatal(err)
	}
	if n.Body.ControlFlow != "branching" {
		t.Errorf("ControlFlow = %q, want branching", n.Body.ControlFlow)
	}
}

func TestMaterializeNodeTokensPopulated(t *testing.T) {
	r, _ := loadFixture(t)
	nodeID := id.Function("auth", "", "Login")
	n, err := store.MaterializeNode(r, nodeID, map[domain.LayerName]bool{domain.LayerTokens: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(n.Tokens) == 0 {
		t.Error("Tokens should be populated")
	}
	// Tokens should mention the function name.
	var sawName bool
	for _, tk := range n.Tokens {
		if tk.Value == "Login" {
			sawName = true
			break
		}
	}
	if !sawName {
		t.Errorf("Tokens missing 'Login': %+v", n.Tokens)
	}
}

func TestMaterializeNodeFileKind(t *testing.T) {
	r, fix := loadFixture(t)
	nodeID := id.File("auth/login.go")
	n, err := store.MaterializeNode(r, nodeID, map[domain.LayerName]bool{domain.LayerSummary: true})
	if err != nil {
		t.Fatal(err)
	}
	if n.Kind != domain.KindFile {
		t.Errorf("Kind = %q, want FILE", n.Kind)
	}
	if !strings.HasSuffix(n.Name, "login.go") {
		t.Errorf("Name = %q, want login.go", n.Name)
	}
	if fix == nil {
		t.Fatal("setup failed")
	}
}

func TestMaterializeNodeClass(t *testing.T) {
	r, _ := loadFixture(t)
	nodeID := id.Class("auth", "Session")
	n, err := store.MaterializeNode(r, nodeID, map[domain.LayerName]bool{domain.LayerSummary: true})
	if err != nil {
		t.Fatal(err)
	}
	if n.Kind != domain.KindClass {
		t.Errorf("Kind = %q, want CLASS", n.Kind)
	}
	if n.Name != "Session" {
		t.Errorf("Name = %q", n.Name)
	}
}

func TestMaterializeNodeMethod(t *testing.T) {
	r, _ := loadFixture(t)
	nodeID := id.Method("auth", "User", "Greet")
	n, err := store.MaterializeNode(r, nodeID, map[domain.LayerName]bool{domain.LayerSummary: true})
	if err != nil {
		t.Fatal(err)
	}
	if n.Kind != domain.KindMethod {
		t.Errorf("Kind = %q, want METHOD", n.Kind)
	}
	if n.Name != "Greet" {
		t.Errorf("Name = %q", n.Name)
	}
}

func TestQuickNode(t *testing.T) {
	r, _ := loadFixture(t)
	n, err := store.QuickNode(r, id.Function("auth", "", "Login").String())
	if err != nil {
		t.Fatalf("QuickNode: %v", err)
	}
	if n.Summary == "" {
		t.Error("Summary should be populated by QuickNode")
	}
	if n.Signature != nil {
		t.Error("QuickNode should not populate Signature")
	}
}

func TestQuickNodeInvalidID(t *testing.T) {
	r, _ := loadFixture(t)
	if _, err := store.QuickNode(r, "not a real id"); err == nil {
		t.Error("QuickNode should error on bad id")
	}
}

func TestBodyFor(t *testing.T) {
	r, _ := loadFixture(t)
	body, err := store.BodyFor(r, id.Function("auth", "", "Login").String())
	if err != nil {
		t.Fatal(err)
	}
	if body == nil || len(body.Stmts) == 0 {
		t.Error("Body should be populated")
	}
	if body.ControlFlow == "" {
		t.Error("ControlFlow should be set")
	}
}

func TestBodyForInvalidID(t *testing.T) {
	r, _ := loadFixture(t)
	if _, err := store.BodyFor(r, "not a real id"); err == nil {
		t.Error("BodyFor should error on bad id")
	}
}

func TestProvenanceOnLayers(t *testing.T) {
	r, _ := loadFixture(t)
	nodeID := id.Function("auth", "", "Login")
	layers := map[domain.LayerName]bool{
		domain.LayerSummary:   true,
		domain.LayerSignature: true,
		domain.LayerBody:      true,
		domain.LayerSource:    true,
	}
	n, err := store.MaterializeNode(r, nodeID, layers)
	if err != nil {
		t.Fatal(err)
	}
	if n.SummaryProvenance == nil || n.SummaryProvenance.Tool != "summarizer" {
		t.Errorf("SummaryProvenance = %+v, want summarizer", n.SummaryProvenance)
	}
	if n.Signature == nil || n.Signature.Provenance.Tool != "tree-sitter" {
		t.Errorf("Signature.Provenance = %+v, want tree-sitter", n.Signature)
	}
	if n.Body == nil || n.Body.Provenance.Tool != "tree-sitter" {
		t.Errorf("Body.Provenance = %+v, want tree-sitter", n.Body)
	}
	if n.Source == nil || n.Source.Provenance.Tool != "tree-sitter" {
		t.Errorf("Source.Provenance = %+v, want tree-sitter", n.Source)
	}
}

func TestMaterializeNodeUnknownID(t *testing.T) {
	r, _ := loadFixture(t)
	nodeID := id.Function("auth", "", "NotExisting")
	_, err := store.MaterializeNode(r, nodeID, map[domain.LayerName]bool{domain.LayerSummary: true})
	if err == nil {
		t.Error("expected error for unknown node id")
	}
}

// writeFile is a small helper that writes a single file under dir/name.
// Used by the body-branching test that adds an extra source file on top of
// the shared fixture.
func writeFile(t *testing.T, dir, name, content string) error {
	t.Helper()
	return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
}
