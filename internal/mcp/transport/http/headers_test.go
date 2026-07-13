package http

import (
	"net/http"
	"testing"
)

func TestSessionIDFromRequest_EmptyWhenMissing(t *testing.T) {
	r, _ := http.NewRequest("POST", "/mcp", nil)
	if got := SessionIDFromRequest(r); got != "" {
		t.Fatalf("SessionIDFromRequest = %q, want empty", got)
	}
}

func TestSessionIDFromRequest_StripsWhitespace(t *testing.T) {
	r, _ := http.NewRequest("POST", "/mcp", nil)
	r.Header.Set("Mcp-Session-Id", "  abc-123  ")
	if got := SessionIDFromRequest(r); got != "abc-123" {
		t.Fatalf("SessionIDFromRequest = %q, want abc-123", got)
	}
}

func TestProtocolVersionFromRequest_EmptyWhenMissing(t *testing.T) {
	r, _ := http.NewRequest("POST", "/mcp", nil)
	if got := ProtocolVersionFromRequest(r); got != "" {
		t.Fatalf("ProtocolVersionFromRequest = %q, want empty", got)
	}
}

func TestAcceptsEventStream_FalseWhenMissing(t *testing.T) {
	r, _ := http.NewRequest("POST", "/mcp", nil)
	if AcceptsEventStream(r) {
		t.Fatalf("AcceptsEventStream should be false when Accept header missing")
	}
}

func TestAcceptsEventStream_TrueWhenPresent(t *testing.T) {
	r, _ := http.NewRequest("POST", "/mcp", nil)
	r.Header.Set("Accept", "application/json, text/event-stream")
	if !AcceptsEventStream(r) {
		t.Fatalf("AcceptsEventStream should be true when text/event-stream in Accept")
	}
}