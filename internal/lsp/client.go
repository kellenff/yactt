package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Options configures a Client. Defaults are applied by New.
type Options struct {
	// Timeout is the per-request budget. Default 500ms (per design §5.1
	// latency budget for warm gopls). Tests typically use 50ms.
	Timeout time.Duration

	// Concurrency caps in-flight requests. Default 8 (per design §8 open
	// question 4 — gopls can't service more usefully than this).
	Concurrency int

	// CloseTimeout is the deadline for graceful shutdown before
	// `cmd.Process.Kill()`. Default 2s.
	CloseTimeout time.Duration

	// RootURI is the workspace root passed to the server in the
	// `initialize` request. Required; e.g. "file:///path/to/repo".
	RootURI string

	// Logf, if non-nil, receives diagnostic messages (server stderr,
	// frame drops, handshake errors). Useful in tests; nil disables logging.
	Logf func(string, ...any)

	// CaptureStderr makes the LSP driver redirect the child's stderr to
	// opts.Logf. Defaults to false (stderr inherits to the parent's
	// terminal). Used by tests.
	CaptureStderr bool
}

// Client is one JSON-RPC 2.0 stdio connection to a language server.
//
// Safe for concurrent use by multiple goroutines: a counting semaphore caps
// in-flight requests, and a single goroutine dispatches replies to waiting
// callers via per-id channels.
type Client struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser // read-only end of the child's stdout pipe
	stderr io.Reader     // optional; drained in a goroutine if non-nil

	sem chan struct{} // counting semaphore, capacity = Options.Concurrency

	writeMu sync.Mutex // serializes writes to stdin

	mu      sync.Mutex // protects pending
	pending map[int64]chan frame
	nextID  atomic.Int64

	closed  atomic.Bool
	closeCh chan struct{} // closed at start of Close; signals reader goroutine
	doneCh  chan struct{} // closed when reader goroutine has fully exited

	serverID       string // captured during initialize
	version        string // captured during initialize
	closeTimeout   time.Duration
	requestTimeout time.Duration

	closeOnce sync.Once
}

// frame is one JSON-RPC message read from the server.
//
// Replies are addressed to a single pending[id] wait channel. Notifications
// carry no id and are dropped into Logf; we never act on server-initiated
// requests.
type frame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *wireError      `json:"error,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type wireError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *wireError) Error() string {
	return fmt.Sprintf("lsp: server error %d: %s", e.Code, e.Message)
}

const (
	defaultTimeout      = 500 * time.Millisecond
	defaultConcurrency  = 8
	defaultCloseTimeout = 2 * time.Second
)

// New starts the child process, performs the LSP `initialize` handshake, and
// returns a live `*Client`.
//
// The `parentStdin` and `parentStdout` arguments must be the PARENT-side
// write end and read end of the pipes wired into `cmd` (typically via
// `cmd.StdinPipe()` / `cmd.StdoutPipe()`; the parent-side handles are the
// values those calls return). These are passed separately because Go
// stores the *child-side* ends in `cmd.Stdin` / `cmd.Stdout` — the
// type assertion on the cmd field would give us the wrong end.
//
// If `stderr` is non-nil it will be drained to either opts.Logf
// (`<prefix>: <line>` per chunk) or io.Discard.
//
// Errors at any handshake step kill the child and return a wrapped error.
func New(ctx context.Context, cmd *exec.Cmd, parentStdin io.WriteCloser, parentStdout io.ReadCloser, stderr io.Reader, opts Options) (*Client, error) {
	if opts.Timeout <= 0 {
		opts.Timeout = defaultTimeout
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = defaultConcurrency
	}
	if opts.CloseTimeout <= 0 {
		opts.CloseTimeout = defaultCloseTimeout
	}
	if opts.RootURI == "" {
		return nil, errors.New("lsp: Options.RootURI is required")
	}
	if parentStdin == nil {
		return nil, errors.New("lsp: parentStdin is nil (wire cmd.StdinPipe() first)")
	}
	if parentStdout == nil {
		return nil, errors.New("lsp: parentStdout is nil (wire cmd.StdoutPipe() first)")
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("lsp: start child: %w", err)
	}

	c := &Client{
		cmd:            cmd,
		stdin:          parentStdin,
		stdout:         parentStdout,
		stderr:         stderr,
		sem:            make(chan struct{}, opts.Concurrency),
		pending:        make(map[int64]chan frame),
		closeCh:        make(chan struct{}),
		doneCh:         make(chan struct{}),
		version:        "unknown",
		closeTimeout:   opts.CloseTimeout,
		requestTimeout: opts.Timeout,
	}

	if stderr != nil {
		if opts.Logf != nil {
			go drainTo(stderr, func(b []byte) {
				opts.Logf("lsp-stderr: %s", string(b))
			})
		} else {
			go io.Copy(io.Discard, stderr)
		}
	}

	go c.readLoop(opts.Logf)

	// initialize handshake. Per-request timeout covers this too, but we
	// give it a much larger window since a cold server (gopls first
	// load on a workspace) can take 5+ seconds to index everything
	// before it'll answer.
	hsTimeout := opts.Timeout * 30
	if hsTimeout < 10*time.Second {
		hsTimeout = 10 * time.Second
	}
	hsCtx, cancel := context.WithTimeout(ctx, hsTimeout)
	defer cancel()

	pid := os.Getpid()
	var init InitializeResult
	if err := c.sendRequest(hsCtx, c.nextID.Add(1), "initialize", InitializeParams{
		ProcessID: &pid,
		RootURI:   opts.RootURI,
	}, &init); err != nil {
		c.killOnFailure()
		return nil, fmt.Errorf("lsp: initialize: %w", err)
	}
	if init.ServerInfo.Name != "" {
		c.serverID = init.ServerInfo.Name
		c.version = init.ServerInfo.Version
	}

	if err := c.Notify(hsCtx, "initialized", struct{}{}); err != nil {
		c.killOnFailure()
		return nil, fmt.Errorf("lsp: initialized: %w", err)
	}

	return c, nil
}

// Version returns the version captured during the initialize handshake
// (e.g. "v0.16.1" from gopls). Falls back to "unknown" when the server
// didn't report one.
func (c *Client) Version() string { return c.version }

// ServerName returns the server name captured during the initialize
// handshake (e.g. "gopls"). Falls back to "" when the server didn't
// report one.
func (c *Client) ServerName() string { return c.serverID }

// RequestTimeout returns the per-request timeout the client was
// configured with. OpenWorkspace uses this as the default per-file
// deadline for the eager-warm pass when the caller doesn't override it.
func (c *Client) RequestTimeout() time.Duration { return c.requestTimeout }

// RequestWithDeadline is Request with an explicit per-call timeout that
// overrides the client's default Options.Timeout. Used by OpenWorkspace
// to give cold-gopls warm-up requests a longer budget than the
// production 500 ms per-request guard.
//
// Pass `0` for `timeout` to use the client's configured default.
func (c *Client) RequestWithDeadline(ctx context.Context, method string, params, result any, timeout time.Duration) error {
	if timeout <= 0 {
		return c.Request(ctx, method, params, result)
	}
	override := time.Now().Add(timeout)
	if dl, ok := ctx.Deadline(); !ok || dl.After(override) {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, override)
		defer cancel()
	}
	return c.Request(ctx, method, params, result)
}

// Request sends a JSON-RPC request and decodes the response into `result`.
//
// The call is bounded by `Options.Timeout` (or the parent context's
// deadline, whichever is sooner). On timeout, ErrTimeout is returned so
// callers can stamp `FallbackUsed: "lsp-timeout"`.
func (c *Client) Request(ctx context.Context, method string, params, result any) error {
	if c.closed.Load() {
		return ErrClosed
	}

	// Apply the per-call deadline on top of the parent's.
	deadline := c.callTimeout(ctx)
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	// Concurrency cap: blocking send on the buffered channel.
	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		if isDeadlineExceeded(ctx) {
			return ErrTimeout
		}
		return ctx.Err()
	case <-c.closeCh:
		return ErrClosed
	}
	defer func() { <-c.sem }()

	id := c.nextID.Add(1)
	if err := c.sendRequest(ctx, id, method, params, result); err != nil {
		if errors.Is(err, ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return ErrTimeout
		}
		return err
	}
	return nil
}

func (c *Client) callTimeout(ctx context.Context) time.Time {
	// Effective deadline for this request.
	//
	// The configured per-request timeout is the hard upper bound —
	// a hung server cannot stall a tool call beyond this. A parent
	// context with a tighter deadline still wins (lets a single
	// request be aborted early by the caller). A parent context
	// with a *looser* deadline does not extend the upper bound;
	// otherwise a long warm-up budget would silently override the
	// per-request guard and a hung server could stall forever.
	t := time.Now().Add(c.requestTimeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(t) {
		t = dl
	}
	return t
}

// sendRequest writes one request frame and waits for the matching reply.
// Framing is newline-delimited JSON per the LSP spec.
func (c *Client) sendRequest(ctx context.Context, id int64, method string, params, result any) error {
	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}

	// Register the waiter BEFORE writing the frame so the reply can't
	// arrive ahead of the registration under heavy concurrency.
	wait := make(chan frame, 1)
	c.mu.Lock()
	c.pending[id] = wait
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	if err := c.writeFrame(req); err != nil {
		return fmt.Errorf("lsp: write %s: %w", method, err)
	}

	select {
	case f := <-wait:
		if f.Error != nil {
			return f.Error
		}
		if result != nil && len(f.Result) > 0 {
			if err := json.Unmarshal(f.Result, result); err != nil {
				return fmt.Errorf("lsp: decode %s result: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		if isDeadlineExceeded(ctx) {
			return fmt.Errorf("lsp: %s: %w", method, ErrTimeout)
		}
		return ctx.Err()
	case <-c.closeCh:
		return ErrClosed
	}
}

// Notify sends a JSON-RPC notification (no id, no response expected).
func (c *Client) Notify(ctx context.Context, method string, params any) error {
	if c.closed.Load() {
		return ErrClosed
	}
	frame := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	}
	if err := c.writeFrame(frame); err != nil {
		return fmt.Errorf("lsp: notify %s: %w", method, err)
	}
	return nil
}

func (c *Client) writeFrame(payload map[string]any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	// LSP framing: Content-Length header + CRLF + JSON body. The body
	// is the marshaled JSON; no trailing newline is required by the
	// spec, but most servers tolerate one.
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("lsp: marshal: %w", err)
	}
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(b))
	if _, err := io.WriteString(c.stdin, header); err != nil {
		return fmt.Errorf("lsp: write header: %w", err)
	}
	if _, err := c.stdin.Write(b); err != nil {
		return fmt.Errorf("lsp: write body: %w", err)
	}
	return nil
}

// killOnFailure kills the child and waits briefly for it to exit. Used at
// handshake-failure paths so we don't leak the child process.
func (c *Client) killOnFailure() {
	if c.cmd == nil || c.cmd.Process == nil {
		return
	}
	_ = c.cmd.Process.Kill()
	_, _ = c.cmd.Process.Wait()
}

// readLoop consumes LSP-framed messages from the server's stdout:
// headers (`Content-Length: N\r\n`) + `\r\n` + N bytes of body, repeat.
//
// Reply frames (with an `id`) are dispatched to waiting callers;
// notifications and server-initiated requests are dropped with logging.
// Exits when the pipe breaks (server closed) or closeCh fires.
func (c *Client) readLoop(logf func(string, ...any)) {
	defer close(c.doneCh)

	br := bufio.NewReader(c.stdout)
	for {
		select {
		case <-c.closeCh:
			return
		default:
		}
		// Read headers until blank line.
		var contentLen int
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				break
			}
			colon := strings.IndexByte(line, ':')
			if colon < 0 {
				continue
			}
			name := strings.ToLower(strings.TrimSpace(line[:colon]))
			value := strings.TrimSpace(line[colon+1:])
			switch name {
			case "content-length":
				n, perr := strconv.Atoi(value)
				if perr != nil {
					if logf != nil {
						logf("lsp: bad Content-Length: %q", value)
					}
					return
				}
				contentLen = n
			}
		}
		if contentLen <= 0 {
			return
		}
		body := make([]byte, contentLen)
		if _, err := io.ReadFull(br, body); err != nil {
			return
		}
		var f frame
		if err := json.Unmarshal(body, &f); err != nil {
			if logf != nil {
				logf("lsp: dropped malformed body: %v", err)
			}
			continue
		}
		if f.ID == nil {
			if logf != nil && f.Method != "" {
				logf("lsp: notification %s", f.Method)
			}
			continue
		}
		c.mu.Lock()
		wait, ok := c.pending[*f.ID]
		c.mu.Unlock()
		if !ok {
			if logf != nil {
				logf("lsp: dropped reply with unknown id %d", *f.ID)
			}
			continue
		}
		select {
		case wait <- f:
		default:
			if logf != nil {
				logf("lsp: dropped reply for stale id %d", *f.ID)
			}
		}
	}
}

// Close sends LSP `shutdown` + `exit` notifications, waits up to
// Options.CloseTimeout for the child to exit, then kills it.
//
// Idempotent. After Close, all subsequent Requests return ErrClosed.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		close(c.closeCh)

		if c.cmd != nil && c.cmd.Process != nil {
			// Best-effort graceful shutdown. shutdown / exit are
			// notifications, so we cannot wait for a reply — just give
			// the child up to CloseTimeout to drain.
			timeout := c.closeTimeout
			if timeout <= 0 {
				timeout = defaultCloseTimeout
			}
			_ = c.Notify(context.Background(), "shutdown", nil)
			_ = c.Notify(context.Background(), "exit", nil)
			done := make(chan error, 1)
			go func() { done <- c.cmd.Wait() }()
			select {
			case <-done:
			case <-time.After(timeout):
				_ = c.cmd.Process.Kill()
			}
		}

		// Wait for the reader goroutine to exit before closing stdin
		// to avoid races on the pipe.
		<-c.doneCh
		if c.stdin != nil {
			_ = c.stdin.Close()
		}
	})
	return nil
}

// drainTo reads from r in 4 KiB chunks and forwards each to onChunk.
func drainTo(r io.Reader, onChunk func([]byte)) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			onChunk(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// isDeadlineExceeded reports whether ctx hit its deadline (rather than
// being cancelled by the parent). Used to map a context error to
// ErrTimeout.
func isDeadlineExceeded(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	return ctx.Err() == context.DeadlineExceeded
}
