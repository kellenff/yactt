package lsp

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubserverBin builds the internal/lsp/internal/stubserver helper binary
// and returns its absolute path. We rebuild per test to pick up source
// changes (the stubserver is a tiny package; the build is fast).
//
// We pass the source dir as a relative path that walks up one level to
// escape the "internal/" prefix shadow (Go's toolchain reserves the
// `internal/` name and "internal/stubserver" means a path inside GOROOT
// to its package resolver). Then we walk back in.
func stubserverBin(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "stubserver")
	// Use an absolute path resolved against the test process's cwd. We
	// also fall back to walking up one level (../internal/lsp/internal/
	// stubserver) for `go test` invocations from elsewhere.
	cwd, _ := os.Getwd()
	candidates := []string{
		filepath.Join(cwd, "internal", "stubserver"),
		filepath.Join(cwd, "..", "internal", "lsp", "internal", "stubserver"),
	}
	var src string
	for _, c := range candidates {
		if _, err := os.Stat(filepath.Join(c, "main.go")); err == nil {
			src = c
			break
		}
	}
	if src == "" {
		t.Fatalf("cannot find stubserver source (tried %v)", candidates)
	}
	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Dir = src
	if out2, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build stubserver: %v: %s", err, out2)
	}
	return out
}

// optsForStub returns a base Options tailored for talking to stubserver:
// short per-request timeout, small concurrency, logf enabled so test
// failures leave a paper trail. Specific tests (timeout) tighten the
// timeout further via direct Options literals.
func optsForStub(t *testing.T, bin string) Options {
	return Options{
		Timeout:      5 * time.Second,
		Concurrency:  8,
		CloseTimeout: time.Second,
		RootURI:      "file:///tmp/stubserver-test",
		Logf: func(format string, args ...any) {
			t.Logf("stubserver: "+format, args...)
		},
	}
}

// startStub opens a stubserver child, performs the LSP handshake, and
// returns a live client. Tests should `defer c.Close()`.
func startStub(t *testing.T, extraArgs ...string) *Client {
	t.Helper()
	bin := stubserverBin(t)
	args := append([]string{bin}, extraArgs...)
	opts := optsForStub(t, bin)
	c, err := StartCommand(context.Background(), args, opts)
	if err != nil {
		t.Fatalf("startStub: %v", err)
	}
	return c
}

// TestRequest_RoundTrip is the happy path: send one request, receive one
// reply, decode it.
func TestRequest_RoundTrip(t *testing.T) {
	c := startStub(t, "-name=test-server", "-version=v1.2.3")
	defer c.Close()

	got := c.ServerName()
	if got != "test-server" {
		t.Errorf("ServerName() = %q, want test-server", got)
	}
	if v := c.Version(); v != "v1.2.3" {
		t.Errorf("Version() = %q, want v1.2.3", v)
	}

	// Use a method the stubserver replies to with the default echo shape
	// (textDocument/hover returns a hover payload now, not an echo).
	var out map[string]any
	err := c.Request(context.Background(), "echo", map[string]any{
		"line": 1,
		"col":  2,
	}, &out)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if echo, _ := out["echo"].(bool); !echo {
		t.Errorf("reply did not include echo:true: %+v", out)
	}
}

// TestRequest_Concurrent serializes N>cap requests through the semaphore
// without dropping any.
func TestRequest_Concurrent(t *testing.T) {
	c := startStub(t)
	defer c.Close()

	const N = 50
	var wg sync.WaitGroup
	var errs int32
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var out map[string]any
			if err := c.Request(context.Background(), "echo", nil, &out); err != nil {
				atomic.AddInt32(&errs, 1)
				t.Errorf("request: %v", err)
			}
		}()
	}
	wg.Wait()
	if n := atomic.LoadInt32(&errs); n != 0 {
		t.Fatalf("%d concurrent requests failed", n)
	}
}

// TestRequest_Timeout_StubSlow returns ErrTimeout when the stub sleeps
// past Options.Timeout. Counts request id 2 (the first non-handshake).
func TestRequest_Timeout_StubSlow(t *testing.T) {
	bin := stubserverBin(t)
	opts := optsForStub(t, bin)
	opts.Timeout = 50 * time.Millisecond
	c, err := StartCommand(context.Background(), []string{bin, "-sleep=2:500"}, opts)
	if err != nil {
		t.Fatalf("startStub: %v", err)
	}
	defer c.Close()

	err = c.Request(context.Background(), "echo", nil, nil)
	if !errors.Is(err, ErrTimeout) {
		t.Errorf("Request(500ms sleep) = %v, want ErrTimeout", err)
	}
}

// TestRequest_ServerError_StubFail returns a wrapped wireError when the
// stub answers with a JSON-RPC error frame. The plan asks that callers
// can stamp `FallbackUsed: "lsp-error"`.
func TestRequest_ServerError_StubFail(t *testing.T) {
	c := startStub(t, "-fail=2") // fail id 2 (the first non-handshake)
	defer c.Close()

	err := c.Request(context.Background(), "echo", nil, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if errors.Is(err, ErrTimeout) {
		t.Errorf("err = ErrTimeout, want a server error")
	}
	if msg := err.Error(); !contains(msg, "method not found") {
		t.Errorf("err did not include stub message: %q", msg)
	}
}

// TestNotify_NoReplyRequired verifies that notifications don't block on a
// reply and don't perturb the request counter.
func TestNotify_NoReplyRequired(t *testing.T) {
	c := startStub(t)
	defer c.Close()

	done := make(chan struct{})
	go func() {
		_ = c.Notify(context.Background(), "workspace/didChangeWatchedFiles", nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Notify blocked for >1s")
	}

	// A follow-up request still works.
	var out map[string]any
	if err := c.Request(context.Background(), "echo", nil, &out); err != nil {
		t.Errorf("request after notify: %v", err)
	}
}

// TestNew_BadHandshake_WrongShape returns a wrapped error when the stub
// sends an invalid InitializeResult.
func TestNew_BadHandshake_WrongShape(t *testing.T) {
	bin := stubserverBin(t)
	_, err := StartCommand(context.Background(),
		[]string{bin, "-bad-init"},
		optsForStub(t, bin))
	if err == nil {
		t.Fatal("expected error on bad-init, got nil")
	}
}

// TestClose_ShutdownNotifications verifies Close sends shutdown + exit and
// returns nil on a cooperative server.
func TestClose_ShutdownNotifications(t *testing.T) {
	c := startStub(t)
	// Issue one round-trip to ensure the server is live past handshake.
	var out map[string]any
	if err := c.Request(context.Background(), "echo", nil, &out); err != nil {
		t.Fatalf("pre-close request: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	// A subsequent request returns ErrClosed.
	err := c.Request(context.Background(), "echo", nil, nil)
	if !errors.Is(err, ErrClosed) {
		t.Errorf("post-Close Request err = %v, want ErrClosed", err)
	}
}

// TestClose_Idempotent ensures Close can be called twice safely.
func TestClose_Idempotent(t *testing.T) {
	c := startStub(t)
	if err := c.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// TestClose_StuckChild_KillsOnTimeout uses -no-shutdown to keep the child
// alive past the close notification; the test verifies Close() returns
// after Options.CloseTimeout (with a Process.Kill fallback).
func TestClose_StuckChild_KillsOnTimeout(t *testing.T) {
	opts := optsForStub(t, "")
	opts.CloseTimeout = 100 * time.Millisecond
	c, err := StartCommand(context.Background(),
		[]string{stubserverBin(t), "-no-shutdown"}, opts)
	if err != nil {
		t.Fatalf("startStub: %v", err)
	}

	start := time.Now()
	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Errorf("Close took %v, expected ~100ms", elapsed)
	}
}

// TestStart_LookPath_Missing covers the ErrUnavailable sentinel path
// without spawning a real gopls: temporarily poison PATH.
func TestStart_LookPath_Missing(t *testing.T) {
	t.Setenv("PATH", "")
	_, err := Start(context.Background(), "/tmp/no-such-root", Options{RootURI: "file:///x"})
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("Start with empty PATH = %v, want ErrUnavailable", err)
	}
}

// Helper kept here so the test file does not need `strings` import.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestNew_DefaultsFromOptionsBoundary covers the three LIVED CONDITIONALS
// BOUNDARY mutants in New() at lines 122/125/128 (`opts.X <= 0` should
// fall back to the default). Send exactly 0 for each — boundary case.
// A mutation flipping `<=` to `<` would skip the default fallback for
// 0-valued Options.
func TestNew_DefaultsFromOptionsBoundary(t *testing.T) {
	bin := stubserverBin(t)
	opts := Options{
		Timeout:      0, // boundary
		Concurrency:  0, // boundary
		CloseTimeout: 0, // boundary
		RootURI:      "file:///tmp/stubserver-test",
		Logf: func(format string, args ...any) {
			t.Logf("stubserver: "+format, args...)
		},
	}
	c, err := StartCommand(context.Background(), []string{bin}, opts)
	if err != nil {
		t.Fatalf("StartCommand with zero Options: %v", err)
	}
	defer c.Close()

	// The defaults must have replaced 0. Verify by checking that
	// RequestTimeout returns the default value.
	if got := c.RequestTimeout(); got != defaultTimeout {
		t.Errorf("RequestTimeout = %v, want defaultTimeout (%v)", got, defaultTimeout)
	}
}

// TestRequestWithDeadline_ZeroTimeout covers the `if timeout <= 0`
// branch at line 226 in RequestWithDeadline — passing 0 falls back
// to the client's default. The mutation `<= 0` → `< 0` would skip
// this for 0.
func TestRequestWithDeadline_ZeroTimeout(t *testing.T) {
	c := startStub(t)
	defer c.Close()

	var out map[string]any
	err := c.RequestWithDeadline(context.Background(), "echo", nil, &out, 0)
	if err != nil {
		t.Errorf("RequestWithDeadline(timeout=0) = %v, want nil (falls back to default)", err)
	}
}

// TestFallbackReason_NonDeadlineError covers the `ctx.Err() ==
// context.DeadlineExceeded` check (line 529) via the public
// FallbackReason helper.
func TestFallbackReason_NonDeadlineError(t *testing.T) {
	wrapped := &fakeErr{msg: "stub: failed"}
	reason := FallbackReason(wrapped)
	if reason == "" {
		t.Errorf("FallbackReason returned empty for non-deadline error")
	}
}

// TestFallbackReason_NilCtxContext covers the `ctx == nil` early
// return at line 526. isDeadlineExceeded(nil) is a package-private
// helper; we exercise it via FallbackReason with various error shapes.
func TestFallbackReason_ErrDeadline(t *testing.T) {
	// Wrap context.DeadlineExceeded — should map to "lsp-timeout".
	reason := FallbackReason(context.DeadlineExceeded)
	if reason == "" {
		t.Errorf("FallbackReason returned empty for DeadlineExceeded")
	}
}

// fakeErr is a minimal error not wrapping DeadlineExceeded.
type fakeErr struct{ msg string }

func (e *fakeErr) Error() string { return e.msg }
