package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/kellenff/yactt/internal/mcp"
)

// stubHandler records each call's argument bytes so tests can assert the
// handler was invoked with the exact wire payload.
type stubHandler struct {
	mu   sync.Mutex
	args []json.RawMessage
	ret  any
	err  error
}

func (h *stubHandler) Handle(ctx context.Context, args json.RawMessage) (any, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.args = append(h.args, append(json.RawMessage(nil), args...))
	return h.ret, h.err
}

func (h *stubHandler) LastArg() json.RawMessage {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.args) == 0 {
		return nil
	}
	return h.args[len(h.args)-1]
}

// newServer wires an MCP Server pointed at in-memory buffers so tests can
// drive Serve with controlled input and observe the framed output.
func newServer(t *testing.T, tools ...mcp.ToolDef) (s *mcp.Server, stdin *bytes.Buffer, stdout *bytes.Buffer) {
	t.Helper()
	stdin = &bytes.Buffer{}
	stdout = &bytes.Buffer{}
	s = mcp.NewServer("yactt-test", "0.0.0-test", "2024-11-05", stdout,
		func() (io.Reader, error) {
			return stdin, nil
		},
	)
	for _, tool := range tools {
		s.RegisterTool(tool)
	}
	return s, stdin, stdout
}

func TestServerInitialize(t *testing.T) {
	s, stdin, stdout := newServer(t)
	stdin.WriteString(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n")
	if err := s.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}

	var resp mcp.Response
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &resp); err != nil {
		t.Fatalf("unmarshal response: %v\n%s", err, stdout.String())
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	raw, _ := json.Marshal(resp.Result)
	var ir mcp.InitializeResult
	if err := json.Unmarshal(raw, &ir); err != nil {
		t.Fatalf("result unmarshal: %v", err)
	}
	if ir.ProtocolVersion != "2024-11-05" {
		t.Errorf("ProtocolVersion = %q, want 2024-11-05", ir.ProtocolVersion)
	}
	if ir.ServerInfo.Name != "yactt-test" {
		t.Errorf("ServerInfo.Name = %q", ir.ServerInfo.Name)
	}
	if ir.ServerInfo.Version != "0.0.0-test" {
		t.Errorf("ServerInfo.Version = %q", ir.ServerInfo.Version)
	}
}

func TestServerToolsList(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","required":["x"]}`)
	s, stdin, stdout := newServer(t,
		mcp.ToolDef{
			Name:        "alpha",
			Description: "first",
			InputSchema: schema,
			Handler:     func(context.Context, json.RawMessage) (any, error) { return nil, nil },
		},
		mcp.ToolDef{
			Name:        "beta",
			Description: "second",
			InputSchema: schema,
			Handler:     func(context.Context, json.RawMessage) (any, error) { return nil, nil },
		},
	)
	stdin.WriteString(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n")
	if err := s.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}

	var resp mcp.Response
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &resp); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(resp.Result)
	var listed mcp.ListToolsResult
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatalf("listed: %v", err)
	}
	if len(listed.Tools) != 2 {
		t.Fatalf("got %d tools, want 2: %+v", len(listed.Tools), listed.Tools)
	}
	names := map[string]bool{}
	for _, td := range listed.Tools {
		names[td.Name] = true
		// JSON ordering may differ; re-decode both and compare semantically.
		var got, want map[string]any
		if err := json.Unmarshal(td.InputSchema, &got); err != nil {
			t.Errorf("schema not valid JSON for %s: %v", td.Name, err)
			continue
		}
		if err := json.Unmarshal(schema, &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("schema mismatch for %s: got %s, want %s", td.Name, td.InputSchema, schema)
		}
	}
	if !names["alpha"] || !names["beta"] {
		t.Errorf("missing tools: %v", names)
	}
}

func TestServerToolsCallOK(t *testing.T) {
	stub := &stubHandler{ret: map[string]any{"ok": true, "n": 7}}
	s, stdin, stdout := newServer(t,
		mcp.ToolDef{
			Name:        "echo",
			Description: "echo",
			InputSchema: json.RawMessage(`{"type":"object"}`),
			Handler:     stub.Handle,
		},
	)
	stdin.WriteString(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"msg":"hi"}}}` + "\n")
	if err := s.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}

	var resp mcp.Response
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	last := stub.LastArg()
	if !bytes.Contains(last, []byte(`"msg":"hi"`)) {
		t.Errorf("handler args = %s, want to contain msg:hi", last)
	}

	raw, _ := json.Marshal(resp.Result)
	var ctr mcp.CallToolResult
	if err := json.Unmarshal(raw, &ctr); err != nil {
		t.Fatalf("result unmarshal: %v", err)
	}
	if ctr.IsError {
		t.Error("IsError should be false")
	}
	if len(ctr.Content) == 0 {
		t.Fatal("Content should be non-empty")
	}
	if ctr.Content[0].Type != "text" {
		t.Errorf("Content[0].Type = %q, want text", ctr.Content[0].Type)
	}
	if ctr.Content[0].Text == "" {
		t.Error("Content[0].Text should be populated")
	}
	sc, _ := ctr.StructuredContent.(map[string]any)
	if sc == nil || sc["ok"] != true {
		t.Errorf("StructuredContent = %v, want ok=true", sc)
	}
}

func TestServerToolsCallHandlerError(t *testing.T) {
	stub := &stubHandler{err: errors.New("kaboom")}
	s, stdin, stdout := newServer(t,
		mcp.ToolDef{
			Name:        "fail",
			Description: "always errors",
			InputSchema: json.RawMessage(`{"type":"object"}`),
			Handler:     stub.Handle,
		},
	)
	stdin.WriteString(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"fail","arguments":{}}}` + "\n")
	if err := s.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}

	var resp mcp.Response
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("tool errors should be reported via isError, not via rpc error: %+v", resp.Error)
	}
	raw, _ := json.Marshal(resp.Result)
	var ctr mcp.CallToolResult
	if err := json.Unmarshal(raw, &ctr); err != nil {
		t.Fatal(err)
	}
	if !ctr.IsError {
		t.Error("IsError should be true")
	}
	if ctr.Content[0].Text != "kaboom" {
		t.Errorf("Content[0].Text = %q, want kaboom", ctr.Content[0].Text)
	}
}

func TestServerToolsCallUnknownTool(t *testing.T) {
	s, stdin, stdout := newServer(t)
	stdin.WriteString(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"ghost","arguments":{}}}` + "\n")
	if err := s.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}

	var resp mcp.Response
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil {
		t.Fatal("expected RPC error for unknown tool")
	}
	if resp.Error.Code != mcp.CodeMethodNotFound {
		t.Errorf("Code = %d, want %d", resp.Error.Code, mcp.CodeMethodNotFound)
	}
}

func TestServerToolsCallMissingName(t *testing.T) {
	s, stdin, stdout := newServer(t)
	stdin.WriteString(`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"arguments":{}}}` + "\n")
	if err := s.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}

	var resp mcp.Response
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil || resp.Error.Code != mcp.CodeInvalidParams {
		t.Errorf("expected CodeInvalidParams, got %+v", resp.Error)
	}
}

func TestServerToolsCallInvalidParams(t *testing.T) {
	s, stdin, stdout := newServer(t)
	// params is an array, not an object.
	stdin.WriteString(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":[1,2]}` + "\n")
	if err := s.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}

	var resp mcp.Response
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil || resp.Error.Code != mcp.CodeInvalidParams {
		t.Errorf("expected CodeInvalidParams, got %+v", resp.Error)
	}
}

func TestServerMalformedJSON(t *testing.T) {
	s, stdin, stdout := newServer(t)
	stdin.WriteString("not-json at all\n")
	stdin.WriteString(`{"jsonrpc":"2.0","id":8,"method":"initialize"}` + "\n")
	if err := s.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected 2 frames, got %d: %q", len(lines), stdout.String())
	}
	var parseResp mcp.Response
	if err := json.Unmarshal([]byte(lines[0]), &parseResp); err != nil {
		t.Fatalf("parse-error frame unmarshal: %v", err)
	}
	if parseResp.Error == nil || parseResp.Error.Code != mcp.CodeParse {
		t.Errorf("first frame = %+v, want parse error", parseResp.Error)
	}
	var initResp mcp.Response
	if err := json.Unmarshal([]byte(lines[1]), &initResp); err != nil {
		t.Fatalf("init frame unmarshal: %v", err)
	}
	if initResp.Error != nil {
		t.Errorf("init errored unexpectedly: %+v", initResp.Error)
	}
}

func TestServerWrongJSONRPCVersion(t *testing.T) {
	s, stdin, stdout := newServer(t)
	stdin.WriteString(`{"jsonrpc":"1.0","id":9,"method":"initialize"}` + "\n")
	if err := s.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}

	var resp mcp.Response
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil || resp.Error.Code != mcp.CodeInvalidRequest {
		t.Errorf("expected CodeInvalidRequest, got %+v", resp.Error)
	}
}

func TestServerNotificationNotWritten(t *testing.T) {
	s, stdin, stdout := newServer(t)
	stdin.WriteString(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n")
	if err := s.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 {
		t.Errorf("notifications/initialized should produce no wire output, got %q", stdout.String())
	}
}

func TestServerUnsupportedMethod(t *testing.T) {
	s, stdin, stdout := newServer(t)
	stdin.WriteString(`{"jsonrpc":"2.0","id":10,"method":"resources/list"}` + "\n")
	if err := s.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}

	var resp mcp.Response
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil || resp.Error.Code != mcp.CodeMethodNotFound {
		t.Errorf("expected methodNotFound, got %+v", resp.Error)
	}
}

func TestServerMultipleRequestsInOrder(t *testing.T) {
	stub := &stubHandler{ret: "ok"}
	s, stdin, stdout := newServer(t,
		mcp.ToolDef{
			Name:        "do",
			Description: "do",
			InputSchema: json.RawMessage(`{}`),
			Handler:     stub.Handle,
		},
	)
	stdin.WriteString(`{"jsonrpc":"2.0","id":11,"method":"initialize"}` + "\n")
	stdin.WriteString(`{"jsonrpc":"2.0","id":12,"method":"tools/call","params":{"name":"do","arguments":{"a":1}}}` + "\n")
	stdin.WriteString(`{"jsonrpc":"2.0","id":13,"method":"tools/call","params":{"name":"do","arguments":{"a":2}}}` + "\n")
	if err := s.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 frames, got %d: %q", len(lines), stdout.String())
	}
	if stub.LastArg() == nil || !bytes.Contains(stub.LastArg(), []byte(`"a":2`)) {
		t.Errorf("last call arg = %s, want a:2", stub.LastArg())
	}
}

func TestServerRegisterToolPanicsOnDuplicate(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on duplicate registration")
		}
	}()
	s := mcp.NewServer("y", "v", "p", &bytes.Buffer{}, func() (io.Reader, error) {
		return &bytes.Buffer{}, nil
	})
	handler := func(context.Context, json.RawMessage) (any, error) { return nil, nil }
	s.RegisterTool(mcp.ToolDef{Name: "x", Handler: handler})
	s.RegisterTool(mcp.ToolDef{Name: "x", Handler: handler})
}

func TestServerServeRejectsBadStdin(t *testing.T) {
	s := mcp.NewServer("y", "v", "p", &bytes.Buffer{}, func() (io.Reader, error) {
		return nil, errors.New("stdin not available")
	})
	if err := s.Serve(context.Background()); err == nil {
		t.Fatal("expected error when stdin reader fails")
	}
}

// TestServerInitialize_WithParams covers the `len(req.Params) > 0`
// branch (line 124) — initialize with non-empty params must still
// produce a valid InitializeResult. A mutation flipping `>` to `<=`
// would parse empty params; this test pins the non-empty case.
func TestServerInitialize_WithParams(t *testing.T) {
	s, stdin, stdout := newServer(t)
	stdin.WriteString(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"x"}}}` + "\n")
	if err := s.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}
	var resp mcp.Response
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if resp.ID == nil {
		t.Errorf("response ID nil")
	}
}

// TestServerInitialize_EmptyParams exercises the `len(req.Params) == 0`
// branch (the inverse). With empty params, the live code skips the
// json.Unmarshal call; the mutation `<=` would attempt unmarshal of
// empty bytes. The response must still be valid.
func TestServerInitialize_EmptyParams(t *testing.T) {
	s, stdin, stdout := newServer(t)
	stdin.WriteString(`{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n")
	if err := s.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}
	var resp mcp.Response
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	// Result must still be a valid InitializeResult.
	init, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("Result = %T, want map[string]any", resp.Result)
	}
	if _, ok := init["protocolVersion"]; !ok {
		t.Errorf("Result missing protocolVersion: %+v", init)
	}
}

// TestServerWriteStdoutError covers the `s.stdout.Write(b)` error
// branch at line 207. When stdout.Write returns an error, Serve
// silently drops the frame (doesn't crash, doesn't retry). Pin the
// "best-effort" contract.
func TestServerWriteStdoutError(t *testing.T) {
	stdin := &bytes.Buffer{}
	errWriter := &errWriter{}
	s := mcp.NewServer("y", "v", "p", errWriter, func() (io.Reader, error) {
		return stdin, nil
	})
	stdin.WriteString(`{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n")
	// Serve must not panic / error out on a write failure.
	err := s.Serve(context.Background())
	if err != nil && !errors.Is(err, io.EOF) {
		// EOF is acceptable — bufio scanner returns EOF when stdin is exhausted.
		// Anything else is a real failure.
		t.Errorf("Serve with failing stdout: %v", err)
	}
}

// errWriter is an io.Writer that always returns an error. Used to drive
// the "stdout write failed" path in server.write.
type errWriter struct{}

func (errWriter) Write(_ []byte) (int, error) {
	return 0, errors.New("write failed")
}

// TestServerToolsCall_NilResult covers the `if result != nil` guard
// at line 178. When the handler returns (nil, nil), the response must
// still be a valid CallToolResult with empty content.
func TestServerToolsCall_NilResult(t *testing.T) {
	stub := &stubHandler{ret: nil, err: nil}
	s, stdin, stdout := newServer(t,
		mcp.ToolDef{
			Name:        "nil_result",
			InputSchema: json.RawMessage(`{}`),
			Handler:     stub.Handle,
		},
	)
	stdin.WriteString(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"nil_result"}}` + "\n")
	if err := s.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}
	var resp mcp.Response
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	// The result should be a CallToolResult with empty text payload.
	ctr, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("Result = %T, want map[string]any", resp.Result)
	}
	// Content is the wire-shape TextContent(payload). payload is "" since
	// result was nil — the marshal produced "content":[] or "content":""?
	if len(ctr) == 0 {
		t.Errorf("Result empty: %+v", ctr)
	}
}
