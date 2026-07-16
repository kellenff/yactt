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
//
// When BindIndex attaches an Index to a Registry, Resolve also
// keeps the loaded *store.Repo warm in-process across tool calls
// (symbol table, call-edge graph, LSP clients). That is the
// parser-warm path for the persistent HTTP daemon.
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
//   - double-encoded traversal (e.g. %252e%252e — url.Parse
//     decodes one level to literal "%2e%2e", but url.PathUnescape
//     on the result catches the second level too)
//
// Defence in depth: the layer beneath us (the registry / store
// loaders) does not itself unescape percent-encoded path segments,
// so a "double-encoded" URI is currently inert. We still reject
// it on the wire so the contract is closed against future
// downstream tools that DO unescape (or against agents that pass
// the raw string to a shell, where unescape rules differ).
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
	// Reject decoded path-traversal on the single-decoded path
	// before filepath.Clean erases it. url.Parse decodes percent-
	// encoded segments inside u.Path, so "%2e%2e" becomes ".."
	// here — the substring check catches it.
	if strings.Contains(u.Path, "/../") || strings.HasSuffix(u.Path, "/..") || u.Path == "/.." {
		return Ref{}, ErrNotAbsolute
	}
	// Belt-and-suspenders: call url.PathUnescape on u.Path to
	// collapse any DOUBLE-encoded traversal that slipped past
	// the single-decode check (e.g. "%252e%252e" — url.Parse
	// decodes to literal "%2e%2e", which the OS treats as data,
	// but a downstream tool that unescapes again would turn it
	// into real ".."). Re-check the traversal pattern on the
	// fully-decoded path. A malformed percent sequence surfaces
	// here as an error — we treat it as ErrNotAbsolute (the
	// caller can re-try with a corrected URI; ErrUnsupportedScheme
	// would be misleading since the scheme is fine).
	fullyDecoded, perr := url.PathUnescape(u.Path)
	if perr != nil {
		return Ref{}, ErrNotAbsolute
	}
	if strings.Contains(fullyDecoded, "/../") || strings.HasSuffix(fullyDecoded, "/..") || fullyDecoded == "/.." {
		return Ref{}, ErrNotAbsolute
	}
	cleaned := filepath.Clean(filepath.Join("/", u.Path))
	if cleaned == "/" || strings.HasPrefix(cleaned, "../") || cleaned == ".." {
		return Ref{}, ErrNotAbsolute
	}
	return Ref{Path: cleaned}, nil
}

// Resolve parses `raw`, looks the decoded path up in `reg`, and
// returns a loaded *store.Repo. When an Index is bound to `reg`
// (see BindIndex), a warm hit returns the pinned repo without
// re-walking the tree; a miss loads via the per-project disk
// cache and stores the result in the Index. When no Index is
// bound, every call pays a fresh store.Load (the pre-Index
// contract — tests that never BindIndex keep that path).
//
// The caller should still defer repo.Close(). Indexed repos are
// Pin()'d so Close is a no-op; eviction / Index.Close ForceClose
// them. Unindexed (cold) repos Close normally and reap LSP children.
//
// Errors:
//   - ParseRef errors (see above)
//   - ErrNotIndexed when the path is absent from the registry
//   - any error from store.Load
//
// ponytail: no context.Context today because store.Load is sync.
// When cancellation needs to propagate from the server loop, add a
// ctx parameter here.
func Resolve(reg *registry.Registry, raw string) (*store.Repo, error) {
	ref, err := ParseRef(raw)
	if err != nil {
		return nil, err
	}
	if _, ok := reg.GetByPath(ref.Path); !ok {
		return nil, ErrNotIndexed
	}
	if idx := IndexFor(reg); idx != nil {
		if repo, ok := idx.Get(ref.Path); ok {
			return repo, nil
		}
	}
	repo, _, err := store.Load(ref.Path, registry.LoadOptsWithDiskCache(ref.Path)...)
	if err != nil {
		return nil, err
	}
	PutFor(reg, repo)
	return repo, nil
}