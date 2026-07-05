package store_test

import (
	"context"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
	javascriptgrammar "github.com/smacker/go-tree-sitter/javascript"
	golanggrammar "github.com/smacker/go-tree-sitter/golang"
	typescriptgrammar "github.com/smacker/go-tree-sitter/typescript/typescript"

	"github.com/kellenff/yactt/internal/store"
)

// parseLang runs one of the three grammars against an inline snippet and
// returns the root. Local helper — `internal/parser/parser_test.go` has
// equivalents, but they're package-private to the parser test package.
func parseLang(t *testing.T, lang, src string) *sitter.Node {
	t.Helper()
	var grammar *sitter.Language
	switch lang {
	case "go":
		grammar = golanggrammar.GetLanguage()
	case "ts":
		grammar = typescriptgrammar.GetLanguage()
	case "js":
		grammar = javascriptgrammar.GetLanguage()
	default:
		t.Fatalf("parseLang: unknown lang %q", lang)
	}
	root, err := sitter.ParseCtx(context.Background(), []byte(src), grammar)
	if err != nil {
		t.Fatalf("ParseCtx: %v", err)
	}
	if root == nil {
		t.Fatal("ParseCtx returned nil root")
	}
	return root
}

// findFirstChild returns the first child of root whose Type() matches.
func findFirstChild(root *sitter.Node, typ string) *sitter.Node {
	for i := 0; i < int(root.ChildCount()); i++ {
		ch := root.Child(i)
		if ch != nil && ch.Type() == typ {
			return ch
		}
	}
	return nil
}

// TestExtractImportPath_Go pins the Go `import_declaration` branch.
func TestExtractImportPath_Go(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"single quoted", `package x
import "fmt"
`, "fmt"},
		{"grouped", `package x
import (
	"fmt"
	"github.com/foo/bar"
)
`, "fmt"},
		{"aliased", `package x
import f "fmt"
`, "fmt"},
		{"dot import", `package x
import . "fmt"
`, "fmt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := parseLang(t, "go", tc.src)
			imp := findFirstChild(root, "import_declaration")
			if imp == nil {
				t.Fatalf("no import_declaration in parse tree")
			}
			got := store.ExtractImportPath(imp, []byte(tc.src))
			if got != tc.want {
				t.Errorf("ExtractImportPath = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestExtractImportPath_TS pins the TypeScript `import_statement` branch.
func TestExtractImportPath_TS(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"side-effect", `import "side-effect";`, "side-effect"},
		{"named", `import { foo } from "./bar";`, "./bar"},
		{"namespace", `import * as Bar from "./baz";`, "./baz"},
		{"default", `import Default from "./mod";`, "./mod"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := parseLang(t, "ts", tc.src)
			imp := findFirstChild(root, "import_statement")
			if imp == nil {
				t.Fatalf("no import_statement in parse tree")
			}
			got := store.ExtractImportPath(imp, []byte(tc.src))
			if got != tc.want {
				t.Errorf("ExtractImportPath = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestExtractImportPath_JS pins the JavaScript `import_statement` branch.
// JS shares the same node kind as TS — included to confirm the parser
// dispatch doesn't depend on TS-only shapes.
func TestExtractImportPath_JS(t *testing.T) {
	root := parseLang(t, "js", `import { x } from "./y";`)
	imp := findFirstChild(root, "import_statement")
	if imp == nil {
		t.Fatalf("no import_statement in parse tree")
	}
	got := store.ExtractImportPath(imp, []byte(`import { x } from "./y";`))
	if got != "./y" {
		t.Errorf("ExtractImportPath = %q, want %q", got, "./y")
	}
}

// TestExtractImportPath_NonImportNode confirms nil / non-import input
// returns "" without panicking — guards the dispatch on `n.Type()`.
func TestExtractImportPath_NonImportNode(t *testing.T) {
	root := parseLang(t, "go", `package x
func F() {}
`)
	fn := findFirstChild(root, "function_declaration")
	if fn == nil {
		t.Fatalf("no function_declaration in parse tree")
	}
	if got := store.ExtractImportPath(fn, []byte(`package x
func F() {}
`)); got != "" {
		t.Errorf("ExtractImportPath on function = %q, want empty", got)
	}
	if got := store.ExtractImportPath(nil, nil); got != "" {
		t.Errorf("ExtractImportPath(nil, nil) = %q, want empty", got)
	}
}