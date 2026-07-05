// Package mcp implements the JSON-RPC 2.0 transport for the Model Context
// Protocol. For MVP we speak stdio only — the wire format is exactly what
// the spec describes, so a downstream consumer (Claude Desktop, an editor
// plugin, a test harness) can talk to us without ceremony.
//
// Three messages are recognised on the request side:
//   - initialize          — handshake; returns server info + capabilities.
//   - tools/list          — returns the registered tools + JSON Schemas.
//   - tools/call          — invokes one tool by name with arguments.
//
// Other methods return an error via JSON-RPC 2.0's standard error shape.
package mcp

import (
	"encoding/json"
)

// JSON-RPC 2.0 request envelope.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// JSON-RPC 2.0 success response.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is the JSON-RPC 2.0 error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Standard JSON-RPC 2.0 error codes; tool-specific errors use the -32603
// range and add detail in `Data`.
const (
	CodeParse          = -32700 // Parse error
	CodeInvalidRequest = -32600 // Invalid Request
	CodeMethodNotFound = -32601 // Method not found
	CodeInvalidParams  = -32602 // Invalid params
	CodeInternalError  = -32603 // Internal error
)

// InitializeResult is the payload returned from `initialize`.
type InitializeResult struct {
	ProtocolVersion string       `json:"protocolVersion"`
	ServerInfo      ServerInfo   `json:"serverInfo"`
	Capabilities    Capabilities `json:"capabilities"`
}

// ServerInfo identifies the server. The Name matches the design's repo
// convention so MCP clients can correlate.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Capabilities enumerates the feature groups this server exposes.
// For MVP we advertise `tools` only; `prompts`/`resources` are not provided.
type Capabilities struct {
	Tools map[string]bool `json:"tools,omitempty"`
}

// ToolDescriptor is the entry returned in `tools/list` — name + description +
// JSON Schema for arguments AND the structuredContent return shape.
type ToolDescriptor struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	OutputSchema json.RawMessage `json:"outputSchema"`
}

// ListToolsResult is the response to `tools/list`.
type ListToolsResult struct {
	Tools []ToolDescriptor `json:"tools"`
}

// CallToolParams is the body of a `tools/call` request.
type CallToolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// CallToolResult is the body of a successful `tools/call` reply.
//
// Per MCP spec, `Content` is an array of structured parts. We model everything
// as `type:"text"` parts with JSON-encoded payloads so the JSON shape under
// "structuredContent" stays machine-readable downstream.
type CallToolResult struct {
	Content           []Content `json:"content"`
	StructuredContent any       `json:"structuredContent,omitempty"`
	IsError           bool      `json:"isError,omitempty"`
}

// Content is one part of a CallToolResult.Content array.
type Content struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// TextContent is a small constructor — most callers want a single text part.
func TextContent(s string) []Content { return []Content{{Type: "text", Text: s}} }

// EncodeResponse marshals a Response to JSON for framing on the wire.
func EncodeResponse(r Response) ([]byte, error) {
	return json.Marshal(r)
}

// EncodeError builds a JSON-RPC 2.0 error response. ID may be nil for
// parse-level errors.
func EncodeError(id json.RawMessage, code int, msg string, data any) Response {
	return Response{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &RPCError{Code: code, Message: msg, Data: data},
	}
}
