// Package project parses the on-wire project reference (a file://
// URI) and resolves it to a loaded *store.Repo via the registry
// and the per-project disk cache.
//
// The package is the seam between MCP dispatch (where every
// targeting tool receives a `project` field) and repo loading
// (where store.Load reads files and primes the disk cache). Tools
// call project.Resolve rather than store.Load directly so URI
// validation, registry lookup, and cache wiring all live in one
// place.
package project

import (
	"errors"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/kellenff/yactt/internal/registry"
	"github.com/kellenff/yactt/internal/store"
)

// Ref is a parsed file:// URI. The wrapper exists so future
// schemes (git://, https://) can grow without a v2 wire shape;
// today the only accepted scheme is file://.
type Ref struct {
	Path string // decoded, cleaned, absolute filesystem path
}

// Sentinel errors. Handlers wrap them with the offending URI for
// machine-readable error envelopes.
var (
	ErrEmpty             = errors.New("project: empty reference")
	ErrUnsupportedScheme = errors.New("project: only file:// URIs are accepted")
	ErrNonLocal          = errors.New("project: file:// URI must have empty authority (local files only)")
	ErrNotAbsolute       = errors.New("project: file:// URI must encode an absolute path")
	ErrNotIndexed        = errors.New("project: not in registry; call index_repository first")
)

// ParseRef parses a file:// URI. Rejects:
//   - empty / whitespace-only input
//   - non-file:// schemes (e.g. git://, https://)
//   - non-empty authority (e.g. file://host/path)
//   - relative paths (the URI must encode an absolute path)
//   - percent-encoded path-traversal segments (e.g. %2e%2e)
func ParseRef(raw string) (Ref, error) {
	if strings.TrimSpace(raw) == "" {
		return Ref{}, ErrEmpty
	}
	u, err := url.Parse(raw)
	if err != nil {
		return Ref{}, ErrUnsupportedScheme
	}
	if u.Scheme != "file" {
		return Ref{}, ErrUnsupportedScheme
	}
	// Require the "file://" authority delimiter. Bare "file:/abs/path"
	// (no "//") is a degenerate form RFC 8089 allows for the no-
	// authority case; we reject it as not a real file:// URI.
	if !strings.Contains(raw, "://") {
		return Ref{}, ErrUnsupportedScheme
	}
	if u.Path == "" || !strings.HasPrefix(u.Path, "/") {
		return Ref{}, ErrNotAbsolute
	}
	if u.Host != "" {
		return Ref{}, ErrNonLocal
	}
	// Reject decoded path-traversal before filepath.Clean erases it.
	if strings.Contains(u.Path, "/../") || strings.HasSuffix(u.Path, "/..") || u.Path == "/.." {
		return Ref{}, ErrNotAbsolute
	}
	cleaned := filepath.Clean(filepath.Join("/", u.Path))
	if cleaned == "/" || strings.HasPrefix(cleaned, "../") || cleaned == ".." {
		return Ref{}, ErrNotAbsolute
	}
	return Ref{Path: cleaned}, nil
}

// Resolve parses `raw`, looks the decoded path up in `reg`, and
// loads the repo via the per-project disk cache. The caller owns
// repo.Close() (use defer).
//
// Errors:
//   - ParseRef errors (see above)
//   - ErrNotIndexed when the path is absent from the registry
//   - any error from store.Load
//
// ponytail: no context.Context today because store.Load is sync.
// When the future daemon mode lands, add a ctx parameter so
// cancellation propagates from the server loop.
func Resolve(reg *registry.Registry, raw string) (*store.Repo, error) {
	ref, err := ParseRef(raw)
	if err != nil {
		return nil, err
	}
	if _, ok := reg.GetByPath(ref.Path); !ok {
		return nil, ErrNotIndexed
	}
	repo, _, err := store.Load(ref.Path, registry.LoadOptsWithDiskCache(ref.Path)...)
	if err != nil {
		return nil, err
	}
	return repo, nil
}