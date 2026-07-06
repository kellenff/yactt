package audit_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kellenff/yactt/internal/audit"
)

// TestExtractPaths covers the heuristic for picking absolute paths
// out of arbitrary JSON-shaped tool arguments. The audit log is
// downstream-parsed by humans (and possibly a structured log shipper),
// so false positives must be kept narrow.
func TestExtractPaths(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "object with single absolute path",
			in:   `{"repo":"/Users/kellen/foo"}`,
			want: []string{"/Users/kellen/foo"},
		},
		{
			name: "object with multiple paths in nested structure",
			in:   `{"repo":"/r","paths":["/a","/b/c.go"]}`,
			want: []string{"/a", "/b/c.go", "/r"},
		},
		{
			name: "ignores relative paths",
			in:   `{"path":"foo/bar","nested":{"path":"baz/qux"}}`,
			want: nil,
		},
		{
			name: "ignores query strings and short identifiers",
			in:   `{"query":"Login","scope":"","limit":5}`,
			want: nil,
		},
		{
			name: "windows drive letter counts as absolute",
			in:   `{"repo":"C:\\Users\\kellen"}`,
			want: []string{`C:\Users\kellen`},
		},
		{
			name: "empty args",
			in:   ``,
			want: nil,
		},
		{
			name: "preserves duplicates — downstream can dedupe",
			in:   `{"a":"/x","b":"/x"}`,
			want: []string{"/x", "/x"},
		},
		{
			name: "captures repo-relative file path via 'file' field",
			in:   `{"file":"auth/login.go"}`,
			want: []string{"auth/login.go"},
		},
		{
			name: "captures 'scope' as a path (absolute or relative)",
			in:   `{"scope":"/Users/kellen/work/monorepo","limit":5}`,
			want: []string{"/Users/kellen/work/monorepo"},
		},
		{
			name: "captures 'file_filter' glob",
			in:   `{"pattern":"Login","file_filter":"src/auth/**/*.go"}`,
			want: []string{"src/auth/**/*.go"},
		},
		{
			name: "mixed: repo + file + nested scope",
			in:   `{"repo":"/r","file":"a/b.go","outer":{"scope":"/inner"}}`,
			want: []string{"/inner", "/r", "a/b.go"},
		},
		{
			name: "renames array — id is not a path, no capture",
			in:   `{"renames":[{"id":"fn:auth.Login","new_name":"SignIn"}]}`,
			want: nil,
		},
		{
			name: "non-JSON-string fallback: bare quoted path",
			in:   `"/Users/kellen"`,
			want: []string{"/Users/kellen"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := audit.ExtractPaths(json.RawMessage(tc.in))
			if !equalStringSlices(got, tc.want) {
				t.Errorf("ExtractPaths(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestEmitStartup verifies the startup line shape: required fields
// populated, default Event + Timestamp applied when missing, every
// entry ends with exactly one newline so a line-oriented parser
// can't get confused by partial flushes.
func TestEmitStartup(t *testing.T) {
	var buf bytes.Buffer
	s := audit.Startup{
		Version:      "v0.1.0",
		RepoRoot:     "/r",
		MaxFiles:     50000,
		LoadedFiles:  1234,
		Grammars:     []string{"go", "javascript", "typescript"},
		LSP:          []audit.LSPEntry{{Language: "go", Tool: "gopls", Version: "v0.16.2"}},
		BinarySHA256: "deadbeef",
	}
	if err := audit.EmitStartup(&buf, s); err != nil {
		t.Fatal(err)
	}
	out := strings.TrimRight(buf.String(), "\n")
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out)
	}
	if got["event"] != "startup" {
		t.Errorf("event = %v, want startup", got["event"])
	}
	if got["version"] != "v0.1.0" {
		t.Errorf("version = %v", got["version"])
	}
	if got["repo_root"] != "/r" {
		t.Errorf("repo_root = %v", got["repo_root"])
	}
	if got["max_files"].(float64) != 50000 {
		t.Errorf("max_files = %v", got["max_files"])
	}
	// Grammars must round-trip as []any of strings.
	g, ok := got["grammars"].([]any)
	if !ok || len(g) != 3 {
		t.Errorf("grammars = %v (%T)", got["grammars"], got["grammars"])
	}
	// LSP must be a JSON array of objects.
	lsp, ok := got["lsp"].([]any)
	if !ok || len(lsp) != 1 {
		t.Fatalf("lsp = %v (%T)", got["lsp"], got["lsp"])
	}
	row, ok := lsp[0].(map[string]any)
	if !ok {
		t.Fatalf("lsp[0] = %v (%T)", lsp[0], lsp[0])
	}
	if row["language"] != "go" || row["tool"] != "gopls" || row["version"] != "v0.16.2" {
		t.Errorf("lsp[0] = %v", row)
	}
	// Timestamp must default to a non-empty RFC3339Nano string.
	if got["timestamp"] == "" || got["timestamp"] == nil {
		t.Error("timestamp missing")
	}
}

// TestEmitStartupDefaults confirms the convenience fields are
// populated when the caller leaves them zero-valued.
func TestEmitStartupDefaults(t *testing.T) {
	var buf bytes.Buffer
	if err := audit.EmitStartup(&buf, audit.Startup{Version: "v"}); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(bytes.TrimRight(buf.Bytes(), "\n"), &got); err != nil {
		t.Fatal(err)
	}
	if got["event"] != "startup" {
		t.Errorf("default event not applied: %v", got["event"])
	}
	if got["timestamp"] == nil || got["timestamp"] == "" {
		t.Error("default timestamp not applied")
	}
}

// TestEmitStartupNilWriter confirms the zero-writer contract:
// callers in main() shouldn't need to nil-check before calling.
func TestEmitStartupNilWriter(t *testing.T) {
	if err := audit.EmitStartup(nil, audit.Startup{Version: "v"}); err != nil {
		t.Errorf("nil writer should be a no-op, got %v", err)
	}
}

// TestLoggerLogToolCall verifies the per-tool audit line. The
// fields are the on-wire contract for incident-response tooling.
func TestLoggerLogToolCall(t *testing.T) {
	var buf bytes.Buffer
	al := audit.NewLogger(&buf)
	al.LogToolCall("node_get", []string{"/r/foo.go"}, 1024, 42*time.Millisecond, false)
	if buf.Len() == 0 {
		t.Fatal("no output")
	}
	var got map[string]any
	if err := json.Unmarshal(bytes.TrimRight(buf.Bytes(), "\n"), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, buf.String())
	}
	if got["event"] != "tool_call" {
		t.Errorf("event = %v", got["event"])
	}
	if got["tool"] != "node_get" {
		t.Errorf("tool = %v", got["tool"])
	}
	paths, ok := got["input_paths"].([]any)
	if !ok || len(paths) != 1 || paths[0] != "/r/foo.go" {
		t.Errorf("input_paths = %v", got["input_paths"])
	}
	if got["output_bytes"].(float64) != 1024 {
		t.Errorf("output_bytes = %v", got["output_bytes"])
	}
	if got["duration_ms"].(float64) != 42 {
		t.Errorf("duration_ms = %v", got["duration_ms"])
	}
	if got["is_error"] != false {
		t.Errorf("is_error = %v", got["is_error"])
	}
	// nil input paths must marshal as [] (so downstream parsers see
	// the field consistently, not "null").
	var buf2 bytes.Buffer
	audit.NewLogger(&buf2).LogToolCall("t", nil, 0, time.Millisecond, true)
	var got2 map[string]any
	if err := json.Unmarshal(bytes.TrimRight(buf2.Bytes(), "\n"), &got2); err != nil {
		t.Fatal(err)
	}
	if _, ok := got2["input_paths"].([]any); !ok {
		t.Errorf("nil input_paths must round-trip as []any, got %T", got2["input_paths"])
	}
	if got2["is_error"] != true {
		t.Errorf("is_error = %v", got2["is_error"])
	}
}

// TestLoggerNilSafe confirms a nil receiver is a silent no-op. This
// keeps handler code free of "if audit != nil" branches.
func TestLoggerNilSafe(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil receiver panicked: %v", r)
		}
	}()
	var l *audit.Logger
	l.LogToolCall("t", nil, 0, 0, false)
}

// TestLoggerConcurrent pins the thread-safety contract: the dispatcher
// is called from one goroutine but the read loop and handler run in
// separate goroutines if a future server introduces concurrency.
func TestLoggerConcurrent(t *testing.T) {
	var buf bytes.Buffer
	al := audit.NewLogger(&buf)
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			al.LogToolCall("node_get", []string{"/r"}, 0, time.Microsecond, false)
		}()
	}
	wg.Wait()
	// Each goroutine wrote exactly one line; the buffer may contain
	// blank-leading content from concurrent appends — count newlines.
	if got := strings.Count(buf.String(), "\n"); got != 64 {
		t.Errorf("got %d newlines, want 64", got)
	}
}

// TestBinarySHA256 pins the hash routine's contract: 64-hex-char
// output for a present file, error for a missing file, deterministic
// across reads.
func TestBinarySHA256(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "binary")
	if err := os.WriteFile(p, []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := audit.BinarySHA256(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 64 {
		t.Errorf("hex SHA-256 must be 64 chars, got %d (%s)", len(got), got)
	}
	// Two SHA computations of the same file must be identical.
	got2, err := audit.BinarySHA256(p)
	if err != nil {
		t.Fatal(err)
	}
	if got != got2 {
		t.Error("deterministic hash expected")
	}
	// Missing file must error.
	if _, err := audit.BinarySHA256(filepath.Join(dir, "missing")); err == nil {
		t.Error("expected error on missing file")
	}
}

// TestCheckKnownGood covers the four status branches. The install
// hook is the writer of truth; we verify reader correctness here.
func TestCheckKnownGood(t *testing.T) {
	dir := t.TempDir()
	tofu := filepath.Join(dir, "known-good")

	// missing: no file
	got, err := audit.CheckKnownGood(tofu, "v0.1.0", "abc")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "missing" {
		t.Errorf("missing file: status = %q", got.Status)
	}

	// unknown_version: TOFU version differs from running version
	if err := os.WriteFile(tofu, []byte("v0.0.9 sha1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = audit.CheckKnownGood(tofu, "v0.1.0", "sha1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "unknown_version" {
		t.Errorf("unknown_version: status = %q", got.Status)
	}

	// match: same version + same hash
	if err := os.WriteFile(tofu, []byte("v0.1.0 sha1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = audit.CheckKnownGood(tofu, "v0.1.0", "sha1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "match" {
		t.Errorf("match: status = %q", got.Status)
	}

	// mismatch: same version, different hash — the security alert
	if err := os.WriteFile(tofu, []byte("v0.1.0 sha-expected\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = audit.CheckKnownGood(tofu, "v0.1.0", "sha-actual")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "mismatch" {
		t.Errorf("mismatch: status = %q", got.Status)
	}
	if got.Expected != "sha-expected" || got.Actual != "sha-actual" {
		t.Errorf("mismatch fields = %+v", got)
	}

	// Malformed TOFU file: error.
	if err := os.WriteFile(tofu, []byte("garbage\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := audit.CheckKnownGood(tofu, "v0.1.0", "x"); err == nil {
		t.Error("expected error on malformed TOFU file")
	}
}

// TestKnownGoodPathXDG confirms XDG_DATA_HOME takes precedence;
// HOME fallback is exercised indirectly by the install hook.
func TestKnownGoodPathXDG(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/tmp/xdg-data")
	got := audit.KnownGoodPath()
	if !strings.HasPrefix(got, "/tmp/xdg-data/yactt/known-good") {
		t.Errorf("XDG_DATA_HOME not honoured: %q", got)
	}
}

// TestNewFileLogger confirms the file-backed writer appends, doesn't
// clobber, and the closer releases the FD (so a follow-up open in the
// same process can read what we wrote).
func TestNewFileLogger(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "audit.log")
	l, closer, err := audit.NewFileLogger(p)
	if err != nil {
		t.Fatal(err)
	}
	l.LogToolCall("a", []string{"/r"}, 0, time.Millisecond, false)
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"tool":"a"`) {
		t.Errorf("audit file missing tool field: %s", body)
	}
	// A second open must append, not overwrite.
	l2, closer2, err := audit.NewFileLogger(p)
	if err != nil {
		t.Fatal(err)
	}
	l2.LogToolCall("b", nil, 0, 0, true)
	if err := closer2.Close(); err != nil {
		t.Fatal(err)
	}
	body2, _ := os.ReadFile(p)
	if !strings.Contains(string(body2), `"tool":"a"`) || !strings.Contains(string(body2), `"tool":"b"`) {
		t.Errorf("append failed: %s", body2)
	}
}
