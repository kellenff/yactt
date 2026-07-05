package store

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/id"
	"github.com/kellenff/yactt/internal/lsp"
	"github.com/kellenff/yactt/internal/store/repofixture"
)

// These tests verify the Tier-1 (gopls) wiring on top of the existing
// tree-sitter materializers. They build a stub JSON-RPC server, point a
// Repo at it via `Repo.attachLSP` (a test-only helper), and assert that
// `MaterializeNode` stamps the right provenance.
//
// We deliberately reuse the stubserver from `internal/lsp` so we don't
// need a real gopls in CI — the LSP-level behaviour (hover returns OK,
// hover times out) is exactly what each branch of the materializer cares
// about.

func buildStubserverBin(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "stubserver")
	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Dir = stubSourceDir()
	if out2, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build stubserver: %v: %s", err, out2)
	}
	return out
}

func stubSourceDir() string {
	// `go test` runs each package from its source directory. The
	// stubserver lives at `<module>/internal/lsp/internal/stubserver`.
	// We resolve it relative to this file via runtime.Caller would be
	// safer, but a known path is fine for tests.
	return filepath.Join("..", "lsp", "internal", "stubserver")
}

// attachLSP is a test-only helper that swaps Repo.lsp to a fresh client
// using the binary at `binPath`. We keep this unexported (and the test
// file is `package store`, not `store_test`) so production code can't
// reach it.
//
// Closes any client already on the repo so the test fixture replaces
// opportunistic-startup's first attempt (which may have attached a real
// gopls on machines where it is on PATH).
func (r *Repo) attachLSP(t *testing.T, binPath string, opts lsp.Options) *lsp.Client {
	t.Helper()
	if r.lsp != nil {
		_ = r.lsp.Close()
		r.lsp = nil
	}
	args := []string{binPath}
	c, err := lsp.StartCommand(context.Background(), args, opts)
	if err != nil {
		t.Fatalf("attachLSP: %v", err)
	}
	r.lsp = c
	r.lspVersion = c.Version()
	return c
}

// TestSignature_LSPNil_FallbackMarker is the regression guard: when LSP
// is not wired, signature provenance stays exactly as it was before
// Tier 1 (tree-sitter with "no-lsp-installed"). Without this test a
// future change to the materializer could silently flip the marker.
//
// We force `r.lsp == nil` (via DetachLSPForTest) to exercise the floor
// path even on machines with gopls installed; opportunistic startup
// otherwise attaches the real client.
func TestSignature_LSPNil_FallbackMarker(t *testing.T) {
	r, _ := loadFixture(t)
	defer func() { _ = r.Close() }()
	r.DetachLSPForTest()

	nodeID := id.Function("auth", "", "Login")
	n, err := MaterializeNode(r, nodeID, map[domain.LayerName]bool{domain.LayerSignature: true})
	if err != nil {
		t.Fatalf("MaterializeNode: %v", err)
	}
	if n.Signature == nil {
		t.Fatal("Signature nil")
	}
	if got := n.Signature.Provenance.Tool; got != "tree-sitter" {
		t.Errorf("Provenance.Tool = %q, want tree-sitter", got)
	}
	if got := n.Signature.Provenance.FallbackUsed; got != "no-lsp-installed" {
		t.Errorf("Provenance.FallbackUsed = %q, want no-lsp-installed", got)
	}
}

// TestSignature_LSPSuccess_TypedAnswer exercises the Tier-1 success
// path: with a cooperative stub LSP, signature provenance flips to
// gopls and the typed contents land in Body.Types (best-effort
// param/result parse).
func TestSignature_LSPSuccess_TypedAnswer(t *testing.T) {
	r, _ := loadFixture(t)
	defer func() { _ = r.Close() }()

	bin := buildStubserverBin(t)
	opts := lsp.Options{
		Timeout:      time.Second,
		Concurrency:  4,
		CloseTimeout: time.Second,
		RootURI:      "file://" + r.Root(),
		Logf: func(format string, args ...any) {
			t.Logf("stub: "+format, args...)
		},
	}
	c := r.attachLSP(t, bin, opts)

	nodeID := id.Function("auth", "", "Login")
	n, err := MaterializeNode(r, nodeID, map[domain.LayerName]bool{
		domain.LayerSignature: true,
		domain.LayerBody:      true,
	})
	if err != nil {
		t.Fatalf("MaterializeNode: %v", err)
	}
	_ = c

	if n.Signature == nil {
		t.Fatal("Signature nil")
	}
	if got := n.Signature.Provenance.Tool; got != "gopls" {
		t.Errorf("Provenance.Tool = %q, want gopls", got)
	}
	if got := n.Signature.Provenance.FallbackUsed; got != "" {
		t.Errorf("Provenance.FallbackUsed = %q, want empty", got)
	}
	if n.Signature.Text == "" {
		t.Error("Signature.Text empty after gopls answer")
	}
}

// TestSignature_LSPTimeout_TaggedFallback verifies the failure path:
// when the stub blocks past the per-request timeout, signature falls
// back to tree-sitter and `FallbackUsed` is the canonical
// "lsp-timeout" so consumers can detect a hung gopls and retry.
func TestSignature_LSPTimeout_TaggedFallback(t *testing.T) {
	r, _ := loadFixture(t)
	defer func() { _ = r.Close() }()

	bin := buildStubserverBin(t)
	opts := lsp.Options{
		Timeout:      50 * time.Millisecond, // tighter than stubserver's 500ms sleep
		Concurrency:  4,
		CloseTimeout: time.Second,
		RootURI:      "file://" + r.Root(),
		Logf: func(format string, args ...any) {
			t.Logf("stub: "+format, args...)
		},
	}
	// Stub sleeps 500ms before replying to id 2 (the first non-handshake
	// request — i.e. the hover our materializer fires).
	c, err := lsp.StartCommand(context.Background(), []string{bin, "-sleep=2:500"}, opts)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	r.lsp = c
	r.lspVersion = c.Version()

	nodeID := id.Function("auth", "", "Login")
	n, err := MaterializeNode(r, nodeID, map[domain.LayerName]bool{domain.LayerSignature: true})
	if err != nil {
		t.Fatalf("MaterializeNode: %v", err)
	}
	if n.Signature == nil {
		t.Fatal("Signature nil")
	}
	if got := n.Signature.Provenance.Tool; got != "tree-sitter" {
		t.Errorf("Provenance.Tool = %q, want tree-sitter (after gopls timeout)", got)
	}
	if got := n.Signature.Provenance.FallbackUsed; got != "lsp-timeout" {
		t.Errorf("Provenance.FallbackUsed = %q, want lsp-timeout", got)
	}
}

// TestStart_LSPAbsent_RepoLSPNil is a sanity check that
// opportunistic-startup reasoning holds: when gopls is not on PATH,
// the attached client is nil and Close is a no-op. We force the nil
// state via DetachLSPForTest to assert the floor behavior on machines
// that DO have gopls.
func TestStart_LSPAbsent_RepoLSPNil(t *testing.T) {
	r, _ := loadFixture(t)
	r.DetachLSPForTest()
	if r.LSP() != nil {
		t.Errorf("DetachLSPForTest did not detach; LSP() != nil")
	}
	if err := r.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	// Idempotent: a second call is also a no-op.
	if err := r.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// loadFixture is the same fixture loader used by the existing tests;
// lives in the test file nearest to where the tests run.
func loadFixture(t *testing.T) (*Repo, error) {
	t.Helper()
	fx := repofixture.New(t)
	r, errs, err := Load(fx.Root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, e := range errs {
		t.Logf("load warning: %v", e)
	}
	return r, nil
}
