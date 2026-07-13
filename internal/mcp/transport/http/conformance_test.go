package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kellenff/yactt/internal/registry"
)

// TestConformance_FullClientTranscript exercises the canonical
// client session over the Streamable HTTP transport:
//
//	initialize → notifications/initialized → tools/list →
//	tools/call → DELETE.
//
// This is the spec-faithful shape that an MCP HTTP client walks
// through; the test doubles as documentation for the expected
// wire format.
func TestConformance_FullClientTranscript(t *testing.T) {
	fixturePath, _ := filepath.Abs("../../../../tests/fixtures/sample-go")
	dir := t.TempDir()
	reg := registry.New(filepath.Join(dir, "projects.json"))
	if err := reg.Upsert(registry.Entry{Path: fixturePath, Name: "fixture"}); err != nil {
		t.Fatal(err)
	}

	cfg := ServerConfig{
		Token:        "",
		Bind:         "127.0.0.1",
		Port:         0,
		MaxSessions:  16,
		IdleTimeout:  5 * time.Minute,
		ProtocolName: "yactt",
		Version:      "test",
		ProtocolVer:  ExpectedProtocol,
	}
	srv := NewServer(cfg, reg)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	// Step 1: initialize
	initResp := postJSON(t, ts.URL+"/mcp", "",
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"conformance","version":"1.0"}}}`)
	sid := initResp.Header.Get(HeaderSessionID)
	if sid == "" {
		t.Fatal("initialize did not return Mcp-Session-Id")
	}
	initResp.Body.Close()

	// Step 2: notifications/initialized (no id, expects 204)
	notifResp := postJSON(t, ts.URL+"/mcp", sid,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if notifResp.StatusCode != http.StatusNoContent {
		t.Fatalf("notification status = %d, want 204", notifResp.StatusCode)
	}
	notifResp.Body.Close()

	// Step 3: tools/list
	listResp := postJSON(t, ts.URL+"/mcp", sid,
		`{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{}}`)
	var listResult struct {
		Result struct {
			Tools []map[string]any `json:"tools"`
		} `json:"result"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&listResult); err != nil {
		t.Fatal(err)
	}
	listResp.Body.Close()
	if len(listResult.Result.Tools) < 20 {
		t.Fatalf("tools/list returned %d tools, want ≥20 (registry + code-intel + persisted_query)", len(listResult.Result.Tools))
	}

	// Step 4: tools/call list_projects
	callResp := postJSON(t, ts.URL+"/mcp", sid,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"list_projects","arguments":{}}}`)
	var callResult struct {
		Result struct {
			Content           []map[string]string `json:"content"`
			StructuredContent map[string]any      `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.NewDecoder(callResp.Body).Decode(&callResult); err != nil {
		t.Fatal(err)
	}
	callResp.Body.Close()
	if callResult.Result.StructuredContent == nil {
		t.Fatalf("tools/call result missing structuredContent: %+v", callResult)
	}

	// Step 5: DELETE
	delReq, _ := http.NewRequest("DELETE", ts.URL+"/mcp", nil)
	delReq.Header.Set(HeaderSessionID, sid)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatal(err)
	}
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204", delResp.StatusCode)
	}
	delResp.Body.Close()
}

// TestConformance_AcceptsEventStreamReturnsSSE verifies that a
// client setting Accept: text/event-stream gets an SSE response
// even for a synchronous tools/list call.
func TestConformance_AcceptsEventStreamReturnsSSE(t *testing.T) {
	cfg := ServerConfig{
		Token:        "",
		Bind:         "127.0.0.1",
		Port:         0,
		MaxSessions:  16,
		IdleTimeout:  5 * time.Minute,
		ProtocolName: "yactt",
		Version:      "test",
		ProtocolVer:  ExpectedProtocol,
	}
	dir := t.TempDir()
	reg := registry.New(filepath.Join(dir, "projects.json"))
	srv := NewServer(cfg, reg)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	resp1 := postJSON(t, ts.URL+"/mcp", "",
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	sid := resp1.Header.Get(HeaderSessionID)
	resp1.Body.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderSessionID, sid)
	req.Header.Set(HeaderProtocolVersion, ExpectedProtocol)
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
}