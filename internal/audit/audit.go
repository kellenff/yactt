// Package audit emits structured application-level audit events to
// support AST09 (No Governance) mitigation.
//
// Two event kinds ship:
//
//   - startup:  emitted once per MCP server launch on the writer passed
//     to EmitStartup. Carries the resolved root, MaxFiles cap, grammar
//     set, LSP presence/version, and the binary's SHA-256 (so a host
//     can cross-check against the published release).
//   - tool_call: emitted once per MCP tool invocation when audit is
//     enabled. Carries the tool name, paths discovered in the input
//     JSON, output byte count, duration, and error status.
//
// Both writers are line-delimited JSON, one event per line. Callers
// pick the writer — typically stderr for startup, an opt-in file path
// for tool_call (configured via --audit-log=).
package audit

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Startup captures the state of the MCP server at launch time.
// Field tags are the on-wire shape; keep them stable — downstream
// parsers (log shippers, incident dashboards) rely on the names.
type Startup struct {
	Event        string     `json:"event"`
	Timestamp    string     `json:"timestamp"`
	Version      string     `json:"version"`
	RepoRoot     string     `json:"repo_root"`
	MaxFiles     int        `json:"max_files"`
	LoadedFiles  int        `json:"loaded_files"`
	Grammars     []string   `json:"grammars"`
	LSP          []LSPEntry `json:"lsp"`
	BinarySHA256 string     `json:"binary_sha256,omitempty"`
}

// LSPEntry is one row in the per-language LSP status table.
// Empty Tool/Version indicates no server was wired for that language
// at startup (server missing on PATH, declined to start, etc.).
type LSPEntry struct {
	Language string `json:"language"`
	Tool     string `json:"tool,omitempty"`
	Version  string `json:"version,omitempty"`
}

// ToolCall captures one MCP tool invocation.
// InputPaths is the set of absolute paths discovered in the JSON
// args; OutputBytes is the size of the marshalled result; DurationMs
// is the wall-clock time the handler consumed.
//
// SessionID and ClientAddr are populated only for HTTP sessions;
// stdio omits both (omitempty). They carry the Mcp-Session-Id UUID
// and the remote "host:port" respectively, so an operator can
// correlate audit lines with the client that issued them.
type ToolCall struct {
	Event       string   `json:"event"`
	Timestamp   string   `json:"timestamp"`
	Tool        string   `json:"tool"`
	InputPaths  []string `json:"input_paths"`
	OutputBytes int      `json:"output_bytes"`
	DurationMs  int64    `json:"duration_ms"`
	IsError     bool     `json:"is_error"`
	SessionID   string   `json:"session_id,omitempty"`
	ClientAddr  string   `json:"client_addr,omitempty"`
}

// Logger is a thread-safe line-delimited JSON event writer. The zero
// value has no writer and silently drops events — useful in tests that
// don't care about audit output. Callers wire it via NewLogger.
type Logger struct {
	mu sync.Mutex
	w  io.Writer
}

// NewLogger wraps w. Events are emitted as one JSON object per line
// followed by '\n'. The underlying writer is not closed by Logger.
func NewLogger(w io.Writer) *Logger {
	return &Logger{w: w}
}

// NewFileLogger opens path for append and returns a Logger writing
// to it. The file is closed when the returned closer is called.
// ponytail: opens O_APPEND so multiple yactt processes (rare, but
// possible during dev) append to the same file without clobbering.
func NewFileLogger(path string) (*Logger, io.Closer, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("audit: open %s: %w", path, err)
	}
	return NewLogger(bufio.NewWriter(f)), f, nil
}

// EmitStartup writes a single startup line to w. Best-effort: a
// write failure is returned to the caller (typically main), which
// logs to stderr and continues — startup audit is observational, not
// blocking.
func EmitStartup(w io.Writer, s Startup) error {
	if w == nil {
		return nil
	}
	if s.Event == "" {
		s.Event = "startup"
	}
	if s.Timestamp == "" {
		s.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if _, err := w.Write(append(b, '\n')); err != nil {
		return err
	}
	if flusher, ok := w.(interface{ Flush() error }); ok {
		_ = flusher.Flush()
	}
	return nil
}

// HTTPMeta carries per-session HTTP context for the audit emit.
// The HTTP transport attaches one of these via WithHTTPMeta on
// every dispatch; stdio passes nothing and the meta fields are
// omitted from the emit.
//
// SessionID is the Mcp-Session-Id UUID; ClientAddr is the
// remote "host:port".
type HTTPMeta struct {
	SessionID  string
	ClientAddr string
}

// HTTPMetaKey is the exported context.Context key used by
// WithHTTPMeta and HTTPMetaFromContext. Exported so consumers in
// other packages can use it without re-declaring a private type
// (Go uses pointer equality on context keys, so two private
// types would not match).
type HTTPMetaKey struct{}

// WithHTTPMeta returns a child context carrying `meta`. A nil
// meta is a no-op. The HTTP transport attaches a per-request
// meta before calling Server.dispatch; the audit shim reads it
// back via HTTPMetaFromContext.
func WithHTTPMeta(ctx context.Context, meta HTTPMeta) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, HTTPMetaKey{}, meta)
}

// HTTPMetaFromContext returns the HTTPMeta attached by
// WithHTTPMeta, or the zero value when none is attached. Stdio
// sessions pass no meta and get the zero value.
func HTTPMetaFromContext(ctx context.Context) HTTPMeta {
	if ctx != nil {
		if v := ctx.Value(HTTPMetaKey{}); v != nil {
			if m, ok := v.(HTTPMeta); ok {
				return m
			}
		}
	}
	return HTTPMeta{}
}

// LogToolCall emits a single tool_call line via the receiver's
// logger. Safe to call on a nil receiver — it's a no-op, which
// keeps handler code free of audit-on/off branches.
//
// inputPaths is typically the output of ExtractPaths(argsJSON).
// outputBytes is the size of the marshalled result (0 on error).
//
// `ctx` is observed for an attached HTTPMeta (via WithHTTPMeta);
// stdio passes context.Background() and the meta fields are
// omitted. The HTTP transport attaches session_id + client_addr
// on every dispatch so incident responders can correlate the
// line with the originating client.
func (l *Logger) LogToolCall(ctx context.Context, tool string, inputPaths []string, outputBytes int, duration time.Duration, isError bool) {
	if l == nil || l.w == nil {
		return
	}
	if inputPaths == nil {
		inputPaths = []string{}
	}
	meta := HTTPMetaFromContext(ctx)
	ev := ToolCall{
		Event:       "tool_call",
		Timestamp:   time.Now().UTC().Format(time.RFC3339Nano),
		Tool:        tool,
		InputPaths:  inputPaths,
		OutputBytes: outputBytes,
		DurationMs:  duration.Milliseconds(),
		IsError:     isError,
		SessionID:   meta.SessionID,
		ClientAddr:  meta.ClientAddr,
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.w.Write(append(b, '\n'))
	if flusher, ok := l.w.(interface{ Flush() error }); ok {
		_ = flusher.Flush()
	}
}

// pathFieldNames are tool-argument field names whose string values
// are paths (absolute or relative to the repo) and should always be
// captured by ExtractPaths, even when they don't satisfy the
// absolute-path shape test. Examples:
//
//   - "file" (get_symbols_overview, repo-relative like "auth/login.go")
//   - "scope" (search / find_symbol / find_code, may be absolute or
//     repo-relative)
//   - "file_filter" (find_code glob; captured verbatim so the audit
//     reader can reconstruct the access pattern)
//
// Ponytail: this list is a deliberate coupling to the tool schemas
// in internal/tool. New tools that take a path-shaped argument
// should add the field name here, not bypass the audit.
var pathFieldNames = map[string]bool{
	"repo":        true,
	"file":        true,
	"scope":       true,
	"file_filter": true,
}

// ExtractPaths walks a raw JSON arguments object and returns every
// string value that looks like a path. The heuristic has two
// layers:
//
//  1. Tool-argument field names in pathFieldNames are always
//     captured verbatim (covers relative paths like "auth/login.go"
//     and globs like "src/**/*.go").
//  2. Any other string value that looks like an absolute path is
//     captured (POSIX "/" prefix or Windows drive-letter prefix).
//
// Anything else (query strings, symbol ids, identifiers) is
// skipped — false positives would clutter the audit log with
// things the reader can't resolve.
func ExtractPaths(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		// Args aren't an object/array — best-effort scan as a single
		// string. Handlers usually pass JSON objects, but be liberal.
		s := strings.TrimSpace(string(raw))
		s = strings.Trim(s, `"`)
		if looksLikePath(s) {
			return []string{s}
		}
		return nil
	}
	out := walk(v, "")
	// Stable order so downstream parses see the same line for the
	// same args; map iteration order is randomized.
	sort.Strings(out)
	return out
}

func walk(v any, key string) []string {
	switch x := v.(type) {
	case map[string]any:
		var out []string
		for k, vv := range x {
			out = append(out, walk(vv, k)...)
		}
		return out
	case []any:
		var out []string
		for _, vv := range x {
			out = append(out, walk(vv, key)...)
		}
		return out
	case string:
		if x == "" {
			// Empty path-named field is "no path supplied" — don't
			// pollute the audit log with the literal empty string.
			return nil
		}
		if pathFieldNames[key] || looksLikePath(x) {
			return []string{x}
		}
	}
	return nil
}

func looksLikePath(s string) bool {
	if s == "" {
		return false
	}
	if s[0] == '/' {
		return true
	}
	// Windows: drive letter + colon + separator. Keep the check
	// tight so a stray "C:foo" doesn't qualify.
	if len(s) >= 3 && isLetter(s[0]) && s[1] == ':' && (s[2] == '/' || s[2] == '\\') {
		return true
	}
	return false
}

func isLetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// BinarySHA256 returns the hex SHA-256 of the file at path. Returns
// the empty string + error on read failure (caller decides whether
// the missing hash is fatal — for audit purposes, it's not).
func BinarySHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// KnownGoodResult is the outcome of a KnownGood check. Status is
// "match", "mismatch", "missing" (no TOFU file), or "unknown_version"
// (TOFU records a different version than the running binary).
type KnownGoodResult struct {
	Status   string
	Version  string
	Expected string
	Actual   string
}

// CheckKnownGood reads the TOFU file at path and compares the
// (version, sha256) tuple against the running binary's SHA-256.
//
//   - missing:        no TOFU file (legitimate for dev installs).
//   - unknown_version: TOFU records a different version — likely a
//     successful upgrade; skip the hash check rather than false-
//     alarm.
//   - match:          sha256 matches; trust chain intact.
//   - mismatch:       same version, different sha256 — strong signal
//     of replay / compromised release; caller should warn loudly.
//
// Returns an error only on I/O / parse failure (malformed TOFU
// file), not on status. Callers surface the result to the user.
func CheckKnownGood(tofuPath, currentVersion, actualSHA string) (KnownGoodResult, error) {
	if tofuPath == "" {
		return KnownGoodResult{Status: "missing"}, nil
	}
	data, err := os.ReadFile(tofuPath)
	if err != nil {
		if os.IsNotExist(err) {
			return KnownGoodResult{Status: "missing"}, nil
		}
		return KnownGoodResult{}, err
	}
	line := strings.TrimSpace(string(data))
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return KnownGoodResult{}, fmt.Errorf("audit: malformed known-good file %q", tofuPath)
	}
	version, expected := fields[0], fields[1]
	if version != currentVersion {
		return KnownGoodResult{
			Status:   "unknown_version",
			Version:  version,
			Expected: expected,
			Actual:   actualSHA,
		}, nil
	}
	if expected != actualSHA {
		return KnownGoodResult{
			Status:   "mismatch",
			Version:  version,
			Expected: expected,
			Actual:   actualSHA,
		}, nil
	}
	return KnownGoodResult{
		Status:  "match",
		Version: version,
	}, nil
}

// KnownGoodPath returns the XDG-canonical TOFU file path used by
// the install hook. Honours $XDG_DATA_HOME/yactt/known-good,
// falling back to $HOME/.local/share/yactt/known-good. Returns ""
// when neither env var resolves.
func KnownGoodPath() string {
	var base string
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		base = xdg
	} else if home, err := os.UserHomeDir(); err == nil && home != "" {
		base = filepath.Join(home, ".local", "share")
	} else {
		return ""
	}
	return filepath.Join(base, "yactt", "known-good")
}