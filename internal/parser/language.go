// Package parser is the boundary between domain code and language-specific
// tree-sitter grammars. Each Language owns its grammar; the rest of the program
// talks only to the Language interface and to the small set of pure types here.
package parser

import (
	"errors"
	"path/filepath"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	golanggrammar "github.com/smacker/go-tree-sitter/golang"
)

// Name is a stable identifier for a programming language.
type Name string

const (
	LangGo         Name = "go"
	LangTypeScript Name = "typescript"
	LangJavaScript Name = "javascript"
)

// ErrUnsupported is returned when a file's language has no parser wired up.
var ErrUnsupported = errors.New("parser: language not supported")

// Language is the tree-sitter-driven parser for one programming language.
//
// Implementations are stateless and safe for concurrent use. They're expected
// to be small: set up the tree-sitter Language pointer once (sitter.NewLanguage
// is cheap) and let Parse do the work.
type Language interface {
	Name() Name
	Grammar() *sitter.Language
	// FileExtensions returns the case-insensitive file extensions this parser
	// claims (without the leading dot). Used for per-file routing in Detect.
	FileExtensions() []string
	// ModulePath walks a parsed tree (root node) and returns the package /
	// import path for the source file. Pure function over the parse tree.
	ModulePath(root *sitter.Node, source []byte) string
}

// ByName returns the canonical Language implementation for a given name. For
// names without a wired-up grammar this returns ErrUnsupported — extension
// points stay honest.
func ByName(name Name) (Language, error) {
	switch name {
	case LangGo:
		return Go{}, nil
	case LangTypeScript:
		return TypeScript{}, nil
	case LangJavaScript:
		return JavaScript{}, nil
	}
	return nil, ErrUnsupported
}

// Detect picks a language from a file path. First-match wins, so the more
// specific extensions must be listed first in each Language's FileExtensions.
//
// Returns ErrUnsupported for files we don't know how to parse — the caller is
// expected to skip them (typical for vendor dirs, generated files, etc.).
func Detect(path string) (Language, error) {
	ext := strings.ToLower(filepath.Ext(path))
	if ext != "" {
		ext = ext[1:] // strip the leading dot
	}
	for _, lang := range All() {
		for _, e := range lang.FileExtensions() {
			if strings.EqualFold(e, ext) {
				return lang, nil
			}
		}
	}
	return nil, ErrUnsupported
}

// All returns every supported language in priority order. Used by Detect.
func All() []Language {
	return []Language{Go{}, TypeScript{}, JavaScript{}}
}

// Go is the tree-sitter driver for the Go programming language.
type Go struct{}

// Name implements Language.
func (Go) Name() Name { return LangGo }

// Grammar implements Language.
func (Go) Grammar() *sitter.Language { return golanggrammar.GetLanguage() }

// FileExtensions implements Language.
func (Go) FileExtensions() []string { return []string{"go"} }

// ModulePath reads the `package <name>` clause near the top of the file and
// returns the import path inferred from the directory structure (caller passes
// the dir as prefix). For MVP we only return the bare package name; the repo
// layer composes it with the module root.
func (Go) ModulePath(root *sitter.Node, source []byte) string {
	if root == nil {
		return ""
	}
	// Walk for a `package_clause` node; the grammar's `(package_clause
	// "package" (package_identifier))` rule exposes (package_identifier) child.
	n := int(root.ChildCount())
	for i := 0; i < n; i++ {
		ch := root.Child(i)
		if ch == nil {
			continue
		}
		if ch.Type() != "package_clause" {
			continue
		}
		cn := int(ch.ChildCount())
		for j := 0; j < cn; j++ {
			gc := ch.Child(j)
			if gc == nil {
				continue
			}
			if gc.Type() == "package_identifier" {
				return gc.Content(source)
			}
		}
	}
	return ""
}
