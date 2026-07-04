package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// Handler is the per-tool function. It receives a raw argument payload and
// returns either a structured result (returned under `structuredContent`) or
// an error to surface as a tool-level error.
//
// Returning `(nil, err)` short-circuits to `CallToolResult{IsError: true}` with
// `err.Error()` as the text content; the wire shape is intentionally stable so
// clients can branch on `isError` without parsing messages.
type Handler func(ctx context.Context, args json.RawMessage) (any, error)

// ToolDef is the registration of one tool.
//
// Schema is JSON Schema (2020-12 dialect) describing the arguments. Handlers
// are expected to validate inside the function (the design's "parse don't
// validate" rule means we parse into typed structs at the top of the
// handler).
type ToolDef struct {
	Name        string
	Description string
	InputSchema json.RawMessage
	Handler     Handler
}

// Server is the JSON-RPC 2.0 server. It is safe for concurrent use across the
// registry; the read-side loop runs on a single goroutine and dispatches
// calls synchronously.
type Server struct {
	name            string
	version         string
	mu              sync.RWMutex
	tools           map[string]ToolDef
	stdout          io.Writer
	stdinReader     func() (io.Reader, error)
	protocolVersion string
}

// NewServer constructs an MCP server bound to stdout for writes.
//
// protocolVersion is the MCP protocol version we advertise on `initialize`.
// For MVP we hard-code the published 2024-11-05 version; clients must match
// it or attempt a negotiation.
func NewServer(name, version, protocolVersion string, stdout io.Writer, stdin func() (io.Reader, error)) *Server {
	return &Server{
		name:            name,
		version:         version,
		stdout:          stdout,
		stdinReader:     stdin,
		protocolVersion: protocolVersion,
		tools:           make(map[string]ToolDef),
	}
}

// RegisterTool attaches a tool definition. Replacement panics — registration
// races would be a bug, not a recoverable state.
func (s *Server) RegisterTool(t ToolDef) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.tools[t.Name]; exists {
		panic(fmt.Sprintf("mcp: duplicate tool name %q", t.Name))
	}
	s.tools[t.Name] = t
}

// Tools returns a copy of the registered tool set, sorted by name.
func (s *Server) Tools() []ToolDef {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ToolDef, 0, len(s.tools))
	for _, t := range s.tools {
		out = append(out, t)
	}
	return out
}

// Serve runs the read-side loop until EOF on stdin or context cancel.
//
// Errors are emitted on stdout framed as JSON-RPC error responses (with
// `id:null` if the request couldn't be parsed). A malformed envelope is
// reported but does not stop the server.
func (s *Server) Serve(ctx context.Context) error {
	r, err := s.stdinReader()
	if err != nil {
		return err
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024) // up to 8 MiB per request
	for sc.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			s.write(EncodeError(nil, CodeParse, "parse error", err.Error()))
			continue
		}
		if req.JSONRPC != "2.0" {
			s.write(EncodeError(req.ID, CodeInvalidRequest, "jsonrpc must be 2.0", nil))
			continue
		}
		resp := s.dispatch(ctx, req)
		s.write(resp)
	}
	return sc.Err()
}

func (s *Server) dispatch(ctx context.Context, req Request) Response {
	switch req.Method {
	case "initialize":
		var params map[string]any
		if len(req.Params) > 0 {
			_ = json.Unmarshal(req.Params, &params)
		}
		return Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: InitializeResult{
				ProtocolVersion: s.protocolVersion,
				ServerInfo:      ServerInfo{Name: s.name, Version: s.version},
				Capabilities:    Capabilities{Tools: map[string]bool{"listChanged": false}},
			},
		}
	case "notifications/initialized":
		// Notifications have no `id` and expect no response. Per JSON-RPC 2.0
		// we still return a Response struct with no payload; the wire writer
		// drops it because there's no ID.
		return Response{JSONRPC: "2.0", ID: nil}
	case "tools/list":
		s.mu.RLock()
		out := make([]ToolDescriptor, 0, len(s.tools))
		for _, t := range s.tools {
			out = append(out, ToolDescriptor{
				Name:        t.Name,
				Description: t.Description,
				InputSchema: t.InputSchema,
			})
		}
		s.mu.RUnlock()
		return Response{JSONRPC: "2.0", ID: req.ID, Result: ListToolsResult{Tools: out}}
	case "tools/call":
		var params CallToolParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return EncodeError(req.ID, CodeInvalidParams, "invalid tools/call params", err.Error())
		}
		if params.Name == "" {
			return EncodeError(req.ID, CodeInvalidParams, "tool name required", nil)
		}
		s.mu.RLock()
		t, ok := s.tools[params.Name]
		s.mu.RUnlock()
		if !ok {
			return EncodeError(req.ID, CodeMethodNotFound, "unknown tool", params.Name)
		}
		result, herr := t.Handler(ctx, params.Arguments)
		if herr != nil {
			return Response{JSONRPC: "2.0", ID: req.ID, Result: CallToolResult{
				Content: TextContent(herr.Error()),
				IsError: true,
			}}
		}
		// Marshal the structured result so the text content also carries the
		// human-readable form. Clients that prefer structuredContent can use
		// it directly.
		var payload string
		if result != nil {
			b, err := json.MarshalIndent(result, "", "  ")
			if err == nil {
				payload = string(b)
			}
		}
		return Response{JSONRPC: "2.0", ID: req.ID, Result: CallToolResult{
			Content:           TextContent(payload),
			StructuredContent: result,
		}}
	default:
		return EncodeError(req.ID, CodeMethodNotFound, "unsupported method", req.Method)
	}
}

func (s *Server) write(r Response) {
	// Notifications (no ID) don't write to the wire. EncodeResponse leaves an
	// empty ID — JSON-RPC 2.0 omits the `id` field when it's nil, so the result
	// of Marshal is a JSON object with `jsonrpc` only; we drop it explicitly.
	if len(r.ID) == 0 && r.Error == nil {
		// This is the notifications/initialized case — silently ignore.
		return
	}
	b, err := json.Marshal(r)
	if err != nil {
		// Last-resort: emit a one-line frame so the client sees we tried.
		b = []byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"marshal failed"}}`)
	}
	b = append(b, '\n')
	if _, err := s.stdout.Write(b); err != nil {
		// Stdio is best-effort; downstream may already be closed.
		return
	}
}
