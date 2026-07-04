// Package summarizer builds deterministic, no-LLM summaries per design §4.1.
//
// Format: "<Kind>: <first line of doc comment OR signature>".
//
// The summarizer is intentionally simple — it reads the source slice, peels
// off the doc comment block above a symbol, and falls back to the first
// non-empty line of the declaration body if there's no comment. No language
// model is involved.
package summarizer

import (
	"strings"
)

// Summarize returns a deterministic summary for a source slice. docComment is
// the immediately-preceding comment block (caller extracted); fallback is the
// declaration line. Either may be empty — empty inputs just yield an
// "<Kind>:" prefix.
//
// kind is the human-readable noun used for the prefix (e.g. "Function",
// "Method"); the prefix is capitalised so it reads as a sentence.
func Summarize(kind, docComment, fallback string) string {
	prefix := strings.TrimSpace(kind)
	if prefix == "" {
		prefix = "Symbol"
	}
	first := ""
	if docComment != "" {
		first = firstLine(docComment)
	}
	if first == "" {
		first = firstLine(fallback)
	}
	if first == "" {
		return prefix + ":"
	}
	return prefix + ": " + first
}

// FirstLine returns the first non-empty, non-trivia line of a multi-line
// comment block. Public so callers can do their own pre-processing.
func FirstLine(s string) string { return firstLine(s) }

func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(stripCommentMarkers(line))
		if trimmed == "" {
			continue
		}
		return trimmed
	}
	return ""
}

// stripCommentMarkers removes the leading // or /* and trailing */ from a
// single line of a comment block. Multi-line /* ... */ blocks aren't handled
// here — call sites that hit those should normalize upstream.
func stripCommentMarkers(line string) string {
	line = strings.TrimSpace(line)
	switch {
	case strings.HasPrefix(line, "//"):
		return strings.TrimSpace(strings.TrimPrefix(line, "//"))
	case strings.HasPrefix(line, "/*"):
		// Drop the leading `/*` and any trailing `*/` on the same line.
		line = strings.TrimPrefix(line, "/*")
		if idx := strings.Index(line, "*/"); idx >= 0 {
			line = line[:idx]
		}
		return strings.TrimSpace(line)
	}
	return line
}
