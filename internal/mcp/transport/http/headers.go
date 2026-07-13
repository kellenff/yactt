// Package http implements the Streamable HTTP transport for MCP
// 2025-03-26. It owns the HTTP server, the session lifecycle, and
// the auth middleware. It does NOT implement JSON-RPC or
// dispatch — that's internal/mcp.Server's job. This package
// speaks JSON-RPC 2.0 frames into Server.dispatch per request.
//
// Surface:
//   POST   /mcp                    — JSON-RPC request
//   GET    /mcp                    — open SSE stream
//   DELETE /mcp                    — terminate session
//   GET    /healthz                — liveness probe (unauthenticated)
//
// One Server hosts one shared *mcp.Server per process. Tools
// resolve their project (file:// URI) per-call via the
// *registry.Registry passed at construction, so the daemon
// doesn't need a per-repo server cache.
package http

import (
	"net/http"
	"strings"
)

const (
	// HeaderSessionID is the per-session correlation header.
	// Server-set on initialize; client must echo on every
	// subsequent POST/GET/DELETE.
	HeaderSessionID = "Mcp-Session-Id"

	// HeaderProtocolVersion is the MCP protocol version
	// advertised by the client. Required on every request
	// after initialize.
	HeaderProtocolVersion = "Mcp-Protocol-Version"

	// ExpectedProtocol is the version this server speaks. Bump
	// when the transport gains or breaks wire-compat features.
	ExpectedProtocol = "2025-03-26"
)

// SessionIDFromRequest returns the trimmed Mcp-Session-Id
// header value, or "" if missing.
func SessionIDFromRequest(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get(HeaderSessionID))
}

// ProtocolVersionFromRequest returns the trimmed
// Mcp-Protocol-Version header value, or "" if missing.
func ProtocolVersionFromRequest(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get(HeaderProtocolVersion))
}

// AcceptsEventStream reports whether the request's Accept
// header includes text/event-stream. Per MCP spec, clients
// that set this header get an SSE response when the server
// supports it (we always do for POST).
func AcceptsEventStream(r *http.Request) bool {
	accept := r.Header.Get("Accept")
	if accept == "" {
		return false
	}
	for _, part := range strings.Split(accept, ",") {
		if strings.TrimSpace(part) == "text/event-stream" {
			return true
		}
	}
	return false
}