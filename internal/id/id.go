// Package id implements node ID parse/format per design Appendix A.
//
// IDs are stable across commits and double as addresses for the cache and the
// cross-tool layer registry. The grammar:
//
//	repo:<absolute_path>
//	pkg:<import_path>
//	file:<path_from_repo_root>
//	fn:<package>.<receiver>.<Name>
//	meth:<package>.<Class>.<Name>
//	class:<package>.<Name>
//	module:<package>.<Name>
//
// The package follows the parser-not-validate convention: constructors return
// (`Kind`, remainder-string-or-error) rather than mutating in place. Callers at
// the boundary parse once and pass the typed value into pure logic.
package id

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/kellenff/yactt/internal/parser"
)

// Kind tags the head of an identifier.
type Kind string

const (
	KindRepo     Kind = "repo"
	KindPackage  Kind = "pkg"
	KindFile     Kind = "file"
	KindFunction Kind = "fn"
	KindMethod   Kind = "meth"
	KindClass    Kind = "class"
	KindModule   Kind = "module"
)

// Errors returned by Parse. Sentinel values so callers can use errors.Is.
var (
	ErrEmpty       = errors.New("id: empty")
	ErrUnknownKind = errors.New("id: unknown kind prefix")
	ErrBadFormat   = errors.New("id: malformed body for kind")
)

// ID is the parsed form of a node identifier. The opaque Body is the kind-specific
// remainder; semantics for each kind live in this package's helpers.
type ID struct {
	Kind Kind
	Body string
}

// String renders ID into its canonical text form.
func (i ID) String() string { return string(i.Kind) + ":" + i.Body }

// Parse decodes "kind:body" and validates only the prefix. Body validation is
// per-kind and exposed as Kind-specific accessors so we can return richer
// errors for callers that want them.
func Parse(s string) (ID, error) {
	if s == "" {
		return ID{}, ErrEmpty
	}
	idx := strings.IndexByte(s, ':')
	if idx <= 0 || idx == len(s)-1 {
		return ID{}, fmt.Errorf("%w: %q", ErrBadFormat, s)
	}
	head := s[:idx]
	tail := s[idx+1:]
	switch Kind(head) {
	case KindRepo, KindPackage, KindFile, KindFunction, KindMethod, KindClass, KindModule:
		return ID{Kind: Kind(head), Body: tail}, nil
	}
	return ID{}, fmt.Errorf("%w: %q", ErrUnknownKind, head)
}

// MustParse is the panicking variant for tests and trusted callers.
func MustParse(s string) ID {
	i, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return i
}

// Repo builds a repo:<abs> ID. The path is stored verbatim; no normalization —
// two distinct callers may legitimately pass equivalent paths and we want stable
// cache keys.
func Repo(absPath string) ID { return ID{KindRepo, absPath} }

// Package builds a pkg:<import-path> ID.
func Package(importPath string) ID { return ID{KindPackage, importPath} }

// File builds a file:<repo-relative-path> ID. The path is canonicalised via
// path.Clean so equivalent inputs produce identical IDs.
func File(repoRelPath string) ID { return ID{KindFile, path.Clean(repoRelPath)} }

// Function builds a fn:<package>.<receiver?>.<Name> ID. receiver may be "" to elide.
func Function(pkg, receiver, name string) ID {
	if receiver == "" {
		return ID{KindFunction, pkg + "." + name}
	}
	return ID{KindFunction, pkg + "." + receiver + "." + name}
}

// Method builds a meth:<package>.<Class>.<Name> ID.
func Method(pkg, class, name string) ID { return ID{KindMethod, pkg + "." + class + "." + name} }

// Class builds a class:<package>.<Name> ID.
func Class(pkg, name string) ID { return ID{KindClass, pkg + "." + name} }

// Module builds a module:<package>.<Name> ID (Python module-level symbol).
func Module(pkg, name string) ID { return ID{KindModule, pkg + "." + name} }

// JoinDotted concatenates non-empty segments with a '.' separator. The
// id-body string in `ID{Kind, body}` is canonically dot-joined; this
// helper drops leading/trailing dots when pkg or the suffix is empty so
// TS/JS symbol IDs (no enclosing module) round-trip cleanly.
func JoinDotted(pkg string, rest ...string) string {
	parts := make([]string, 0, 1+len(rest))
	if pkg != "" {
		parts = append(parts, pkg)
	}
	for _, p := range rest {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, ".")
}

// For builds the canonical ID for any parser.Symbol regardless of
// language. Single source of truth for the kind→prefix mapping and
// for pkg/class/name composition; replaces the five sites in the tool
// and search layers that each emitted their own prefix.
//
// The Kind mapping:
//
//	function_declaration          → fn:
//	method_declaration            → meth:
//	type_declaration              → class:
//	class_declaration             → class:
//	interface_declaration         → class:
//	type_alias_declaration        → class:
//	enum_declaration              → class:
//	module / module_declaration   → module:
//	(unknown)                     → fn:
//
// Methods without a populated Receiver fall back to `fn:` because the
// ID grammar requires a receiver to disambiguate; the symbol layer
// always populates it, so this branch is a safety net only.
func For(s parser.Symbol, pkg string) string {
	kind := KindFunction
	switch s.Kind {
	case "method_declaration":
		if s.Receiver == "" {
			// Safety net for malformed symbols; the parser layer always
			// populates Receiver, so this branch should never fire in
			// practice. Fall back to fn: so the ID is at least
			// round-trippable.
			break
		}
		kind = KindMethod
	case "type_declaration", "class_declaration",
		"interface_declaration", "type_alias_declaration", "enum_declaration":
		kind = KindClass
	case "module", "module_declaration":
		kind = KindModule
	}
	name := s.Name
	if kind == KindMethod {
		name = s.Receiver + "." + s.Name
	}
	return ID{kind, JoinDotted(pkg, name)}.String()
}

// FunctionParts splits the body of a fn:<...> ID into (package, receiver, name).
// Receiver is "" if absent.
func (i ID) FunctionParts() (pkg, receiver, name string, err error) {
	if i.Kind != KindFunction {
		return "", "", "", fmt.Errorf("%w: not a function id", ErrBadFormat)
	}
	last := strings.LastIndexByte(i.Body, '.')
	if last < 0 {
		return "", "", "", fmt.Errorf("%w: missing dotted receiver/name", ErrBadFormat)
	}
	head, name := i.Body[:last], i.Body[last+1:]
	mid := strings.LastIndexByte(head, '.')
	if mid < 0 {
		return head, "", name, nil
	}
	return head[:mid], head[mid+1:], name, nil
}

// MethodParts splits a meth:<package-or-empty>.<Class>.<Name> ID. The
// package segment is optional — TS/JS class methods and other shapes
// where the enclosing module is implicit have no package segment.
func (i ID) MethodParts() (pkg, class, name string, err error) {
	if i.Kind != KindMethod {
		return "", "", "", fmt.Errorf("%w: not a method id", ErrBadFormat)
	}
	parts := strings.SplitN(i.Body, ".", 3)
	switch len(parts) {
	case 3:
		if parts[0] == "" || parts[1] == "" || parts[2] == "" {
			return "", "", "", fmt.Errorf("%w: want pkg.Class.Name", ErrBadFormat)
		}
		return parts[0], parts[1], parts[2], nil
	case 2:
		if parts[0] == "" || parts[1] == "" {
			return "", "", "", fmt.Errorf("%w: want Class.Name", ErrBadFormat)
		}
		return "", parts[0], parts[1], nil
	}
	return "", "", "", fmt.Errorf("%w: want pkg.Class.Name or Class.Name", ErrBadFormat)
}

// ClassParts splits a class:<package>.<Name> ID.
func (i ID) ClassParts() (pkg, name string, err error) {
	if i.Kind != KindClass {
		return "", "", fmt.Errorf("%w: not a class id", ErrBadFormat)
	}
	parts := strings.SplitN(i.Body, ".", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("%w: want pkg.Name", ErrBadFormat)
	}
	return parts[0], parts[1], nil
}
