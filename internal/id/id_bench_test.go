package id_test

import (
	"testing"

	"github.com/kellenff/yactt/internal/id"
	"github.com/kellenff/yactt/internal/parser"
)

// BenchmarkFor_Kinds exercises the kind-routing switch in id.For.
// Per-symbol hot path — every Load pulls every Symbol through this.
//
// ponytail: covers every routing branch (function / method / class / module)
// inline rather than reading a real repo, so the bench is independent of
// the parser package's internals.
func BenchmarkFor_Kinds(b *testing.B) {
	syms := []parser.Symbol{
		{Kind: "function_declaration", Name: "Login"},
		{Kind: "method_declaration", Name: "Greet", Receiver: "User"},
		{Kind: "type_declaration", Name: "Session"},
		{Kind: "class_declaration", Name: "User"},
		{Kind: "interface_declaration", Name: "Repository"},
		{Kind: "module", Name: "auth"},
	}
	pkg := "auth"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, s := range syms {
			_ = id.For(s, pkg)
		}
	}
}

// BenchmarkJoinDotted measures the dotted-segment joiner used to compose
// fn:/meth:/class: IDs across Go (3-segment), TS/JS (2-segment for methods
// without a pkg), and the edge case of all-empty segments.
func BenchmarkJoinDotted(b *testing.B) {
	cases := []struct {
		pkg  string
		rest []string
	}{
		{"auth", []string{"Server", "Login"}},
		{"payments", []string{"Charge"}},
		{"", []string{"User", "greet"}},
		{"", nil},
		{"auth", []string{"", "Server", "Login"}},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, c := range cases {
			_ = id.JoinDotted(c.pkg, c.rest...)
		}
	}
}
