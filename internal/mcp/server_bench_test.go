package mcp

import (
	"context"
	"encoding/json"
	"testing"
)

// These benchmarks live in `package mcp` (not `package mcp_test`) so
// they can drive `dispatch` directly without going through Serve's
// read-loop. Ponytail: the existence of two test packages in one
// directory is fine — they coexist at compile time.

// noopHandler returns nothing. Used as a stub for tools/call so the
// bench measures dispatch overhead, not user-supplied work.
type noopHandler struct{}

func (noopHandler) Handler(_ context.Context, _ json.RawMessage) (any, error) {
	return map[string]any{"ok": true}, nil
}

func benchServer(b *testing.B) *Server {
	s := NewServer("yactt-bench", "v0.0.0", ProtocolVersion, nil, nil)
	for i := 0; i < 16; i++ {
		s.RegisterTool(ToolDef{
			Name:         "stub_" + itoa(i),
			Description:  "bench stub",
			InputSchema:  json.RawMessage(`{"type":"object","properties":{}}`),
			OutputSchema: json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}}}`),
			Handler:      noopHandler{}.Handler,
		})
	}
	return s
}

// itoa avoids importing strconv for a single micro-bench helper.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := [20]byte{}
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}

// BenchmarkDispatchToolsCall measures the per-request hot path: parse
// params, look up the tool in the registry, invoke the handler, frame
// the structuredContent response. Multiplied by every agent turn.
func BenchmarkDispatchToolsCall(b *testing.B) {
	s := benchServer(b)
	req := Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"stub_0","arguments":{}}`),
	}
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.dispatch(ctx, req)
	}
}

// BenchmarkDispatchToolsList measures the tools/list path the client
// fires on connect (and on `list_changed` notifications).
func BenchmarkDispatchToolsList(b *testing.B) {
	s := benchServer(b)
	req := Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/list",
	}
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.dispatch(ctx, req)
	}
}

// BenchmarkRegisterTool exercises the registry insert + OutputSchema
// validation. Cold-path; one-time per server. Captures the cost of
// adding the 11th tool on a fresh MCP server start.
func BenchmarkRegisterTool(b *testing.B) {
	toolDef := ToolDef{
		Name:         "bench",
		Description:  "bench",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{}}`),
		OutputSchema: json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}}}`),
		Handler:      noopHandler{}.Handler,
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Ponytail: NewServer per iter so RegisterTool doesn't trip the
		// duplicate-name panic. NewServer is sub-µs.
		s := NewServer("yactt-bench", "v0.0.0", ProtocolVersion, nil, nil)
		s.RegisterTool(toolDef)
	}
}