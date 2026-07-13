package http

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kellenff/yactt/internal/audit"
	"github.com/kellenff/yactt/internal/registry"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	cfg := ServerConfig{
		Token:        "",
		Bind:         "127.0.0.1",
		Port:         0,
		MaxSessions:  16,
		IdleTimeout:  5 * time.Minute,
		ProtocolName: "yactt-test",
		Version:      "test",
		ProtocolVer:  ExpectedProtocol,
	}
	dir := t.TempDir()
	reg := registry.New(filepath.Join(dir, "projects.json"))
	srv := NewServer(cfg, reg)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	})
	return ts
}

func TestHealthz_OK(t *testing.T) {
	s := newTestServer(t)
	resp, err := http.Get(s.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["status"] != "ok" {
		t.Fatalf("status field = %q, want ok", got["status"])
	}
}

func TestUnknownPath_404(t *testing.T) {
	s := newTestServer(t)
	resp, err := http.Get(s.URL + "/nope")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestMethodNotAllowed_MCPPath(t *testing.T) {
	s := newTestServer(t)
	req, _ := http.NewRequest("PATCH", s.URL+"/mcp", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 405 {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
	if allow := resp.Header.Get("Allow"); allow != "GET, POST, DELETE" {
		t.Fatalf("Allow header = %q, want GET, POST, DELETE", allow)
	}
}

func TestMethodNotAllowed_Healthz(t *testing.T) {
	s := newTestServer(t)
	req, _ := http.NewRequest("POST", s.URL+"/healthz", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 405 {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
}

func TestPOST_Initialize_CreatesSession(t *testing.T) {
	s := newTestServer(t)
	resp := postJSON(t, s.URL+"/mcp", "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	sid := resp.Header.Get("Mcp-Session-Id")
	if sid == "" {
		t.Fatal("Mcp-Session-Id missing")
	}
	var result struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Result.ProtocolVersion != ExpectedProtocol {
		t.Fatalf("ProtocolVersion = %q, want %q", result.Result.ProtocolVersion, ExpectedProtocol)
	}
}

func TestPOST_ToolsList_ReturnsRegisteredTools(t *testing.T) {
	s := newTestServer(t)
	resp1 := postJSON(t, s.URL+"/mcp", "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	sid := resp1.Header.Get("Mcp-Session-Id")
	resp1.Body.Close()

	resp := postJSON(t, s.URL+"/mcp", sid, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var result struct {
		Result struct {
			Tools []map[string]any `json:"tools"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if len(result.Result.Tools) == 0 {
		t.Fatalf("tools/list returned no tools")
	}
}

func TestPOST_Notification_NoContent(t *testing.T) {
	s := newTestServer(t)
	resp1 := postJSON(t, s.URL+"/mcp", "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	sid := resp1.Header.Get("Mcp-Session-Id")
	resp1.Body.Close()
	resp := postJSON(t, s.URL+"/mcp", sid, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
}

func TestPOST_NonInit_MissingSession_400(t *testing.T) {
	s := newTestServer(t)
	resp := postJSON(t, s.URL+"/mcp", "", `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestPOST_MalformedJSON_ParseError(t *testing.T) {
	s := newTestServer(t)
	resp := postJSON(t, s.URL+"/mcp", "", `{not json`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["error"] == nil {
		t.Fatalf("expected error envelope, got %v", got)
	}
}

func TestGET_OpenSSEStream(t *testing.T) {
	s := newTestServer(t)
	resp1 := postJSON(t, s.URL+"/mcp", "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	sid := resp1.Header.Get("Mcp-Session-Id")
	resp1.Body.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", s.URL+"/mcp", nil)
	req.Header.Set(HeaderSessionID, sid)
	req.Header.Set(HeaderProtocolVersion, ExpectedProtocol)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
}

func TestDELETE_TerminatesSession_204(t *testing.T) {
	s := newTestServer(t)
	resp1 := postJSON(t, s.URL+"/mcp", "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	sid := resp1.Header.Get("Mcp-Session-Id")
	resp1.Body.Close()

	req, _ := http.NewRequest("DELETE", s.URL+"/mcp", nil)
	req.Header.Set(HeaderSessionID, sid)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
}

func TestDELETE_MissingSession_400(t *testing.T) {
	s := newTestServer(t)
	req, _ := http.NewRequest("DELETE", s.URL+"/mcp", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHTTP_AuditEmitsSessionAndClientAddr(t *testing.T) {
	var auditBuf bytes.Buffer
	cfg := ServerConfig{
		Token:        "",
		Bind:         "127.0.0.1",
		Port:         0,
		MaxSessions:  16,
		IdleTimeout:  5 * time.Minute,
		ProtocolName: "yactt-test",
		Version:      "test",
		ProtocolVer:  ExpectedProtocol,
	}
	dir := t.TempDir()
	reg := registry.New(filepath.Join(dir, "projects.json"))
	srv := NewServer(cfg, reg)
	srv.WithAudit(audit.NewLogger(&auditBuf))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	resp1 := postJSON(t, ts.URL+"/mcp", "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	sid := resp1.Header.Get("Mcp-Session-Id")
	resp1.Body.Close()

	resp := postJSON(t, ts.URL+"/mcp", sid, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_projects","arguments":{}}}`)
	resp.Body.Close()

	var line map[string]any
	dec := json.NewDecoder(&auditBuf)
	if err := dec.Decode(&line); err != nil {
		t.Fatalf("decode audit line: %v\nbuffer: %q", err, auditBuf.String())
	}
	if line["session_id"] != sid {
		t.Fatalf("session_id = %v, want %s", line["session_id"], sid)
	}
	if line["client_addr"] == nil || line["client_addr"] == "" {
		t.Fatalf("client_addr missing from audit line: %v", line)
	}
}

func TestShutdown_CancelsSessions(t *testing.T) {
	cfg := ServerConfig{
		Token:        "",
		Bind:         "127.0.0.1",
		MaxSessions:  16,
		IdleTimeout:  5 * time.Minute,
		ProtocolName: "yactt-test",
		Version:      "test",
		ProtocolVer:  ExpectedProtocol,
	}
	dir := t.TempDir()
	reg := registry.New(filepath.Join(dir, "projects.json"))
	srv := NewServer(cfg, reg)
	sess, err := srv.sessions.Allocate("x")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		<-sess.Ctx().Done()
		close(done)
	}()
	srv.Shutdown(context.Background())
	select {
	case <-done:
		// ok
	case <-time.After(time.Second):
		t.Fatalf("Shutdown should cancel sessions")
	}
}

func TestShutdown_EmitsServerShutdownEventOnOpenStream(t *testing.T) {
	cfg := ServerConfig{
		Token:        "",
		Bind:         "127.0.0.1",
		MaxSessions:  16,
		IdleTimeout:  5 * time.Minute,
		ProtocolName: "yactt-test",
		Version:      "test",
		ProtocolVer:  ExpectedProtocol,
	}
	dir := t.TempDir()
	reg := registry.New(filepath.Join(dir, "projects.json"))
	srv := NewServer(cfg, reg)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	resp1 := postJSON(t, ts.URL+"/mcp", "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	sid := resp1.Header.Get("Mcp-Session-Id")
	resp1.Body.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/mcp", nil)
	req.Header.Set(HeaderSessionID, sid)
	req.Header.Set(HeaderProtocolVersion, ExpectedProtocol)
	streamResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer streamResp.Body.Close()

	go func() {
		time.Sleep(100 * time.Millisecond)
		srv.Shutdown(context.Background())
	}()

	body, _ := io.ReadAll(streamResp.Body)
	if !strings.Contains(string(body), "event: server_shutdown") {
		t.Fatalf("expected server_shutdown event in stream, got: %q", string(body))
	}
}

func postJSON(t *testing.T, url, sid, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if sid != "" {
		req.Header.Set(HeaderSessionID, sid)
		req.Header.Set(HeaderProtocolVersion, ExpectedProtocol)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}