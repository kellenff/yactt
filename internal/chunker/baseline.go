// Char-count baseline chunker for the issue-34 recall benchmark.
// Lives in the chunker package so the AST/baseline comparison is
// co-located. Not part of the public chunker API — only the
// tests/chunking benchmark imports it.
//
// The baseline slices each file's bytes into overlapping windows:
// 1000 chars per window, 200-char overlap. Each window becomes a
// Chunk with a synthetic ID (`baseline:<file>:<idx>`), no symbol
// metadata, no callers/callees. The whole point is to measure how
// the AST chunker fares against the dumb-but-fast approach.

package chunker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"

	"github.com/kellenff/yactt/internal/store"
)

// BaselineWindowChars is the baseline chunker's window size, in
// bytes. Matches the plan in docs/plans/issue-34-ast-chunker.md.
const BaselineWindowChars = 1000

// BaselineOverlapChars is the per-window overlap.
const BaselineOverlapChars = 200

// BaselineStep returns BaselineWindowChars - BaselineOverlapChars.
func BaselineStep() int { return BaselineWindowChars - BaselineOverlapChars }

// BaselineChunk is the baseline's wire shape. Distinct from Chunk
// so callers can tell which chunker produced the entry. The
// recall benchmark matches these to AST Chunks via file +
// overlapping line range.
type BaselineChunk struct {
	ID        string `json:"id"`
	File      string `json:"file"`
	StartLine int    `json:"start_line"` // 1-based, inclusive
	EndLine   int    `json:"end_line"`   // 1-based, inclusive
	StartByte int    `json:"start_byte"` // 0-based
	EndByte   int    `json:"end_byte"`   // exclusive
	Text      string `json:"text"`
	Policy    string `json:"policy"` // always "baseline"
}

// RunBaseline walks the repo once and writes one NDJSON line per
// baseline chunk to w. Same options shape as Run (Languages,
// Include, Exclude, WithTests) so the benchmark can pin both
// chunkers to the same file set.
func RunBaseline(ctx context.Context, r *store.Repo, opts Options, w io.Writer) (int, error) {
	if r == nil {
		return 0, fmt.Errorf("chunker: nil repo")
	}
	if w == nil {
		return 0, fmt.Errorf("chunker: nil writer")
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)

	langs := make(map[string]bool, len(opts.Languages))
	for _, l := range opts.Languages {
		langs[string(l)] = true
	}
	filterLanguages := len(langs) > 0

	files := r.Files()
	sort.Strings(files)
	count := 0
	step := BaselineStep()
	for _, path := range files {
		if err := ctx.Err(); err != nil {
			return count, err
		}
		if !shouldInclude(path, r.Root(), opts) {
			continue
		}
		cf, err := r.CachedFile(path)
		if err != nil || cf == nil {
			continue
		}
		if filterLanguages && !langs[string(cf.Grammar.Name())] {
			continue
		}
		src := cf.Bytes
		if len(src) == 0 {
			continue
		}
		rel, _ := filepath.Rel(r.Root(), path)
		base := filepath.Base(path)
		// Walk the file byte-by-byte in BaselineStep windows.
		// Use byte indices to keep the slicing simple; convert to
		// 1-based line ranges at the end so the wire shape matches
		// AST Chunk.
		for startByte := 0; startByte < len(src); startByte += step {
			if err := ctx.Err(); err != nil {
				return count, err
			}
			endByte := startByte + BaselineWindowChars
			if endByte > len(src) {
				endByte = len(src)
			}
			text := string(src[startByte:endByte])
			startLine := byteToLine(src, startByte) + 1
			endLine := byteToLine(src, endByte-1) + 1
			if endLine < startLine {
				endLine = startLine
			}
			c := BaselineChunk{
				ID:        fmt.Sprintf("baseline:%s:%d", rel, count),
				File:      path,
				StartLine: startLine,
				EndLine:   endLine,
				StartByte: startByte,
				EndByte:   endByte,
				Text:      text,
				Policy:    "baseline",
			}
			_ = base
			if err := enc.Encode(c); err != nil {
				return count, fmt.Errorf("baseline: encode: %w", err)
			}
			count++
			// Last window: stop. Without this, an N-byte file with
			// N < WindowChars still emits one chunk (the full
			// file) but files larger than 2*WindowChars emit a
			// trailing window with < OverlapChars of new content.
			if endByte == len(src) {
				break
			}
		}
	}
	return count, nil
}

// byteToLine returns the 0-based line number containing byte
// offset `b` in src. Lines are split on '\n'.
func byteToLine(src []byte, b int) int {
	if b < 0 {
		return 0
	}
	if b >= len(src) {
		b = len(src) - 1
	}
	line := 0
	for i := 0; i < b; i++ {
		if src[i] == '\n' {
			line++
		}
	}
	return line
}
