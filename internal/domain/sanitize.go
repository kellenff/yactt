// Package domain sanitize.go: identifier-name egress filter.
//
// SanitizeName is the single egress filter applied to every identifier name
// returned by yactt's MCP tools. It strips the Unicode characters that an
// attacker could use to disguise an identifier — zero-width joiners and
// bidi-override controls — so the name the agent sees cannot be visually
// different from the name yactt actually matched against. Raw bytes are
// preserved in the `tokens` layer for callers that need them; SanitizeName
// runs only on the canonical name surfaced in `Node.Name`, parser
// summaries, and search hit IDs.
//
// Per AST05 (OWASP Agentic Skills Top 10), untrusted identifier names are
// an injection vector. Stripping at egress means a name like "Log​in"
// cannot masquerade as "Login" in any answer yactt emits.
package domain

// SanitizeName returns s with every "dangerous" Unicode rune removed:
//
//   - U+200B (zero-width space)
//
//   - U+200C (zero-width non-joiner)
//
//   - U+200D (zero-width joiner)
//
//   - U+FEFF (BOM / zero-width no-break space)
//
//   - U+2060 (word joiner)
//
//   - U+180E (Mongolian vowel separator)
//
//   - U+202A–U+202E (LRE, RLE, PDF, LRO, RLO — bidi embedding/override)
//
//   - U+2066–U+2069 (LRI, RLI, FSI, PDI — bidi isolate)
//
// These are the categories the Unicode Security Mechanisms (TR39) call
// out as "potentially misleading" in identifier contexts. We strip rather
// than escape because the goal is to match the same name the rest of the
// codebase uses for that symbol — a name with a zero-width char in the
// middle is just a different symbol from yactt's perspective.
//
// The common case (clean ASCII) is one allocation: we scan and return
// the original string if no dangerous rune is present.
func SanitizeName(s string) string {
	if s == "" || !containsDangerous(s) {
		return s
	}
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); {
		r, size := decodeRune(s, i)
		if !isDangerousRune(r) {
			b = appendRune(b, r)
		}
		i += size
	}
	return string(b)
}

// containsDangerous is a fast pre-check. It returns true as soon as it
// sees any byte that could start a dangerous rune (zero-width and bidi
// control runes all live in UTF-8 ranges starting with 0xE1 / 0xE2 /
// 0xEF). Without this short-circuit the happy-path caller (every clean
// identifier in the codebase) would pay an unnecessary allocation per
// call.
func containsDangerous(s string) bool {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case 0xE1, // U+180E Mongolian vowel separator
			0xE2, // U+200B..U+202E, U+2060, U+2066..U+2069 (zero-width + bidi)
			0xEF: // U+FEFF BOM / zero-width no-break space
			return true
		}
	}
	return false
}

// decodeRune is a minimal UTF-8 decoder for our purposes: we don't need
// to validate, just classify. Invalid sequences are treated as a single
// byte of width 1 so the loop terminates.
func decodeRune(s string, i int) (rune, int) {
	b := s[i]
	switch {
	case b < 0x80:
		return rune(b), 1
	case b < 0xC2:
		return rune(b), 1 // invalid leading byte; skip
	case b < 0xE0:
		if i+1 >= len(s) {
			return rune(b), 1
		}
		return rune(b&0x1F)<<6 | rune(s[i+1]&0x3F), 2
	case b < 0xF0:
		if i+2 >= len(s) {
			return rune(b), 1
		}
		return rune(b&0x0F)<<12 | rune(s[i+1]&0x3F)<<6 | rune(s[i+2]&0x3F), 3
	default:
		if i+3 >= len(s) {
			return rune(b), 1
		}
		return rune(b&0x07)<<18 | rune(s[i+1]&0x3F)<<12 | rune(s[i+2]&0x3F)<<6 | rune(s[i+3]&0x3F), 4
	}
}

func appendRune(b []byte, r rune) []byte {
	switch {
	case r < 0x80:
		return append(b, byte(r))
	case r < 0x800:
		return append(b, byte(0xC0|(r>>6)), byte(0x80|(r&0x3F)))
	case r < 0x10000:
		return append(b, byte(0xE0|(r>>12)), byte(0x80|((r>>6)&0x3F)), byte(0x80|(r&0x3F)))
	default:
		return append(b, byte(0xF0|(r>>18)), byte(0x80|((r>>12)&0x3F)), byte(0x80|((r>>6)&0x3F)), byte(0x80|(r&0x3F)))
	}
}

// isDangerousRune reports whether r is one of the chars we strip from
// identifier names.
func isDangerousRune(r rune) bool {
	switch r {
	case 0x200B, // zero-width space
		0x200C, // zero-width non-joiner
		0x200D, // zero-width joiner
		0xFEFF, // BOM / zero-width no-break space
		0x2060, // word joiner
		0x180E: // Mongolian vowel separator
		return true
	}
	// Bidi controls: 0x202A..0x202E and 0x2066..0x2069.
	if r >= 0x202A && r <= 0x202E {
		return true
	}
	if r >= 0x2066 && r <= 0x2069 {
		return true
	}
	return false
}
