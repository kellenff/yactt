package lsp

import (
	"context"
	"errors"
)

// ErrUnavailable is returned by `Start`/`gopls.Start` when the language
// server binary cannot be located on PATH. The presence of this sentinel lets
// callers distinguish "LSP could not even start" from "LSP started but a
// request failed" — the former maps to `FallbackUsed: "no-lsp-installed"`,
// the latter to `"lsp-timeout"` / `"lsp-error"`.
//
// Per design §5.5 the system runs without LSP today; tree-sitter is the
// unconditional floor.
var ErrUnavailable = errors.New("lsp: server not on PATH")

// ErrTimeout is returned when an LSP request exceeds `Options.Timeout`.
// Callers map this to `FallbackUsed: "lsp-timeout"` so consumers can detect
// a hung server and retry.
var ErrTimeout = errors.New("lsp: request timeout")

// ErrClosed is returned when a request is sent after `Close`. Idempotent
// reads of the error from concurrent callers are fine.
var ErrClosed = errors.New("lsp: client closed")

// FallbackReason maps an LSP request error to the canonical
// `Provenance.FallbackUsed` string. Centralized so the policy stays in
// one place: timeout maps to `"lsp-timeout"`, any other non-nil error
// maps to `"lsp-error"`, a nil error maps to `""`.
func FallbackReason(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
		return "lsp-timeout"
	}
	return "lsp-error"
}
