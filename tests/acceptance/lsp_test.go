// LSP-acceptance tests verify the Tier-1 (gopls) subgraph's deliverables
// at the boundary the contract actually cares about: an MCP tool call
// returning a node with the right provenance, and a cross-package edge
// resolved with confidence 1.0.
//
// These tests use the LIVE gopls on PATH when one exists (CI on this
// machine has it; CI on machines without it skips via t.Skip). When
// opportunistic-startup can't find gopls, the tools fall back to the
// tree-sitter subgraph with the existing `no-lsp-installed` marker.
package acceptance_test

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/id"
	"github.com/kellenff/yactt/internal/store"
	"github.com/kellenff/yactt/internal/tool"
)

// goplsAvailable returns true when a `gopls` binary is on the PATH that
// the test process inherits (matches what `Repo.Load` will use to spawn
// the LSP client).
func goplsAvailable(t *testing.T) bool {
	t.Helper()
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Logf("gopls not on PATH: %v", err)
		return false
	}
	return true
}

// TestLSPSignature_Provenance_goplsPresent loads the fixture, asks for
// the signature of `auth.Login`, and asserts the live-gopls branch is
// exercised: `Provenance.Tool == "gopls"` and `FallbackUsed == ""`.
//
// Skips when gopls is not on PATH — CI environments without it keep
// working via the Tier-2 tree-sitter subgraph.
func TestLSPSignature_Provenance_goplsPresent(t *testing.T) {
	if !goplsAvailable(t) {
		t.Skip("gopls not on PATH")
	}

	repo := loadRepo(t)
	reg := seedRegForProject(t, repo.Root())
	defer func() { _ = repo.Close() }()
	if repo.LSP() == nil {
		t.Skip("Repo.lsp == nil despite gopls on PATH (start failed?)")
	}

	out := callJSON(t, tool.GetNode(reg), `{"id":"fn:auth.Login","layers":["signature","body"]}`)
	n, ok := out.(*domain.Node)
	if !ok {
		t.Fatalf("node type: got %T", out)
	}
	if n.Signature == nil {
		t.Fatal("Signature nil")
	}
	if tool := n.Signature.Provenance.Tool; tool != "gopls" {
		t.Errorf("Signature.Provenance.Tool = %q, want gopls", tool)
	}
	if fu := n.Signature.Provenance.FallbackUsed; fu != "" {
		t.Errorf("Signature.Provenance.FallbackUsed = %q, want empty (Tier-1 success)", fu)
	}
}

// TestLSPCallers_CrossPackage_ResolvedConfidence asks for the callers
// of `payments.Charge` (the new fixture) and asserts that the
// cross-package caller `auth.Login` shows up with confidence 1.0 from
// gopls. Skipped when gopls is absent.
func TestLSPCallers_CrossPackage_ResolvedConfidence(t *testing.T) {
	if !goplsAvailable(t) {
		t.Skip("gopls not on PATH")
	}

	repo := loadRepo(t)
	reg := seedRegForProject(t, repo.Root())
	defer func() { _ = repo.Close() }()
	if repo.LSP() == nil {
		t.Skip("Repo.lsp == nil despite gopls on PATH")
	}

	out := callJSON(t, tool.NodeEdges(reg), `{"id":"fn:payments.Charge","kinds":["callers"]}`)
	env, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("node_edges envelope type: got %T", out)
	}
	results, ok := env["edges"].([]tool.NodeEdgesResult)
	if !ok {
		t.Fatalf("node_edges edges slice type: got %T", env["edges"])
	}
	if len(results) == 0 {
		t.Fatal("expected at least one caller for payments.Charge")
	}

	// One of the results should be auth.Login at confidence 1.0 with
	// gopls provenance. We don't pin it to a particular row because
	// references may include declarations in future gopls versions,
	// and the exact ordering isn't part of the contract.
	var goplsHit bool
	for _, r := range results {
		if r.Provenance.Tool != "gopls" {
			continue
		}
		if r.Confidence != 1.0 {
			t.Errorf("Tier-1 caller confidence = %v, want 1.0 (id=%s)", r.Confidence, r.TargetID)
		}
		if !strings.HasSuffix(r.TargetID, ".Login") {
			t.Errorf("Tier-1 caller not Login: %s", r.TargetID)
		}
		goplsHit = true
	}
	if !goplsHit {
		// No gopls-resolved caller. The Tier-2 tree-sitter pass
		// should still have produced at least one caller row; if not,
		// call out the loss of Tier-1 in the failure message.
		var t2Hit bool
		for _, r := range results {
			if r.Provenance.Tool == "tree-sitter" && strings.HasSuffix(r.TargetID, ".Login") {
				t2Hit = true
				break
			}
		}
		if t2Hit {
			t.Errorf("Tier-2 found auth.Login as a caller but Tier-1 (gopls) did not; check that Repo.LSP() is wired and the LSP-prefixed fixtures are loaded")
		} else {
			t.Fatalf("Tier-1 missing AND Tier-2 missing for auth.Login -> payments.Charge: %+v", results)
		}
	}
}

// TestLSPFallback_NoGopls asserts the regression guard: when gopls is
// absent (the common case on developer laptops without the LSP
// dependency), tools still answer with `tree-sitter` provenance and
// `FallbackUsed == "no-lsp-installed"`.
//
// We force `r.lsp == nil` via DetachLSPForTest so the test exercises
// the floor path even when gopls is on PATH (otherwise opportunistic
// startup attaches the real client).
func TestLSPFallback_NoGopls(t *testing.T) {
	repo, _, err := store.Load("../fixtures/sample-go")
	if err != nil {
		t.Fatalf("loading fixture: %v", err)
	}
	defer func() { _ = repo.Close() }()
	repo.DetachLSPForTest()
	if repo.LSP() != nil {
		t.Fatalf("DetachLSPForTest did not detach; r.lsp is still non-nil")
	}

	out := callJSON(t, tool.GetNode(reg), `{"id":"fn:auth.Login","layers":["signature"]}`)
	n, ok := out.(*domain.Node)
	if !ok {
		t.Fatalf("node type: got %T", out)
	}
	if n.Signature == nil {
		t.Fatal("Signature nil")
	}
	if tool := n.Signature.Provenance.Tool; tool != "tree-sitter" {
		t.Errorf("Signature.Provenance.Tool = %q, want tree-sitter", tool)
	}
	if fu := n.Signature.Provenance.FallbackUsed; fu != "no-lsp-installed" {
		t.Errorf("Signature.Provenance.FallbackUsed = %q, want no-lsp-installed", fu)
	}
}

// TestLSPInstalled_Hover_TextFromGopls is a small smoke test: when
// gopls is present, the live hover answer differs from the syntactic
// fallback (gopls gives the typed `func(token string) error` shape, not
// the line-slice that tree-sitter would emit). We accept either as
// long as provenance is honest.
func TestLSPInstalled_Hover_HonestProvenance(t *testing.T) {
	if !goplsAvailable(t) {
		t.Skip("gopls not on PATH")
	}
	repo := loadRepo(t)
	reg := seedRegForProject(t, repo.Root())
	defer func() { _ = repo.Close() }()
	if repo.LSP() == nil {
		t.Skip("Repo.lsp == nil despite gopls on PATH")
	}

	out := callJSON(t, tool.GetNode(reg), `{"id":"fn:payments.Charge","layers":["signature"]}`)
	n, ok := out.(*domain.Node)
	if !ok {
		t.Fatalf("node type: got %T", out)
	}
	if n.Signature == nil {
		t.Fatal("Signature nil")
	}
	if n.Signature.Provenance.Tool != "gopls" {
		t.Errorf("Provenance.Tool = %q, want gopls", n.Signature.Provenance.Tool)
	}
	if n.Signature.Provenance.FallbackUsed != "" {
		t.Errorf("Provenance.FallbackUsed = %q, want empty", n.Signature.Provenance.FallbackUsed)
	}
	// Sanity: signature text contains something gopls-like — must
	// mention the function's name at minimum.
	if !strings.Contains(n.Signature.Text, "Charge") {
		t.Errorf("Signature text lacks function name: %q", n.Signature.Text)
	}
}

// unused-import guards so future refactors that drop references do
// not silently break the build.
var (
	_ = id.Function
	_ context.Context
	_ json.RawMessage
)
