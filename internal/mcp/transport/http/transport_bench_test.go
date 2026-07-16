package http

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kellenff/yactt/internal/registry"
	"github.com/kellenff/yactt/internal/store/repofixture"
	"github.com/kellenff/yactt/tests/chunking/genfixture"
)

// These benchmarks exercise MCP tool calls over the Streamable HTTP
// transport (POST /mcp) — the path a persistent `yactt mcp serve-http`
// daemon serves. Stdio framing is intentionally out of scope; see
// internal/mcp/server_bench_test.go for dispatch-only benches.
//
// Project sizes:
//   - small  — repofixture (~6 source files)
//   - medium — genfixture synthetic repo (100 files, ~500 declarations)
//
// Each tools/call sub-benchmark pre-warms the project via one
// index_repository round-trip before ResetTimer so the timed loop
// measures steady-state agent turns (parser-warm), not cold Load.
//
// Run:
//
//	go test -bench='^BenchmarkHTTP' -benchmem ./internal/mcp/transport/http/...
//	go test -bench='^BenchmarkHTTP_ToolsCall/medium' -benchtime=3x ./internal/mcp/transport/http/...

// benchFixture is one indexed project the HTTP daemon can resolve.
type benchFixture struct {
	name       string
	root       string
	projectURI string
	findName   string // name_path for find_symbol
	findPat    string // anchored regex for find_code
}

func benchSmallFixture(b *testing.B) benchFixture {
	b.Helper()
	fx := repofixture.New(b)
	return benchFixture{
		name:       "small",
		root:       fx.Root,
		projectURI: "file://" + fx.Root,
		findName:   "auth.Login",
		findPat:    `^func (Login|Authenticate|Charge|Refund)$`,
	}
}

func benchMediumFixture(b *testing.B) benchFixture {
	b.Helper()
	dir := b.TempDir()
	if _, err := genfixture.Write(dir); err != nil {
		b.Fatalf("genfixture.Write: %v", err)
	}
	return benchFixture{
		name:       "medium",
		root:       dir,
		projectURI: "file://" + dir,
		findName:   "auth.Aggregate",
		findPat:    `^func (Aggregate|Sanitize|Normalize|Charge|Refund)$`,
	}
}

type benchSession struct {
	ts  *httptest.Server
	srv *Server
	url string
	sid string
}

func newBenchSession(b *testing.B, fx benchFixture) *benchSession {
	b.Helper()
	cfg := ServerConfig{
		Token:        "",
		Bind:         "127.0.0.1",
		Port:         0,
		MaxSessions:  64,
		IdleTimeout:  5 * time.Minute,
		ProtocolName: "yactt-bench",
		Version:      "test",
		ProtocolVer:  ExpectedProtocol,
	}
	dir := b.TempDir()
	reg := registry.New(filepath.Join(dir, "projects.json"))
	if err := reg.Upsert(registry.Entry{
		Name: filepath.Base(fx.root),
		Path: fx.root,
	}); err != nil {
		b.Fatalf("registry upsert: %v", err)
	}

	srv := NewServer(cfg, reg)
	ts := httptest.NewServer(srv.Handler())
	b.Cleanup(func() {
		ts.Close()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	})

	sid, err := benchInitialize(ts.URL + "/mcp")
	if err != nil {
		b.Fatalf("initialize: %v", err)
	}
	if err := benchWarmProject(ts.URL+"/mcp", sid, fx.projectURI); err != nil {
		b.Fatalf("warm index_repository: %v", err)
	}

	return &benchSession{ts: ts, srv: srv, url: ts.URL + "/mcp", sid: sid}
}

func benchInitialize(url string) (string, error) {
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"bench","version":"1.0"}}}`
	resp, err := benchPost(url, "", body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	sid := resp.Header.Get(HeaderSessionID)
	if sid == "" {
		return "", fmt.Errorf("initialize missing %s", HeaderSessionID)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return sid, nil
}

func benchWarmProject(url, sid, projectURI string) error {
	body := fmt.Sprintf(
		`{"jsonrpc":"2.0","id":99,"method":"tools/call","params":{"name":"index_repository","arguments":{"project":%q}}}`,
		projectURI,
	)
	resp, err := benchPost(url, sid, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("index_repository status = %d", resp.StatusCode)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func benchPost(url, sid, body string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if sid != "" {
		req.Header.Set(HeaderSessionID, sid)
		req.Header.Set(HeaderProtocolVersion, ExpectedProtocol)
	}
	return http.DefaultClient.Do(req)
}

// benchDelete terminates a session so initialize benches do not
// accumulate slots against MaxSessions across large b.N runs.
func benchDelete(url, sid string) error {
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set(HeaderSessionID, sid)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("DELETE status = %d, want 204", resp.StatusCode)
	}
	return nil
}

func benchToolsCall(b *testing.B, sess *benchSession, toolName, argsJSON string) {
	b.Helper()
	body := fmt.Sprintf(
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":%q,"arguments":%s}}`,
		toolName, argsJSON,
	)
	resp, err := benchPost(sess.url, sess.sid, body)
	if err != nil {
		b.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		b.Fatalf("read body: %v", err)
	}
}

// BenchmarkHTTP_Initialize measures session allocation + initialize
// dispatch over HTTP. One fresh session per iteration; each session
// is DELETEd outside the timed path so MaxSessions is never exhausted.
func BenchmarkHTTP_Initialize(b *testing.B) {
	cfg := ServerConfig{
		Token:        "",
		Bind:         "127.0.0.1",
		Port:         0,
		MaxSessions:  256,
		IdleTimeout:  5 * time.Minute,
		ProtocolName: "yactt-bench",
		Version:      "test",
		ProtocolVer:  ExpectedProtocol,
	}
	dir := b.TempDir()
	reg := registry.New(filepath.Join(dir, "projects.json"))
	srv := NewServer(cfg, reg)
	ts := httptest.NewServer(srv.Handler())
	b.Cleanup(func() {
		ts.Close()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	})
	url := ts.URL + "/mcp"
	initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"bench","version":"1.0"}}}`

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp, err := benchPost(url, "", initBody)
		if err != nil {
			b.Fatalf("post: %v", err)
		}
		sid := resp.Header.Get(HeaderSessionID)
		if sid == "" {
			b.Fatalf("missing session id")
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		// Drop the session outside the timed path so large b.N
		// runs never trip MaxSessions / ErrSessionCap.
		b.StopTimer()
		if err := benchDelete(url, sid); err != nil {
			b.Fatalf("delete session: %v", err)
		}
		b.StartTimer()
	}
}

// BenchmarkHTTP_ToolsList measures tools/list over an established HTTP
// session. Project size does not affect the registry tool catalog.
func BenchmarkHTTP_ToolsList(b *testing.B) {
	sess := newBenchSession(b, benchSmallFixture(b))
	body := `{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{}}`

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp, err := benchPost(sess.url, sess.sid, body)
		if err != nil {
			b.Fatalf("post: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			b.Fatalf("status = %d", resp.StatusCode)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
}

// BenchmarkHTTP_ToolsCall measures representative tools/call round-trips
// over HTTP for small and medium project sizes.
func BenchmarkHTTP_ToolsCall(b *testing.B) {
	fixtures := []func(*testing.B) benchFixture{
		benchSmallFixture,
		benchMediumFixture,
	}
	tools := []struct {
		name string
		call func(fx benchFixture) (tool string, args string)
	}{
		{
			name: "list_projects",
			call: func(fx benchFixture) (string, string) {
				return "list_projects", `{}`
			},
		},
		{
			name: "tree_overview",
			call: func(fx benchFixture) (string, string) {
				return "tree_overview", fmt.Sprintf(`{"project":%q,"depth":2}`, fx.projectURI)
			},
		},
		{
			name: "find_symbol",
			call: func(fx benchFixture) (string, string) {
				return "find_symbol", fmt.Sprintf(`{"project":%q,"name_path":%q,"limit":10}`, fx.projectURI, fx.findName)
			},
		},
		{
			name: "find_code",
			call: func(fx benchFixture) (string, string) {
				return "find_code", fmt.Sprintf(
					`{"project":%q,"pattern":%q,"pattern_kind":"regex","scope":%q,"limit":20}`,
					fx.projectURI, fx.findPat, fx.root,
				)
			},
		},
	}

	for _, setup := range fixtures {
		fx := setup(b)

		for _, tc := range tools {
			b.Run(fx.name+"/"+tc.name, func(b *testing.B) {
				sess := newBenchSession(b, fx)
				toolName, args := tc.call(fx)
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					benchToolsCall(b, sess, toolName, args)
				}
			})
		}
	}
}
