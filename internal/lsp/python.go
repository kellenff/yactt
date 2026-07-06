package lsp

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
)

// StartPython starts the `pyright-langserver` child process against `root`
// (an absolute filesystem path; converted to a file:// URI internally).
// Returns ErrUnavailable when the binary is not on PATH.
//
// `pyright-langserver` is the canonical LSP server for Python — Microsoft
// pyright, distributed both as the official `pyright` package and via
// community forks (basedpyright). It speaks the `python` language id and
// indexes `.py` and `.pyi` files from a single workspace. Per-file language
// routing is handled at the *Repo* layer via the file extension; the
// server itself just needs `RootURI` to know where to look.
//
// The `--stdio` flag forces stdio framing. Current versions default to a
// TCP socket and require the flag — we always pass it for forward
// compatibility.
func StartPython(ctx context.Context, root string, opts Options) (*Client, error) {
	if _, err := exec.LookPath("pyright-langserver"); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, ErrUnavailable
		}
		return nil, fmt.Errorf("lsp: lookpath pyright-langserver: %w", err)
	}

	opts.RootURI = fileURI(root)
	return StartCommand(ctx, []string{"pyright-langserver", "--stdio"}, opts)
}