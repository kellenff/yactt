package http

import (
	"context"
	"fmt"
	"io"
	"strings"
)

// SSEWriter writes Server-Sent Events in the wire format
// defined by the W3C SSE spec. Each WriteEvent produces:
//
//	event: <name>\n
//	data: <line1>\n
//	data: <line2>\n
//	\n
//
// Comments (`: keepalive\n\n`) are also supported.
type SSEWriter struct {
	w   io.Writer
	ctx context.Context
}

// newSSEWriter wraps w. The context defaults to context.Background().
func newSSEWriter(w io.Writer) *SSEWriter {
	return &SSEWriter{w: w, ctx: context.Background()}
}

// newSSEWriterWithContext wraps w with an explicit context;
// write operations check ctx.Err() before flushing.
func newSSEWriterWithContext(ctx context.Context, w io.Writer) *SSEWriter {
	return &SSEWriter{w: w, ctx: ctx}
}

// WriteEvent emits a typed event with the given data payload.
// Multi-line data is split into one `data:` line per source line.
func (s *SSEWriter) WriteEvent(event, data string) error {
	if s.ctx.Err() != nil {
		return s.ctx.Err()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "event: %s\n", event)
	for _, line := range strings.Split(data, "\n") {
		fmt.Fprintf(&b, "data: %s\n", line)
	}
	b.WriteByte('\n')
	_, err := io.WriteString(s.w, b.String())
	return err
}

// WriteComment emits a comment line (used for keepalives).
func (s *SSEWriter) WriteComment(text string) error {
	if s.ctx.Err() != nil {
		return s.ctx.Err()
	}
	_, err := fmt.Fprintf(s.w, ": %s\n\n", text)
	return err
}

// WriteShutdownEvent emits the spec-mandated final event on
// every open stream when the daemon is shutting down.
func (s *SSEWriter) WriteShutdownEvent() error {
	return s.WriteEvent("server_shutdown", `{"reason":"shutdown"}`)
}