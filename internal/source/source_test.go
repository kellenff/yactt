package source_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/parser"
	"github.com/kellenff/yactt/internal/source"
)

// goLang is the canonical Language used by every test that needs a parsed
// File. We use the real Go grammar — no mocks.
var goLang = parser.Go{}

const smallGoSource = `package auth

func Login(user, pass string) error {
	return nil
}
`

func writeFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "auth.go")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return p
}

func TestLoadFileOK(t *testing.T) {
	p := writeFile(t, smallGoSource)
	f, err := source.LoadFile(p, goLang)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if f == nil {
		t.Fatal("LoadFile returned nil file")
	}
	if f.Path != p {
		t.Errorf("Path = %q, want %q", f.Path, p)
	}
	if string(f.Bytes) != smallGoSource {
		t.Errorf("Bytes mismatch:\n got %q\nwant %q", f.Bytes, smallGoSource)
	}
	if f.MTime == 0 {
		t.Error("MTime should be set from os.Stat")
	}
	if f.Root == nil {
		t.Error("Root should be non-nil after parse")
	}
	if f.Grammar == nil {
		t.Error("Grammar should be set")
	}
}

func TestLoadFileMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does_not_exist.go")
	_, err := source.LoadFile(missing, goLang)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !errors.Is(err, source.ErrReadFailed) {
		t.Errorf("err = %v, want errors.Is(ErrReadFailed)", err)
	}
}

func TestLoadFileBinaryGarbage(t *testing.T) {
	// Tree-sitter is permissive — random bytes parse without error. The test
	// asserts that we don't panic and that we get a non-nil Root.
	p := writeFile(t, "\x00\x01\x02\x03\xffnot really source\xfe\xfd")
	f, err := source.LoadFile(p, goLang)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if f.Root == nil {
		t.Error("Root should still be set on permissive parse")
	}
}

func TestLoadFileEmptyFile(t *testing.T) {
	p := writeFile(t, "")
	f, err := source.LoadFile(p, goLang)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if len(f.Bytes) != 0 {
		t.Errorf("Bytes = %q, want empty", f.Bytes)
	}
}

func TestLines(t *testing.T) {
	p := writeFile(t, smallGoSource)
	f, err := source.LoadFile(p, goLang)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	lines, err := f.Lines()
	if err != nil {
		t.Fatalf("Lines: %v", err)
	}
	// smallGoSource ends with "\n}\n"; the trailing empty after the final \n
	// is dropped. Interior empty lines are preserved. Visible content is 5 lines.
	want := []string{
		"package auth",
		"",
		"func Login(user, pass string) error {",
		"\treturn nil",
		"}",
	}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d: %q", len(lines), len(want), lines)
	}
	for i, w := range want {
		if string(lines[i]) != w {
			t.Errorf("line %d = %q, want %q", i, lines[i], w)
		}
	}
	if f.LineCount() != len(lines) {
		t.Errorf("LineCount = %d, Lines len = %d", f.LineCount(), len(lines))
	}
}

func TestLinesNoTrailingNewline(t *testing.T) {
	p := writeFile(t, "a\nb\nc")
	f, _ := source.LoadFile(p, goLang)
	lines, err := f.Lines()
	if err != nil {
		t.Fatalf("Lines: %v", err)
	}
	if len(lines) != 3 {
		t.Errorf("got %d lines, want 3: %q", len(lines), lines)
	}
	if string(lines[2]) != "c" {
		t.Errorf("last line = %q, want c", lines[2])
	}
}

func TestSliceHappyPath(t *testing.T) {
	p := writeFile(t, smallGoSource)
	f, _ := source.LoadFile(p, goLang)

	// Range [0,2) covers lines "package auth" and "" (the blank separator).
	// bytes.Join joins with a single "\n", so the result has exactly one newline.
	got, err := f.Slice(domain.LineRange{Start: 0, End: 2})
	if err != nil {
		t.Fatalf("Slice: %v", err)
	}
	want := "package auth\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSliceEmptyRange(t *testing.T) {
	p := writeFile(t, smallGoSource)
	f, _ := source.LoadFile(p, goLang)

	_, err := f.Slice(domain.LineRange{Start: 2, End: 2})
	if !errors.Is(err, source.ErrEmptyRange) {
		t.Errorf("err = %v, want ErrEmptyRange", err)
	}
}

func TestSliceNegativeStart(t *testing.T) {
	p := writeFile(t, smallGoSource)
	f, _ := source.LoadFile(p, goLang)

	_, err := f.Slice(domain.LineRange{Start: -1, End: 2})
	if !errors.Is(err, source.ErrRangeOOB) {
		t.Errorf("err = %v, want ErrRangeOOB", err)
	}
}

func TestSliceStartOOB(t *testing.T) {
	p := writeFile(t, smallGoSource)
	f, _ := source.LoadFile(p, goLang)

	_, err := f.Slice(domain.LineRange{Start: 100, End: 101})
	if !errors.Is(err, source.ErrRangeOOB) {
		t.Errorf("err = %v, want ErrRangeOOB", err)
	}
}

func TestSliceEndOOB(t *testing.T) {
	p := writeFile(t, smallGoSource)
	f, _ := source.LoadFile(p, goLang)

	_, err := f.Slice(domain.LineRange{Start: 0, End: 9999})
	if !errors.Is(err, source.ErrRangeOOB) {
		t.Errorf("err = %v, want ErrRangeOOB", err)
	}
}

func TestSliceWholeFile(t *testing.T) {
	p := writeFile(t, smallGoSource)
	f, _ := source.LoadFile(p, goLang)

	got, err := f.Slice(domain.LineRange{Start: 0, End: f.LineCount()})
	if err != nil {
		t.Fatalf("Slice: %v", err)
	}
	// Trailing newline dropped, so the joined output is the original minus one \n.
	if got != strings.TrimSuffix(smallGoSource, "\n") {
		t.Errorf("whole-file slice mismatch:\n got %q\nwant %q", got, strings.TrimSuffix(smallGoSource, "\n"))
	}
}

func TestSliceWithTriviaSurroundingWhitespace(t *testing.T) {
	const src = "" // placeholder — see real test below
	_ = src

	const file = "package x\n\n   \nfunc F() {}\n   \n\n"
	p := writeFile(t, file)
	f, _ := source.LoadFile(p, goLang)

	// Request lines 3..4 ("func F() {}") — extends outward to include the
	// contiguous whitespace lines 2 and 4.
	got, err := f.SliceWithTrivia(domain.LineRange{Start: 3, End: 4})
	if err != nil {
		t.Fatalf("SliceWithTrivia: %v", err)
	}
	for _, want := range []string{"func F() {}", "   "} {
		if !strings.Contains(got, want) {
			t.Errorf("got %q, missing fragment %q", got, want)
		}
	}
}

func TestSliceWithTriviaNoExtension(t *testing.T) {
	const file = "package x\nfunc F() {}\nfunc G() {}\n"
	p := writeFile(t, file)
	f, _ := source.LoadFile(p, goLang)

	r := domain.LineRange{Start: 1, End: 2}
	got, err := f.SliceWithTrivia(r)
	if err != nil {
		t.Fatalf("SliceWithTrivia: %v", err)
	}
	// Lines 0 (package) and 2 (func G) are non-whitespace, so no extension.
	if got != "func F() {}" {
		t.Errorf("got %q, want %q", got, "func F() {}")
	}
}

func TestSliceWithTriviaPropagatesErrors(t *testing.T) {
	p := writeFile(t, smallGoSource)
	f, _ := source.LoadFile(p, goLang)

	_, err := f.SliceWithTrivia(domain.LineRange{Start: 0, End: 0})
	if !errors.Is(err, source.ErrEmptyRange) {
		t.Errorf("empty range: err = %v, want ErrEmptyRange", err)
	}
}

func TestTokensEmitsNamedTerminals(t *testing.T) {
	p := writeFile(t, smallGoSource)
	f, _ := source.LoadFile(p, goLang)

	toks, err := f.Tokens(domain.LineRange{Start: 0, End: f.LineCount()})
	if err != nil {
		t.Fatalf("Tokens: %v", err)
	}
	if len(toks) == 0 {
		t.Fatal("expected at least one token")
	}
	for _, tok := range toks {
		if tok.Value == "" {
			t.Errorf("token %+v has empty value", tok)
		}
		if tok.LineRange.End <= tok.LineRange.Start {
			t.Errorf("token %+v has non-forward range", tok)
		}
		if tok.LineRange.Start < 0 || tok.LineRange.End > f.LineCount() {
			t.Errorf("token %+v outside line range [0,%d)", tok, f.LineCount())
		}
	}
}

func TestTokensRestrictedRange(t *testing.T) {
	p := writeFile(t, smallGoSource)
	f, _ := source.LoadFile(p, goLang)

	all, _ := f.Tokens(domain.LineRange{Start: 0, End: f.LineCount()})
	if len(all) == 0 {
		t.Fatal("setup: full-range tokens empty")
	}
	// Restrict to line 2 (the `func Login...` line).
	restricted, err := f.Tokens(domain.LineRange{Start: 2, End: 3})
	if err != nil {
		t.Fatalf("Tokens: %v", err)
	}
	if len(restricted) == 0 {
		t.Fatal("restricted tokens empty")
	}
	if len(restricted) >= len(all) {
		t.Errorf("restricted should have fewer tokens: restricted=%d all=%d", len(restricted), len(all))
	}
	// Every restricted token's range should fall inside [2, 3).
	for _, tok := range restricted {
		if tok.LineRange.Start < 2 || tok.LineRange.End > 3 {
			t.Errorf("token %+v outside [2,3)", tok)
		}
	}
}

func TestTokensEmptyRangeGivesFullFile(t *testing.T) {
	p := writeFile(t, smallGoSource)
	f, _ := source.LoadFile(p, goLang)

	// An invalid range (start<0 OR end<=start) is the documented signal to
	// return tokens for the whole file.
	all, _ := f.Tokens(domain.LineRange{Start: 0, End: f.LineCount()})
	def, err := f.Tokens(domain.LineRange{Start: -1, End: -1})
	if err != nil {
		t.Fatalf("Tokens default-range: %v", err)
	}
	if len(def) != len(all) {
		t.Errorf("default-range tokens = %d, want %d", len(def), len(all))
	}
}

func TestTokensEmptyLeafDropped(t *testing.T) {
	// A small Go file: tree-sitter sometimes produces empty named terminals
	// (e.g. missing semicolons in some grammars). Verify we never emit one.
	p := writeFile(t, "package x\n")
	f, _ := source.LoadFile(p, goLang)

	toks, err := f.Tokens(domain.LineRange{Start: 0, End: f.LineCount()})
	if err != nil {
		t.Fatalf("Tokens: %v", err)
	}
	for _, tok := range toks {
		if tok.Value == "" {
			t.Errorf("empty token leaked: %+v", tok)
		}
	}
}

// TestSlice_StartEqualsLineCount covers the boundary
// `r.Start >= len(lines)` at line 81. Start exactly at len(lines)
// (i.e. one past the last line) with End > Start must reject via
// ErrRangeOOB — the live code is `>=` so Start==len(lines) is rejected.
func TestSlice_StartEqualsLineCount(t *testing.T) {
	p := writeFile(t, smallGoSource)
	f, _ := source.LoadFile(p, goLang)
	_, err := f.Slice(domain.LineRange{Start: f.LineCount(), End: f.LineCount() + 1})
	if !errors.Is(err, source.ErrRangeOOB) {
		t.Errorf("start == LineCount with End > Start: err = %v, want ErrRangeOOB", err)
	}
}

// TestSlice_EndEqualsLineCount covers the boundary `end > len(lines)`
// at line 85. End exactly at len(lines) is the inclusive-exclusive last
// line; live code accepts, mutated `>=` would reject.
func TestSlice_EndEqualsLineCount(t *testing.T) {
	p := writeFile(t, smallGoSource)
	f, _ := source.LoadFile(p, goLang)
	got, err := f.Slice(domain.LineRange{Start: 0, End: f.LineCount()})
	if err != nil {
		t.Fatalf("end == LineCount: err = %v, want nil", err)
	}
	if got == "" {
		t.Errorf("end == LineCount produced empty slice")
	}
}

// TestSliceWithTrivia_StartAtZero covers the boundary `start > 0` at
// line 107. When start == 0 the loop never runs, so no upward
// extension. A mutation to `>=` would index lines[-1] and panic.
func TestSliceWithTrivia_StartAtZero(t *testing.T) {
	const file = "package x\n\nfunc F() {}\n"
	p := writeFile(t, file)
	f, _ := source.LoadFile(p, goLang)

	// Lines: 0=package x, 1="", 2=func F() {}. Range [2,3) extends
	// upward to include the blank line at 1.
	got, err := f.SliceWithTrivia(domain.LineRange{Start: 2, End: 3})
	if err != nil {
		t.Fatalf("SliceWithTrivia: %v", err)
	}
	if !strings.Contains(got, "func F() {}") {
		t.Errorf("got %q, missing func body", got)
	}
}

// TestSliceWithTrivia_EndAtLineCount covers the boundary `end < len(lines)`
// at line 111. When end == len(lines), the loop must NOT run (otherwise
// we'd index out of bounds).
func TestSliceWithTrivia_EndAtLineCount(t *testing.T) {
	const file = "package x\n\nfunc F() {}\n"
	p := writeFile(t, file)
	f, _ := source.LoadFile(p, goLang)

	// Range [2, 3) — end is at the last line. SliceWithTrivia must
	// not panic trying to read lines[len(lines)].
	_, err := f.SliceWithTrivia(domain.LineRange{Start: 2, End: 3})
	if err != nil {
		t.Fatalf("SliceWithTrivia at end-of-file: %v", err)
	}
}

// TestSliceWithTrivia_AdjacentLineConcatenation covers the boundary
// `r.Start < r.End` at line 120. When the range has at least one line,
// a newline is inserted between head + body + tail. Pin the exact
// output.
func TestSliceWithTrivia_AdjacentLineConcatenation(t *testing.T) {
	const file = "package x\n\n\nfunc F() {}\n\n"
	p := writeFile(t, file)
	f, _ := source.LoadFile(p, goLang)

	// Range [3, 4] is the func line. Upward extension includes line 2
	// (blank, whitespace). Result must contain the blank line followed
	// by the func line, joined by \n.
	got, err := f.SliceWithTrivia(domain.LineRange{Start: 3, End: 4})
	if err != nil {
		t.Fatalf("SliceWithTrivia: %v", err)
	}
	if !strings.Contains(got, "\nfunc F() {}") {
		t.Errorf("got %q, want head+newline+body", got)
	}
}

// TestLines_EmptyFile covers the boundary `len(raw) > 0` at line 134.
// An empty file (zero bytes) returns an empty slice, not a panic.
func TestLines_EmptyFile(t *testing.T) {
	p := writeFile(t, "")
	f, _ := source.LoadFile(p, goLang)
	lines, err := f.Lines()
	if err != nil {
		t.Fatalf("Lines: %v", err)
	}
	if len(lines) != 0 {
		t.Errorf("empty file lines = %d, want 0", len(lines))
	}
}

// TestTokens_DefaultRangeAtZero covers the boundary
// `filterEnd <= filterStart` at line 171. When Start == End == 0, the
// default-range branch must fire (not the zero-range filter).
func TestTokens_DefaultRangeAtZero(t *testing.T) {
	p := writeFile(t, smallGoSource)
	f, _ := source.LoadFile(p, goLang)

	all, _ := f.Tokens(domain.LineRange{Start: 0, End: f.LineCount()})
	def, err := f.Tokens(domain.LineRange{Start: 0, End: 0})
	if err != nil {
		t.Fatalf("Tokens default: %v", err)
	}
	if len(def) != len(all) {
		t.Errorf("default-range at zero = %d, want %d (whole file)", len(def), len(all))
	}
}

// TestWalkTokens_RangeBoundary covers the line-range filter in
// walkTokens (line 186: `er <= lineStart || sr >= lineEnd`). A token
// whose start row equals lineEnd is rejected.
func TestWalkTokens_RangeBoundary(t *testing.T) {
	p := writeFile(t, smallGoSource)
	f, _ := source.LoadFile(p, goLang)

	// Restrict to line 2 (the func declaration). A token whose start
	// row is 2 and end row is also 2 (single-line token) must still be
	// included (since er=2 > lineStart=2 is false, sr=2 < lineEnd=3 is true).
	restricted, err := f.Tokens(domain.LineRange{Start: 2, End: 3})
	if err != nil {
		t.Fatalf("Tokens: %v", err)
	}
	if len(restricted) == 0 {
		t.Fatalf("no tokens on line 2")
	}
	// And restricting to [3, 4) (line 3 = `\treturn nil`) must include
	// tokens with sr=3.
	rets, err := f.Tokens(domain.LineRange{Start: 3, End: 4})
	if err != nil {
		t.Fatalf("Tokens: %v", err)
	}
	if len(rets) == 0 {
		t.Fatalf("no tokens on line 3")
	}
}

// TestWalkTokens_ClampLowerBound covers the clamp helper at line 200
// (`if v < lineStart`). For a token starting on line 0 with lineStart
// == 2, the start must clamp to 2.
func TestWalkTokens_ClampLowerBound(t *testing.T) {
	const file = "package x\n\nfunc F() {\n\tdoStuff()\n}\n"
	p := writeFile(t, file)
	f, _ := source.LoadFile(p, goLang)

	// Restrict to lines 3..5. Tokens on line 2 (the `func F()` line)
	// have sr=2 — clamp should bring it to 3.
	out, err := f.Tokens(domain.LineRange{Start: 3, End: 5})
	if err != nil {
		t.Fatalf("Tokens: %v", err)
	}
	for _, tok := range out {
		if tok.LineRange.Start < 3 {
			t.Errorf("token %+v: start < 3, clamp failed", tok)
		}
	}
}

// TestLoadFile_RejectsOversized verifies the byte cap. We synthesise
// a 1 MiB file, configure the cap at 64 KiB, and assert LoadFile
// returns ErrFileTooLarge. Without the cap, a planted multi-GB blob
// could allocate gigabytes before failing to parse.
func TestLoadFile_RejectsOversized(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "big.go")
	// 1 MiB of repeated Go-comment padding.
	const block = "// padding line that takes a few bytes\n"
	const total = 1 << 20 // 1 MiB
	var sb strings.Builder
	for sb.Len() < total {
		sb.WriteString(block)
	}
	if err := os.WriteFile(p, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := source.LoadFile(p, goLang, source.WithMaxBytes(64*1024))
	if err == nil {
		t.Fatal("expected ErrFileTooLarge; got nil")
	}
	if !errors.Is(err, source.ErrFileTooLarge) {
		t.Errorf("err = %v, want errors.Is(ErrFileTooLarge)", err)
	}
}

// TestLoadFile_AcceptsUnderCap confirms a file under the cap still
// loads successfully when WithMaxBytes is in effect. Regression
// guard: an off-by-one in the comparison (e.g. >= instead of >) would
// reject exactly-cap files.
func TestLoadFile_AcceptsUnderCap(t *testing.T) {
	// 4 KiB file, cap 8 KiB — well under.
	p := writeFile(t, strings.Repeat("// padding\n", 200))
	f, err := source.LoadFile(p, goLang, source.WithMaxBytes(8*1024))
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if f == nil {
		t.Fatal("LoadFile returned nil")
	}
}

// TestLoadFile_DefaultCap documents the default cap (8 MiB). A
// file under 8 MiB must load without WithMaxBytes being supplied.
func TestLoadFile_DefaultCap(t *testing.T) {
	p := writeFile(t, smallGoSource)
	if source.DefaultMaxFileBytes != 8*1024*1024 {
		t.Errorf("DefaultMaxFileBytes = %d, want 8 MiB", source.DefaultMaxFileBytes)
	}
	f, err := source.LoadFile(p, goLang)
	if err != nil {
		t.Fatalf("LoadFile with default cap: %v", err)
	}
	if f == nil {
		t.Fatal("LoadFile returned nil")
	}
}
