// Package source is the lossless source layer per design §4.3.
//
// "Lossless" is the key word here: comments, whitespace, and encoding are all
// preserved verbatim. The layer returns the exact byte slice for a line range;
// the tokens layer is the CST-walked view of the same content.
//
// The package is small by design — it's a thin boundary between filesystem I/O
// and the rest of the program. Bigger construction lives upstream in `store`.
package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/parser"
)

// Errors returned by Fetch. Sentinels so callers can match with errors.Is.
var (
	ErrEmptyRange  = errors.New("source: empty line range")
	ErrRangeOOB    = errors.New("source: line range out of bounds")
	ErrReadFailed  = errors.New("source: cannot read file")
	ErrParseFailed = errors.New("source: cannot parse file")
)

// File holds the on-disk content of one source file plus its parsed CST.
//
// It is the value the source layer shares with the rest of the program; cache
// invalidation revs on (Path, MTime) — see package cache for the policy.
//
// Root is the parse tree's root node (tree-sitter's `ParseCtx` returns it
// directly in the v0.0.0-20240827 API). Callers walk it for symbol extraction.
type File struct {
	Path    string
	Bytes   []byte
	MTime   int64        // unix nanos; used by the cache for invalidation
	Root    *sitter.Node // parse tree root
	Grammar parser.Language
}

// LoadFile reads the file and parses it with the given grammar. Parse errors
// are returned as `ErrParseFailed` so callers can fall back gracefully — design
// §8 open question 6 asked about exactly this case and the answer we picked is
// "surface the error"; the store layer decides how to propagate.
func LoadFile(path string, lang parser.Language) (*File, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrReadFailed, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrReadFailed, err)
	}
	root, err := sitter.ParseCtx(context.Background(), raw, lang.Grammar())
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrParseFailed, err)
	}
	return &File{Path: path, Bytes: raw, MTime: st.ModTime().UnixNano(), Root: root, Grammar: lang}, nil
}

// Slice returns the byte slice for the inclusive-exclusive line range
// [start, end). An empty range is an error; callers who want "to end of file"
// can pass (start, lineCount).
func (f *File) Slice(r domain.LineRange) (string, error) {
	if r.Start == r.End {
		return "", ErrEmptyRange
	}
	if r.Start < 0 {
		return "", fmt.Errorf("%w: start=%d", ErrRangeOOB, r.Start)
	}
	lines, err := f.Lines()
	if err != nil {
		return "", err
	}
	if r.Start >= len(lines) {
		return "", fmt.Errorf("%w: start=%d beyond %d", ErrRangeOOB, r.Start, len(lines))
	}
	end := r.End
	if end > len(lines) {
		return "", fmt.Errorf("%w: end=%d beyond %d", ErrRangeOOB, end, len(lines))
	}
	return string(bytes.Join(lines[r.Start:end], []byte("\n"))), nil
}

// SliceWithTrivia returns the byte slice for [start, end) plus any leading
// whitespace on start and trailing whitespace on end. Whitespace is defined as
// `\s` (incl. spaces, tabs, newlines); semantics match design §4.3 where
// `include_trivia=true` "preserves comments and whitespace". Comments are a
// future refinement — for MVP we capture whitespace only.
func (f *File) SliceWithTrivia(r domain.LineRange) (string, error) {
	body, err := f.Slice(r)
	if err != nil {
		return "", err
	}
	lines, lerr := f.Lines()
	if lerr != nil {
		return "", lerr
	}
	// Extend the slice to include contiguous whitespace lines above and below.
	start := r.Start
	for start > 0 && isWhitespaceLine(lines[start-1]) {
		start--
	}
	end := r.End
	for end < len(lines) && isWhitespaceLine(lines[end]) {
		end++
	}
	if start == r.Start && end == r.End {
		return body, nil
	}
	head := string(bytes.Join(lines[start:r.Start], []byte("\n")))
	tail := string(bytes.Join(lines[r.End:end], []byte("\n")))
	out := head + body
	if r.Start < r.End {
		out += "\n"
	}
	out += tail
	return out, nil
}

// Lines returns the file content split by newline. Empty trailing line is
// dropped (a file ending in "\n" has Lines().len() == visible lines).
func (f *File) Lines() ([][]byte, error) {
	if len(f.Bytes) == 0 {
		return [][]byte{}, nil
	}
	raw := bytes.Split(f.Bytes, []byte("\n"))
	if len(raw) > 0 && len(raw[len(raw)-1]) == 0 {
		raw = raw[:len(raw)-1]
	}
	return raw, nil
}

// LineCount is the count of visible lines; works on the (file minus trailing
// newline) split.
func (f *File) LineCount() int {
	lines, err := f.Lines()
	if err != nil {
		return 0
	}
	return len(lines)
}

func isWhitespaceLine(b []byte) bool {
	for _, c := range b {
		switch c {
		case ' ', '\t':
			continue
		default:
			return false
		}
	}
	return true
}

// Tokens walks the CST and emits a Token per named terminal. The walk is depth
// first in source order; range rows are 0-based and inclusive-exclusive at the
// byte level, mirroring tree-sitter convention.
func (f *File) Tokens(r domain.LineRange) ([]domain.Token, error) {
	root := f.Root
	if root == nil {
		return nil, ErrParseFailed
	}
	filterStart, filterEnd := r.Start, r.End
	if filterStart < 0 || filterEnd <= filterStart {
		filterStart, filterEnd = 0, f.LineCount()
	}
	out := make([]domain.Token, 0, 32)
	walkTokens(root, f.Bytes, &out, filterStart, filterEnd)
	return out, nil
}

func walkTokens(n *sitter.Node, src []byte, out *[]domain.Token, lineStart, lineEnd int) {
	if n == nil {
		return
	}
	sr := int(n.StartPoint().Row)
	er := int(n.EndPoint().Row) + 1
	// Skip nodes that fully sit outside the requested line range.
	if er <= lineStart || sr >= lineEnd {
		return
	}
	// Leaf terminals with anonymous grammar roles (punctuation, braces, etc.)
	// are excluded — they're noise in the tokens layer.
	if n.ChildCount() == 0 {
		if !n.IsNamed() {
			return
		}
		text := n.Content(src)
		if text == "" {
			return
		}
		clamp := func(v int) int {
			if v < lineStart {
				return lineStart
			}
			if v > lineEnd {
				return lineEnd
			}
			return v
		}
		*out = append(*out, domain.Token{
			Kind:      n.Type(),
			Value:     text,
			LineRange: domain.LineRange{Start: clamp(sr), End: clamp(er)},
		})
		return
	}
	nch := int(n.ChildCount())
	for i := 0; i < nch; i++ {
		walkTokens(n.Child(i), src, out, lineStart, lineEnd)
	}
}
