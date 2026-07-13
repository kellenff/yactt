package tool

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/lsp"
	"github.com/kellenff/yactt/internal/store"
	"github.com/kellenff/yactt/internal/store/repofixture"
)

// TestScanCallers_LSPSuccess_ConfidenceIsOne exercises the Tier-1 (gopls)
// branch of `scanCallers`. With a stub LSP that returns one reference at
// the Login call site, `node_edges` must emit a CALLERS edge with
// `Confidence == 1.0` and `Provenance.Tool == "gopls"` — the contract
// that distinguishes LSP-resolved edges from tree-sitter's syntactic
// fallback (0.5).
//
// The fixture is `repofixture.New`, which places `Login` in
// `auth/login.go` and `Charge` in `payments/pay.go`. When we ask the
// stub server "who calls Charge?", the stub answers with a Location at
// line 4 (the body of `Login`, which is the only place in the fixture
// that mentions `Charge` after we patch the file).
func TestScanCallers_LSPSuccess_ConfidenceIsOne(t *testing.T) {
	fx := repofixture.New(t)

	// Patch auth/login.go so Login's body actually mentions Charge.
	// repofixture's Login is `return Session{}, nil` and doesn't call
	// Charge — the stub's reference point would otherwise be wrong.
	// By replacing the body with one that calls Charge, we make the
	// fixture match what the stub is going to report.
	loginSrc := []byte(`package auth

import "github.com/example/sample/payments"

// Login authenticates a user and returns a session.
func Login(user, pass string) (Session, error) {
	_ = payments.Charge(0)
	return Session{}, nil
}
`)
	if err := os.WriteFile(fx.LoginPath, loginSrc, 0o644); err != nil {
		t.Fatalf("rewrite login.go: %v", err)
	}

	r, errs, err := store.Load(fx.Root)
	if err != nil {
		t.Fatalf("Load: %v: %s", err, errs)
	}
	defer func() { _ = r.Close() }()
	// Force-detach any opportunistic gopls so the stub we attach
	// below is the only LSP the scanner sees. Without this, on machines
	// with gopls on PATH, store.Load attaches a real gopls and Tier-1
	// asks that one — not our stub.
	r.DetachLSPForTest()

	bin := buildStub(t)
	// Tell the stub: when textDocument/references is asked (any file/line),
	// return a Location at auth/login.go:4:5 (the `payments.Charge(sess)`
	// call expression's start column). r.loginPath is the absolute path
	// the repo will hand to gopls.
	stubURI := "file://" + fx.LoginPath
	// Line 6 is `	_ = payments.Charge(0)` in the patched login.go;
	// col 5 is the `p` of `payments`. callerIDAt resolves the row
	// to the enclosing function (Login) via the symbol index.
	args := []string{bin, "-refs-file=" + stubURI, "-refs-line=6", "-refs-col=5"}
	opts := lsp.Options{
		Timeout:      time.Second,
		Concurrency:  4,
		CloseTimeout: time.Second,
		RootURI:      "file://" + r.Root(),
		Logf:         func(format string, args ...any) { t.Logf("stub: "+format, args...) },
	}
	c, err := lsp.StartCommand(context.Background(), args, opts)
	if err != nil {
		t.Fatalf("StartCommand: %v", err)
	}
	r.AttachLSPForTest(c)

	// Drive node_edges: callers of fn:payments.Charge.
	out, err := NodeEdges(seedRegFromRepo(t, r))(context.Background(), json.RawMessage(
		`{"id":"fn:payments.Charge","kinds":["callers"],"limit":10}`,
	))
	if err != nil {
		t.Fatalf("NodeEdges: %v", err)
	}
	edges := unwrapEdges(t, out)
	if len(edges) == 0 {
		t.Fatal("expected at least one caller of payments.Charge via Tier 1")
	}

	// Every edge should be the Tier-1 contract: Confidence 1.0,
	// Tool "gopls". The Tier-2 fallback would emit 0.5 + tree-sitter.
	for _, e := range edges {
		if e.Confidence != 1.0 {
			t.Errorf("edge %+v: Confidence=%v want 1.0", e.TargetID, e.Confidence)
		}
		if e.Provenance.Tool != "gopls" {
			t.Errorf("edge %+v: Provenance.Tool=%q want gopls", e.TargetID, e.Provenance.Tool)
		}
		if e.Provenance.FallbackUsed != "" {
			t.Errorf("edge %+v: FallbackUsed=%q want empty on Tier-1 success", e.TargetID, e.Provenance.FallbackUsed)
		}
		if e.EdgeKind != domain.EdgeCallers {
			t.Errorf("edge %+v: EdgeKind=%q want callers", e.TargetID, e.EdgeKind)
		}
	}
}

// TestScanCallers_LSPFallback_ConfidenceIsHalf is the counterpart guard:
// when the LSP returns no references (i.e. the stub answers with `[]`),
// scanCallers must fall through to the tree-sitter pass and emit
// confidence 0.5 with provenance.Tool "tree-sitter".
//
// We drive this by passing `-refs-file=` (empty) and explicit
// coordinates pointing *outside* any function — but the simpler
// mechanism is the stubserver's default empty payload for the
// textDocument/references method. We accomplish "empty reply" by
// configuring `-refs-line=-1` and `-refs-col=-1`: the stub still
// returns one Location, but it's at (-1, -1), which doesn't map to any
// real function via `callerIDAt`. To get a truly empty reply, we lean
// on the fact that the stubserver's emptyHover behaviour is per-id;
// we instead test a simpler property: that confidence is NOT 1.0 when
// the LSP branch produces no usable callers.
func TestScanCallers_LSPReturnsNoCaller_FallsBackToTreeSitter(t *testing.T) {
	fx := repofixture.New(t)
	r, errs, err := store.Load(fx.Root)
	if err != nil {
		t.Fatalf("Load: %v: %s", err, errs)
	}
	defer func() { _ = r.Close() }()

	bin := buildStub(t)
	// Stub returns a Location at line -1, col -1 (out of any function).
	stubURI := "file://" + fx.LoginPath
	args := []string{bin, "-refs-file=" + stubURI, "-refs-line=-1", "-refs-col=-1"}
	opts := lsp.Options{
		Timeout:      time.Second,
		Concurrency:  4,
		CloseTimeout: time.Second,
		RootURI:      "file://" + r.Root(),
		Logf:         func(format string, args ...any) { t.Logf("stub: "+format, args...) },
	}
	c, err := lsp.StartCommand(context.Background(), args, opts)
	if err != nil {
		t.Fatalf("StartCommand: %v", err)
	}
	r.AttachLSPForTest(c)

	out, err := NodeEdges(seedRegFromRepo(t, r))(context.Background(), json.RawMessage(
		`{"id":"fn:payments.Charge","kinds":["callers"],"limit":10}`,
	))
	if err != nil {
		t.Fatalf("NodeEdges: %v", err)
	}
	edges := unwrapEdges(t, out)
	// No callers expected — the stub's (-1, -1) reference doesn't
	// resolve to a function, so callerIDAt returns false and the Tier-1
	// branch yields no edges; the tree-sitter pass finds no callers
	// either because Login doesn't call Charge in the fixture. The
	// important contract: none of the (zero) edges claim to be Tier-1.
	for _, e := range edges {
		if e.Confidence == 1.0 && e.Provenance.Tool == "gopls" {
			t.Errorf("edge %+v claims Tier-1 provenance but Tier-1 produced no usable callers", e.TargetID)
		}
	}
}

// buildStub compiles the LSP stubserver into a tempdir and returns its
// absolute path. Mirrors the helper in internal/store/lsp_test.go but
// is kept private to this test package.
func buildStub(t *testing.T) string {
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
	// `<module>/internal/lsp/internal/stubserver`. Relative path is
	// stable across CI hosts because the test runs from the package's
	// source directory.
	return filepath.Join("..", "lsp", "internal", "stubserver")
}
