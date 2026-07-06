package lsp

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
)

// StartRust starts the `rust-analyzer` child process against `root` (an
// absolute filesystem path; converted to a file:// URI internally).
// Returns ErrUnavailable when the binary is not on PATH.
//
// `rust-analyzer` is the canonical LSP server for Rust, the
// official successor to the original RLS. It speaks the `rust` language
// id and indexes `.rs` files from a single workspace. Per-file language
// routing is handled at the *Repo* layer via the file extension; the
// server itself just needs `RootURI` to know where to look.
//
// Unlike `pyright-langserver` / `typescript-language-server`, rust-analyzer
// defaults to stdio framing — no `--stdio` flag is required. This mirrors
// the gopls shape, not the Python / TS shape, because the transport
// default differs across LSP servers.
func StartRust(ctx context.Context, root string, opts Options) (*Client, error) {
	if _, err := exec.LookPath("rust-analyzer"); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, ErrUnavailable
		}
		return nil, fmt.Errorf("lsp: lookpath rust-analyzer: %w", err)
	}

	opts.RootURI = fileURI(root)
	return StartCommand(ctx, []string{"rust-analyzer"}, opts)
}
