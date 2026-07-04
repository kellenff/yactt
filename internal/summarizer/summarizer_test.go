package summarizer_test

import (
	"testing"

	"github.com/kellenff/yactt/internal/summarizer"
)

func TestSummarizeDocComment(t *testing.T) {
	got := summarizer.Summarize("Function", "// Login authenticates a user.", "func Login()")
	want := "Function: Login authenticates a user."
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSummarizeMultilineDocComment(t *testing.T) {
	doc := "// First line wins.\n// Second line is ignored."
	got := summarizer.Summarize("Method", doc, "func (s *S) M()")
	want := "Method: First line wins."
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSummarizeFallback(t *testing.T) {
	got := summarizer.Summarize("Function", "", "func Login() error")
	want := "Function: func Login() error"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSummarizeFallbackMultiline(t *testing.T) {
	// Doc comment is empty; fallback is multi-line. First non-empty line wins.
	got := summarizer.Summarize("Function", "", "func Login() error {\n\tdoStuff()\n}")
	want := "Function: func Login() error {"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSummarizeEmpty(t *testing.T) {
	got := summarizer.Summarize("Function", "", "")
	want := "Function:"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSummarizeEmptyKind(t *testing.T) {
	got := summarizer.Summarize("", "// docs", "fallback")
	want := "Symbol: docs"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSummarizeWhitespaceKind(t *testing.T) {
	got := summarizer.Summarize("   ", "// docs", "")
	want := "Symbol: docs"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSummarizeDocWinsOverFallback(t *testing.T) {
	got := summarizer.Summarize("Function", "// Prefer doc.", "fallback body")
	if want := "Function: Prefer doc."; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSummarizeAllBlankLines(t *testing.T) {
	// Doc has only blank-comment lines; falls through to fallback.
	doc := "//\n//\n//"
	got := summarizer.Summarize("Function", doc, "func F()")
	want := "Function: func F()"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFirstLineMultiLine(t *testing.T) {
	in := "// first\n// second\n// third"
	if got, want := summarizer.FirstLine(in), "first"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFirstLineSkipsLeadingBlanks(t *testing.T) {
	in := "\n\n// real"
	if got, want := summarizer.FirstLine(in), "real"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFirstLineBlockComment(t *testing.T) {
	in := "/*\n * The real content.\n */"
	if got, want := summarizer.FirstLine(in), "* The real content."; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFirstLineBlockCommentInline(t *testing.T) {
	in := "/* real content */"
	if got, want := summarizer.FirstLine(in), "real content"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFirstLineEmpty(t *testing.T) {
	for _, in := range []string{"", "\n", "   \n\n", "//\n//"} {
		if got := summarizer.FirstLine(in); got != "" {
			t.Errorf("FirstLine(%q) = %q, want empty", in, got)
		}
	}
}

func TestFirstLinePlainText(t *testing.T) {
	if got, want := summarizer.FirstLine("plain text\nmore"), "plain text"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFirstLineLeadingSpaceInContent(t *testing.T) {
	// Leading whitespace inside a comment line is trimmed, so "   foo" yields "foo".
	if got, want := summarizer.FirstLine("   foo\nbar"), "foo"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
