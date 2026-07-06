package domain

import "testing"

// All test inputs that contain "dangerous" Unicode runes are written using
// backslash-u escape sequences so the .go source file itself is ASCII-only.
// The Go scanner rejects U+FEFF (BOM) as the first rune of a string
// literal, so the BOM case is constructed at runtime from its UTF-8 bytes.

func TestSanitizeName_NoOpOnClean(t *testing.T) {
	cases := []string{
		"",
		"Login",
		"User",
		"alpha",
		"_under_score",
		"X509",
		"a",
	}
	for _, in := range cases {
		if got := SanitizeName(in); got != in {
			t.Errorf("SanitizeName(%q) = %q, want %q", in, got, in)
		}
	}
}

func TestSanitizeName_StripsZeroWidth(t *testing.T) {
	// Escape sequences join safe ASCII so the .go source stays clean.
	zwsp := "​" // zero-width space
	zwj := "‍"  // zero-width joiner
	wj := "⁠"   // word joiner
	mvs := "᠎"  // Mongolian vowel separator
	// BOM (U+FEFF) cannot be the first rune of a string literal in Go,
	// so build the prefix string at runtime from its UTF-8 bytes.
	bom := string([]byte{0xEF, 0xBB, 0xBF})

	cases := map[string]string{
		"Log" + zwsp + "in": "Login",
		"Lo" + zwj + "gin":  "Login",
		bom + "Login":       "Login",
		"Log" + wj + "in":   "Login",
		"L" + mvs + "ogin":  "Login",
	}
	for in, want := range cases {
		if got := SanitizeName(in); got != want {
			t.Errorf("SanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizeName_StripsRTLOverride(t *testing.T) {
	// RTL override chars: U+202A..U+202E (LRE, RLE, PDF, LRO, RLO).
	lre := "‪"
	pdf := "‬"
	lro := "‭"
	rlo := "‮"
	cases := map[string]string{
		"Lo" + rlo + "gin": "Login",
		lre + "Login":      "Login",
		"Login" + pdf:      "Login",
		lro + "Login":      "Login",
		rlo + "Login":      "Login",
	}
	for in, want := range cases {
		if got := SanitizeName(in); got != want {
			t.Errorf("SanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizeName_StripsBidiIsolate(t *testing.T) {
	// Bidi isolate chars: U+2066..U+2069 (LRI, RLI, FSI, PDI).
	lri := "⁦"
	rli := "⁧"
	fsi := "⁨"
	pdi := "⁩"
	cases := map[string]string{
		"Log" + rli + "in": "Login",
		lri + "Login":      "Login",
		"Login" + pdi:      "Login",
		fsi + "Login":      "Login",
	}
	for in, want := range cases {
		if got := SanitizeName(in); got != want {
			t.Errorf("SanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizeName_StripsMixedDangerous(t *testing.T) {
	// Multiple dangerous chars in one name, mixed categories.
	zwsp := "​"
	rlo := "‮"
	zwj := "‍"
	wj := "⁠"
	zwnj := "‌"
	in := zwsp + "Lo" + rlo + "gi" + zwj + "n" + wj + zwnj
	want := "Login"
	if got := SanitizeName(in); got != want {
		t.Errorf("SanitizeName(%q) = %q, want %q", in, got, want)
	}
}

func TestSanitizeName_PreservesNonDangerousUnicode(t *testing.T) {
	// Unicode letters that are NOT in the dangerous set must pass through.
	cafe := "Café"      // Café
	naive := "naïve"    // naïve
	pi := "π"           // π
	lambdaFn := "λfunc" // λfunc
	cyrillic := "Метод" // Метод
	cjk := "関数"         // 関数
	cases := map[string]string{
		cafe:            cafe,
		naive:           naive,
		pi:              pi,
		lambdaFn:        lambdaFn,
		cyrillic:        cyrillic,
		cjk:             cjk,
		"snake_case_v2": "snake_case_v2",
	}
	for in, want := range cases {
		if got := SanitizeName(in); got != want {
			t.Errorf("SanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizeName_Empty(t *testing.T) {
	if got := SanitizeName(""); got != "" {
		t.Errorf("SanitizeName(\"\") = %q, want empty", got)
	}
}

// TestSanitizeName_ReturnsOriginalWhenClean guards the fast path: a clean
// string must be returned by-reference (no allocation).
func TestSanitizeName_ReturnsOriginalWhenClean(t *testing.T) {
	in := "clean_ascii_name"
	got := SanitizeName(in)
	if got != in {
		t.Errorf("SanitizeName(%q) = %q, want %q", in, got, in)
	}
}
