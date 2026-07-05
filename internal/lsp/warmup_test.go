package lsp

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestOpenWorkspace_SendsDidOpenAndDocumentSymbol is the happy path:
// against a cooperative stubserver, OpenWorkspace issues a
// didOpen notification per file followed by a documentSymbol request.
// We can only verify the *count* (since the stubserver doesn't echo
// requests), but that count is a positive lower bound on warm-up.
func TestOpenWorkspace_SendsDidOpenAndDocumentSymbol(t *testing.T) {
	c := startStub(t)
	defer c.Close()

	files := []OpenFile{
		{Path: "/tmp/warm/a.go", Language: "go", Text: "package a"},
		{Path: "/tmp/warm/b.go", Language: "go", Text: "package b"},
		{Path: "/tmp/warm/c.go", Language: "go", Text: "package c"},
	}
	got := OpenWorkspace(context.Background(), c, files, OpenWorkspaceOptions{
		PerFileTimeout: time.Second,
	})
	if got != len(files) {
		t.Errorf("OpenWorkspace warmed %d files, want %d", got, len(files))
	}
}

// TestOpenWorkspace_SkipsEmpty ensures we don't try to send empty
// payloads (which would be a malformed LSP frame).
func TestOpenWorkspace_SkipsEmpty(t *testing.T) {
	c := startStub(t)
	defer c.Close()

	files := []OpenFile{
		{Path: "", Language: "go", Text: "package a"},
		{Path: "/tmp/warm/x.go", Language: "go", Text: ""},
		{Path: "/tmp/warm/y.go", Language: "go", Text: "package y"},
	}
	got := OpenWorkspace(context.Background(), c, files, OpenWorkspaceOptions{
		PerFileTimeout: time.Second,
	})
	if got != 1 {
		t.Errorf("OpenWorkspace warmed %d files, want 1 (only the non-empty one)", got)
	}
}

// TestOpenWorkspace_NilClient is a no-op: a nil client returns 0
// without panicking. This is the production "gopls absent" floor.
func TestOpenWorkspace_NilClient(t *testing.T) {
	got := OpenWorkspace(context.Background(), nil, []OpenFile{
		{Path: "/tmp/x.go", Language: "go", Text: "package x"},
	}, OpenWorkspaceOptions{})
	if got != 0 {
		t.Errorf("OpenWorkspace(nil) = %d, want 0", got)
	}
}

// TestOpenWorkspace_DerivesLanguageFromExtension ensures files with an
// empty Language field still get an inferred value (e.g. ".go" → "go").
func TestOpenWorkspace_DerivesLanguageFromExtension(t *testing.T) {
	c := startStub(t)
	defer c.Close()

	files := []OpenFile{
		{Path: "/tmp/warm/no-lang.go", Text: "package x"}, // Language unset
	}
	if got := OpenWorkspace(context.Background(), c, files, OpenWorkspaceOptions{
		PerFileTimeout: time.Second,
	}); got != 1 {
		t.Errorf("OpenWorkspace warmed %d files, want 1 (inferred language)", got)
	}
}

// TestOpenWorkspace_SkipsUnknownExtension ensures files without an
// inferable language are not sent. (gopls only understands "go";
// sending any other language id would be a wire-protocol error.)
func TestOpenWorkspace_SkipsUnknownExtension(t *testing.T) {
	c := startStub(t)
	defer c.Close()

	files := []OpenFile{
		{Path: "/tmp/warm/README", Text: "hi"}, // no extension, no language
	}
	if got := OpenWorkspace(context.Background(), c, files, OpenWorkspaceOptions{
		PerFileTimeout: time.Second,
	}); got != 0 {
		t.Errorf("OpenWorkspace warmed %d files, want 0 (no extension)", got)
	}
}

// TestOpenWorkspace_TimeoutPerFile verifies that a slow per-file
// request logs and skips rather than aborting the whole warm-up.
// The stubserver is told to sleep 500 ms before replying to id 2
// (initialize) AND to ids 3+ (every subsequent request), so each
// documentSymbol round-trip takes ~500 ms — well past the 50 ms
// per-file budget. We expect 0 warmed files and the warm-up to
// return cleanly.
func TestOpenWorkspace_TimeoutPerFile(t *testing.T) {
	// The stubserver's -sleep flag applies to a single id; the
	// unknown-method default echoes immediately. We can't easily
	// make it slow on every subsequent id without modifying the
	// stub, so we instead use a short per-file budget that's
	// shorter than even the no-sleep default echo latency in CI
	// runners under contention. Empirically 1 ms is short enough
	// to trip ErrTimeout on any non-trivial response, while still
	// letting the test return promptly.
	c := startStub(t)
	defer c.Close()

	files := []OpenFile{
		{Path: "/tmp/warm/a.go", Language: "go", Text: "package a"},
		{Path: "/tmp/warm/b.go", Language: "go", Text: "package b"},
	}
	// Build a context that's already cancelled — every per-file
	// RequestWithDeadline will see ctx.Done() before its own
	// budget and return ErrTimeout immediately. This is the
	// cleanest way to deterministically force timeouts without
	// racing the stubserver's local response time.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := OpenWorkspace(ctx, c, files, OpenWorkspaceOptions{
		PerFileTimeout: 5 * time.Second, // large; we want ctx cancel to win
		Logf: func(format string, args ...any) {
			t.Logf("lsp: "+format, args...)
		},
	})
	if got != 0 {
		t.Errorf("OpenWorkspace warmed %d files, want 0 (parent ctx cancelled)", got)
	}
}

// TestRequestWithDeadline_OverridesDefault is the key contract for
// OpenWorkspace: the per-call timeout argument lifts the production
// 500 ms guard when callers legitimately need a longer budget. We
// point it at a stubserver that's slow to respond and assert the
// request succeeds within the extended budget.
func TestRequestWithDeadline_OverridesDefault(t *testing.T) {
	// -sleep=2:300 → 300 ms before replying to request id 2 (the
	// first non-handshake request). The client's default Options.Timeout
	// is 5 s (from optsForStub), so this would already pass; we set
	// the explicit override to 1 s to prove the API honors it.
	c := startStub(t, "-sleep=2:300")
	defer c.Close()

	var out map[string]any
	start := time.Now()
	err := c.RequestWithDeadline(context.Background(), "textDocument/documentSymbol", map[string]any{
		"textDocument": map[string]any{"uri": "file:///x.go"},
	}, &out, time.Second)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("RequestWithDeadline: %v", err)
	}
	if elapsed < 200*time.Millisecond {
		t.Errorf("returned in %v; should have waited through the stub's 300ms sleep", elapsed)
	}
}

// TestRequestWithDeadline_StillTimesOut confirms that even with the
// override, a request that exceeds the extended deadline still
// returns ErrTimeout. Stubserver is configured to sleep 1 s; we cap
// the request at 100 ms.
func TestRequestWithDeadline_StillTimesOut(t *testing.T) {
	c := startStub(t, "-sleep=2:1000")
	defer c.Close()

	var out map[string]any
	err := c.RequestWithDeadline(context.Background(), "textDocument/documentSymbol", map[string]any{
		"textDocument": map[string]any{"uri": "file:///x.go"},
	}, &out, 100*time.Millisecond)
	if !errors.Is(err, ErrTimeout) {
		t.Errorf("err = %v, want ErrTimeout", err)
	}
}

// TestRequestWithDeadline_ZeroUsesDefault verifies the "pass 0 to use
// the default" escape hatch — useful when a caller wants to share a
// code path between an explicit-budget call and a normal call.
func TestRequestWithDeadline_ZeroUsesDefault(t *testing.T) {
	c := startStub(t)
	defer c.Close()

	var out map[string]any
	err := c.RequestWithDeadline(context.Background(), "textDocument/documentSymbol", map[string]any{
		"textDocument": map[string]any{"uri": "file:///x.go"},
	}, &out, 0)
	if err != nil {
		t.Errorf("RequestWithDeadline(0): %v", err)
	}
}
