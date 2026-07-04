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

// MethodParts splits a meth:<package>.<Class>.<Name> ID.
func (i ID) MethodParts() (pkg, class, name string, err error) {
	if i.Kind != KindMethod {
		return "", "", "", fmt.Errorf("%w: not a method id", ErrBadFormat)
	}
	parts := strings.SplitN(i.Body, ".", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", fmt.Errorf("%w: want pkg.Class.Name", ErrBadFormat)
	}
	return parts[0], parts[1], parts[2], nil
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
