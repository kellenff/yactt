package lsp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"path/filepath"
	"time"
)

// Start starts a gopls child process against `root` (an absolute filesystem
// path; converted to a file:// URI internally).
//
// Returns ErrUnavailable when `gopls` is not on PATH. Any other error
// originates in `New` (child failed to start, handshake errored, etc).
func Start(ctx context.Context, root string, opts Options) (*Client, error) {
	if _, err := exec.LookPath("gopls"); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, ErrUnavailable
		}
		return nil, fmt.Errorf("lsp: lookpath gopls: %w", err)
	}

	opts.RootURI = fileURI(root)
	return StartCommand(ctx, []string{"gopls"}, opts)
}

// StartCommand is the lower-level entry point: runs an arbitrary command
// (typically just `["gopls"]`, but tests pass a stub binary) and performs
// the handshake. Useful for collaboration tests where we want to inject a
// fake server without touching PATH.
//
// The child inherits the parent's stderr by default. Pass `withStderr:
// true` via Options to capture stderr into opts.Logf instead (useful for
// unit tests).
func StartCommand(ctx context.Context, argv []string, opts Options) (*Client, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	var stderrR io.Reader
	if opts.CaptureStderr {
		r, _ := cmd.StderrPipe()
		stderrR = r
	}

	c, err := New(ctx, cmd, stdin, stdout, stderrR, opts)
	if err != nil {
		// Close the pipes we just opened if New didn't take ownership of them.
		_ = stdin.Close()
		_ = stdout.Close()
		if stderrR != nil {
			if cl, ok := stderrR.(io.Closer); ok {
				_ = cl.Close()
			}
		}
		return nil, err
	}
	return c, nil
}

// fileURI returns the file:// URI form of path. Used to populate RootURI
// and per-file TextDocument identifiers.
//
// Cleaning the path before concatenation matters: a path with `..`,
// `/./`, or trailing `/` would otherwise produce a malformed URI that
// LSP servers parse inconsistently (some treat `..` literally, some
// resolve, some reject). Reserved characters — spaces, `#`, `?`,
// and others the URI grammar treats specially — are percent-encoded
// by `net/url` per RFC 3986 so paths containing them stay valid URIs.
func fileURI(path string) string {
	cleaned := filepath.ToSlash(filepath.Clean(path))
	u := url.URL{Scheme: "file", Path: cleaned}
	return u.String()
}

// OpenFile is the minimum information `OpenWorkspace` needs for each
// file it should hand to gopls: the on-disk path, the language id
// (gopls only cares about "go" today, but the field is part of the LSP
// `TextDocumentItem` spec so we keep it explicit), and the contents.
type OpenFile struct {
	Path     string
	Language string
	Text     string
}

// OpenWorkspaceOptions configures the eager-warm pass that OpenWorkspace
// runs at Load time.
type OpenWorkspaceOptions struct {
	// PerFileTimeout caps how long a single documentSymbol round-trip
	// can take. Defaults to the client's per-request timeout, which
	// is too short for a cold gopls (the first request against an
	// unindexed file routinely takes 1–3 s). Callers should set this
	// to something generous like 5 s when warming a large workspace.
	PerFileTimeout time.Duration
	// Logf is the optional diagnostic sink; nil disables logging.
	Logf func(string, ...any)
}

// OpenWorkspace eagerly warms gopls's in-memory index for every file
// in `files` so subsequent `textDocument/hover` / `textDocument/references`
// requests hit a warm cache.
//
// The LSP spec lets a server lazily index on first request, but gopls's
// first request against a cold file takes ~1 s while it parses and
// type-checks. That blows past our 500 ms per-request budget. So this
// helper does two things per file:
//
//  1. Sends `textDocument/didOpen` so gopls knows the file exists.
//  2. Sends a follow-up `textDocument/documentSymbol` request. This
//     forces gopls to fully process the file before returning; once
//     it does, subsequent hovers/references on the same file return
//     in <10 ms.
//
// If the documentSymbol round-trip exceeds the per-file timeout we
// log and move on — the file will be lazily indexed on its first
// real request, and a partial warm-up is still strictly better than
// none. Files whose didOpen failed are not counted.
//
// Notifications and requests are issued sequentially. gopls handles
// them serially internally anyway, so parallelism doesn't help.
//
// Returns the number of files whose documentSymbol round-trip
// succeeded (a positive lower bound on the warm-up).
func OpenWorkspace(ctx context.Context, client *Client, files []OpenFile, opts OpenWorkspaceOptions) int {
	if client == nil {
		return 0
	}
	perFile := opts.PerFileTimeout
	if perFile <= 0 {
		perFile = client.RequestTimeout()
	}
	logf := opts.Logf
	warmed := 0
	for _, f := range files {
		if f.Path == "" || f.Text == "" {
			continue
		}
		uri := fileURI(f.Path)
		lang := f.Language
		if lang == "" {
			ext := filepath.Ext(f.Path)
			if ext == "" {
				continue
			}
			lang = ext[1:]
		}
		params := DidOpenTextDocumentParams{
			TextDocument: TextDocumentItem{
				URI:        uri,
				LanguageID: lang,
				Version:    1,
				Text:       f.Text,
			},
		}
		if err := client.Notify(ctx, "textDocument/didOpen", params); err != nil {
			if logf != nil {
				logf("lsp: didOpen %s: %v", f.Path, err)
			}
			continue
		}
		// Force gopls to finish indexing. We don't care about the
		// payload — we only care that it returned successfully, which
		// implies the file is fully processed. Each round-trip has
		// its own per-file deadline so a single slow file can't
		// block the rest of the warm-up — and we use the explicit
		// timeout argument so the client's default 500 ms guard
		// doesn't clip it.
		var syms interface{}
		err := client.RequestWithDeadline(ctx, "textDocument/documentSymbol", DocumentSymbolParams{
			TextDocument: TextDocumentIdentifier{URI: uri},
		}, &syms, perFile)
		if err != nil {
			if logf != nil {
				logf("lsp: warm %s: %v", f.Path, err)
			}
			continue
		}
		warmed++
	}
	return warmed
}
