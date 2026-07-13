package http

import (
	"bytes"
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSSEWriter_WriteEvent(t *testing.T) {
	rr := httptest.NewRecorder()
	w := newSSEWriter(rr)
	if err := w.WriteEvent("test", `{"hello":"world"}`); err != nil {
		t.Fatal(err)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "event: test\n") {
		t.Fatalf("missing event line: %q", body)
	}
	if !strings.Contains(body, "data: {\"hello\":\"world\"}\n") {
		t.Fatalf("missing data line: %q", body)
	}
	if !strings.HasSuffix(body, "\n\n") {
		t.Fatalf("missing terminator: %q", body)
	}
}

func TestSSEWriter_WriteComment(t *testing.T) {
	rr := httptest.NewRecorder()
	w := newSSEWriter(rr)
	if err := w.WriteComment("keepalive"); err != nil {
		t.Fatal(err)
	}
	body := rr.Body.String()
	if !strings.HasPrefix(body, ": keepalive\n\n") {
		t.Fatalf("comment format wrong: %q", body)
	}
}

func TestSSEWriter_MultiLineData(t *testing.T) {
	rr := httptest.NewRecorder()
	w := newSSEWriter(rr)
	if err := w.WriteEvent("e", "line1\nline2"); err != nil {
		t.Fatal(err)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "data: line1\n") {
		t.Fatalf("first line: %q", body)
	}
	if !strings.Contains(body, "data: line2\n") {
		t.Fatalf("second line: %q", body)
	}
}

func TestSSEWriter_ShutdownEvent(t *testing.T) {
	var buf bytes.Buffer
	w := newSSEWriter(&buf)
	if err := w.WriteShutdownEvent(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "event: server_shutdown") {
		t.Fatalf("missing shutdown event: %q", buf.String())
	}
}

func TestSSEWriter_ContextCancel(t *testing.T) {
	rr := httptest.NewRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	w := newSSEWriterWithContext(ctx, rr)
	cancel()
	err := w.WriteEvent("e", "x")
	if err == nil {
		t.Fatal("WriteEvent on cancelled ctx should return ctx.Err()")
	}
	if err != context.Canceled {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}