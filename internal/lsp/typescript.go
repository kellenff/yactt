package lsp

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
)

// StartTypeScript starts the `typescript-language-server` child process
// against `root` (an absolute filesystem path; converted to a file:// URI
// internally). Returns ErrUnavailable when the binary is not on PATH.
//
// `typescript-language-server` is the canonical LSP server for TypeScript
// and JavaScript — it speaks both languages, advertises `typescript` and
// `javascript` capabilities, and indexes `.ts`/`.tsx`/`.js`/`.jsx` (and the
// `.mts`/`.cts`/`.mjs`/`.cjs` ES-module variants) from a single workspace.
// Per-file language routing is handled at the *Repo* layer via the file
// extension; the server itself just needs `RootURI` to know where to look.
//
// The `--stdio` flag forces stdio framing. Older versions defaulted to
// stdio anyway; current versions default to a TCP socket and require the
// flag — we always pass it for forward compatibility.
func StartTypeScript(ctx context.Context, root string, opts Options) (*Client, error) {
	if _, err := exec.LookPath("typescript-language-server"); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, ErrUnavailable
		}
		return nil, fmt.Errorf("lsp: lookpath typescript-language-server: %w", err)
	}

	opts.RootURI = fileURI(root)
	return StartCommand(ctx, []string{"typescript-language-server", "--stdio"}, opts)
}