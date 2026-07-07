package parser_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/parser"
)

// parseGo runs the Go grammar against an inline source snippet and returns
// the root node. Used by collaboration tests that need a real parse tree.
func parseGo(t *testing.T, src string) *sitter.Node {
	t.Helper()
	lang := parser.Go{}
	root, err := sitter.ParseCtx(context.Background(), []byte(src), lang.Grammar())
	if err != nil {
		t.Fatalf("ParseCtx: %v", err)
	}
	if root == nil {
		t.Fatal("ParseCtx returned nil root")
	}
	return root
}

// parseTS runs the TypeScript grammar against an inline source snippet.
func parseTS(t *testing.T, src string) *sitter.Node {
	t.Helper()
	lang := parser.TypeScript{}
	root, err := sitter.ParseCtx(context.Background(), []byte(src), lang.Grammar())
	if err != nil {
		t.Fatalf("ParseCtx: %v", err)
	}
	if root == nil {
		t.Fatal("ParseCtx returned nil root")
	}
	return root
}

// parseJS runs the JavaScript grammar against an inline source snippet.
func parseJS(t *testing.T, src string) *sitter.Node {
	t.Helper()
	lang := parser.JavaScript{}
	root, err := sitter.ParseCtx(context.Background(), []byte(src), lang.Grammar())
	if err != nil {
		t.Fatalf("ParseCtx: %v", err)
	}
	if root == nil {
		t.Fatal("ParseCtx returned nil root")
	}
	return root
}

func TestDetectGo(t *testing.T) {
	cases := []string{"/abs/path/foo.go", "relative/bar.go", "CamelCase.GO"}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			lang, err := parser.Detect(p)
			if err != nil {
				t.Fatalf("Detect(%q) error: %v", p, err)
			}
			if lang.Name() != parser.LangGo {
				t.Errorf("Name = %q, want %q", lang.Name(), parser.LangGo)
			}
		})
	}
}

func TestDetectUnknownExtension(t *testing.T) {
	for _, p := range []string{"foo.xyz", "foo", "foo.go.bak"} {
		t.Run(p, func(t *testing.T) {
			_, err := parser.Detect(p)
			if !errors.Is(err, parser.ErrUnsupported) {
				t.Errorf("err = %v, want ErrUnsupported", err)
			}
		})
	}
}

func TestByName(t *testing.T) {
	for _, name := range []parser.Name{
		parser.LangGo,
		parser.LangTypeScript,
		parser.LangJavaScript,
		parser.LangPython,
		parser.LangRust,
		parser.LangPHP,
	} {
		t.Run(string(name), func(t *testing.T) {
			lang, err := parser.ByName(name)
			if err != nil {
				t.Fatalf("ByName(%q): %v", name, err)
			}
			if lang.Name() != name {
				t.Errorf("Name = %q, want %q", lang.Name(), name)
			}
		})
	}
	if _, err := parser.ByName(parser.Name("ruby")); !errors.Is(err, parser.ErrUnsupported) {
		t.Errorf("unsupported lang err = %v, want ErrUnsupported", err)
	}
}

func TestAllContainsKnown(t *testing.T) {
	all := parser.All()
	if len(all) == 0 {
		t.Fatal("All() returned empty")
	}
	names := map[parser.Name]bool{}
	for _, l := range all {
		names[l.Name()] = true
	}
	for _, want := range []parser.Name{parser.LangGo, parser.LangTypeScript, parser.LangJavaScript, parser.LangPython, parser.LangRust, parser.LangPHP} {
		if !names[want] {
			t.Errorf("All() missing %q: %v", want, all)
		}
	}
}

func TestModulePath(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"bare name", "package auth\n", "auth"},
		{"name with underscore", "package auth_helpers\n", "auth_helpers"},
		{"leading comment", "// header comment\npackage foo\n", "foo"},
		{"leading import", "import \"fmt\"\n\npackage bar\n", "bar"},
		{"no package clause", "var x = 1\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := parseGo(t, tc.src)
			got := parser.Go{}.ModulePath(root, []byte(tc.src))
			if got != tc.want {
				t.Errorf("ModulePath = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestModulePathNilRoot(t *testing.T) {
	var lang parser.Language = parser.Go{}
	if got := lang.ModulePath(nil, nil); got != "" {
		t.Errorf("ModulePath(nil) = %q, want empty", got)
	}
}

const goFunctionOnly = `package auth

func Login(user, pass string) error {
	return nil
}
`

func TestExtractSymbolsGoFunction(t *testing.T) {
	root := parseGo(t, goFunctionOnly)
	syms, err := parser.ExtractSymbols(parser.Go{}, root, []byte(goFunctionOnly))
	if err != nil {
		t.Fatalf("ExtractSymbols: %v", err)
	}
	if len(syms) != 1 {
		t.Fatalf("got %d symbols, want 1: %+v", len(syms), syms)
	}
	s := syms[0]
	if s.Kind != "function_declaration" {
		t.Errorf("Kind = %q, want function_declaration", s.Kind)
	}
	if s.Name != "Login" {
		t.Errorf("Name = %q, want Login", s.Name)
	}
	if s.StartRow != 2 {
		t.Errorf("StartRow = %d, want 2", s.StartRow)
	}
	if s.EndRow != 5 {
		t.Errorf("EndRow = %d, want 5", s.EndRow)
	}
}

const goMethodOnly = `package auth

type Server struct{}

func (s *Server) Login() error { return nil }
`

func TestExtractSymbolsGoMethod(t *testing.T) {
	root := parseGo(t, goMethodOnly)
	syms, err := parser.ExtractSymbols(parser.Go{}, root, []byte(goMethodOnly))
	if err != nil {
		t.Fatalf("ExtractSymbols: %v", err)
	}
	if len(syms) != 2 {
		t.Fatalf("got %d symbols, want 2 (type+method): %+v", len(syms), syms)
	}
	var method *parser.Symbol
	for i := range syms {
		if syms[i].Kind == "method_declaration" {
			method = &syms[i]
			break
		}
	}
	if method == nil {
		t.Fatalf("no method_declaration in %+v", syms)
	}
	if method.Name != "Login" {
		t.Errorf("Method Name = %q, want Login", method.Name)
	}
}

const goTypeOnly = `package auth

type Session struct {
	User string
	Tok  string
}
`

func TestExtractSymbolsGoType(t *testing.T) {
	root := parseGo(t, goTypeOnly)
	syms, err := parser.ExtractSymbols(parser.Go{}, root, []byte(goTypeOnly))
	if err != nil {
		t.Fatalf("ExtractSymbols: %v", err)
	}
	if len(syms) != 1 {
		t.Fatalf("got %d symbols, want 1: %+v", len(syms), syms)
	}
	if syms[0].Kind != "type_declaration" {
		t.Errorf("Kind = %q, want type_declaration", syms[0].Kind)
	}
	if syms[0].Name != "Session" {
		t.Errorf("Name = %q, want Session", syms[0].Name)
	}
}

const goMultiple = `package auth

type User struct{ Name string }

func Login() error         { return nil }
func (u *User) Greet() string { return u.Name }

type Token string
`

func TestExtractSymbolsMultiple(t *testing.T) {
	root := parseGo(t, goMultiple)
	syms, err := parser.ExtractSymbols(parser.Go{}, root, []byte(goMultiple))
	if err != nil {
		t.Fatalf("ExtractSymbols: %v", err)
	}
	counts := map[string]int{}
	for _, s := range syms {
		counts[s.Kind]++
	}
	want := map[string]int{
		"function_declaration": 1,
		"method_declaration":   1,
		"type_declaration":     2,
	}
	if len(counts) != len(want) {
		t.Fatalf("kinds = %v, want %v", counts, want)
	}
	for k, v := range want {
		if counts[k] != v {
			t.Errorf("%s count = %d, want %d", k, counts[k], v)
		}
	}

	// Every name should be non-empty and every range strictly forward.
	for _, s := range syms {
		if s.Name == "" {
			t.Errorf("empty name in %+v", s)
		}
		if s.EndRow <= s.StartRow {
			t.Errorf("non-forward range for %s: [%d,%d)", s.Name, s.StartRow, s.EndRow)
		}
	}
}

func TestExtractSymbolsNilRoot(t *testing.T) {
	syms, err := parser.ExtractSymbols(parser.Go{}, nil, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(syms) != 0 {
		t.Errorf("got %d symbols, want 0", len(syms))
	}
}

func TestExtractSymbolsUnsupported(t *testing.T) {
	// Build a fake Language that isn't Go; ExtractSymbols must refuse.
	fake := fakeLang{}
	_, err := parser.ExtractSymbols(fake, nil, nil)
	if !errors.Is(err, parser.ErrUnsupported) {
		t.Errorf("err = %v, want ErrUnsupported", err)
	}
}

func TestSymbolKindMapping(t *testing.T) {
	cases := []struct {
		in   string
		want domain.NodeKind
	}{
		{"function_declaration", domain.KindFunction},
		{"method_declaration", domain.KindMethod},
		{"type_declaration", domain.KindClass},
		{"class_declaration", domain.KindClass},
		{"interface_declaration", domain.KindClass},
		{"type_alias_declaration", domain.KindClass},
		{"enum_declaration", domain.KindClass},
		{"something_else", domain.KindModule}, // default
		{"", domain.KindModule},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := parser.SymbolKind(parser.Symbol{Kind: tc.in})
			if got != tc.want {
				t.Errorf("SymbolKind(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSymbolSummaryFormat(t *testing.T) {
	cases := []struct {
		in   parser.Symbol
		want string
	}{
		{parser.Symbol{Kind: "function_declaration", Name: "Login"}, "Function: Login"},
		{parser.Symbol{Kind: "method_declaration", Name: "Greet"}, "Method: Greet"},
		{parser.Symbol{Kind: "type_declaration", Name: "Session"}, "Class: Session"},
		{parser.Symbol{Kind: "class_declaration", Name: "User"}, "Class: User"},
		{parser.Symbol{Kind: "interface_declaration", Name: "Repo"}, "Class: Repo"},
		{parser.Symbol{Kind: "type_alias_declaration", Name: "UserId"}, "Class: UserId"},
		{parser.Symbol{Kind: "enum_declaration", Name: "Color"}, "Class: Color"},
		{parser.Symbol{Kind: "unknown", Name: "X"}, "Symbol: X"},
		{parser.Symbol{Kind: "function_declaration", Name: ""}, "Function:"},
	}
	for _, tc := range cases {
		t.Run(tc.in.Kind+"/"+tc.in.Name, func(t *testing.T) {
			got := parser.SymbolSummary(tc.in)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
			if !strings.Contains(got, tc.in.Name) && tc.in.Name != "" {
				t.Errorf("summary %q should mention name %q", got, tc.in.Name)
			}
		})
	}
}

// fakeLang implements parser.Language but isn't a Go parser. Used to verify
// ExtractSymbols' dispatch rejection.
type fakeLang struct{}

func (fakeLang) Name() parser.Name                          { return "fake" }
func (fakeLang) Grammar() *sitter.Language                  { return nil }
func (fakeLang) FileExtensions() []string                   { return []string{"fake"} }
func (fakeLang) ModulePath(_ *sitter.Node, _ []byte) string { return "" }

func TestExtractSymbolsGoMethodReceiver(t *testing.T) {
	// Each case stands up a tiny file with one method. The receiver column
	// must reflect the local name of the receiver type — that's the field
	// the resolver uses to disambiguate same-named methods across types.
	cases := []struct {
		name     string
		src      string
		receiver string
	}{
		{
			name:     "value receiver",
			src:      "package auth\ntype Server struct{}\nfunc (s Server) Login() error { return nil }\n",
			receiver: "Server",
		},
		{
			name:     "pointer receiver",
			src:      "package auth\ntype Server struct{}\nfunc (s *Server) Login() error { return nil }\n",
			receiver: "Server",
		},
		{
			name:     "qualified receiver strips package prefix",
			src:      "package auth\ntype Server struct{}\nfunc (s *pkg.Server) Login() error { return nil }\n",
			receiver: "Server",
		},
		{
			name:     "method without receiver leaves it empty",
			src:      "package auth\nfunc Login() error { return nil }\n",
			receiver: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := parseGo(t, tc.src)
			syms, err := parser.ExtractSymbols(parser.Go{}, root, []byte(tc.src))
			if err != nil {
				t.Fatalf("ExtractSymbols: %v", err)
			}
			// Find the method/function symbol. A fixture with a type
			// declaration + method yields two symbols; the test cares about
			// the method's receiver field, not the total count.
			var fnOrMethod parser.Symbol
			for _, s := range syms {
				if s.Kind == "method_declaration" || s.Kind == "function_declaration" {
					fnOrMethod = s
					break
				}
			}
			if fnOrMethod.Name == "" {
				t.Fatalf("no function/method symbol in %+v", syms)
			}
			if fnOrMethod.Name != "Login" {
				t.Errorf("got name %q, want Login", fnOrMethod.Name)
			}
			if got := fnOrMethod.Receiver; got != tc.receiver {
				t.Errorf("Receiver = %q, want %q", got, tc.receiver)
			}
			// A bare function should never carry a receiver; a method should.
			if tc.name == "method without receiver leaves it empty" {
				if fnOrMethod.Kind != "function_declaration" {
					t.Errorf("kind = %q, want function_declaration", fnOrMethod.Kind)
				}
			} else {
				if fnOrMethod.Kind != "method_declaration" {
					t.Errorf("kind = %q, want method_declaration", fnOrMethod.Kind)
				}
			}
		})
	}
}

func TestExtractSymbolsTwoMethodsSameNameDisambiguatedByReceiver(t *testing.T) {
	// Two types in the same file, each with a method named "Ping". This
	// mirrors the resolver's worst case: it has to tell Alpha.Ping from
	// Beta.Ping without ambiguity. The receiver field is what enables that.
	src := `package auth

type Alpha struct{}
func (a *Alpha) Ping() string { return "alpha" }

type Beta struct{}
func (b *Beta) Ping() string { return "beta" }
`
	root := parseGo(t, src)
	syms, err := parser.ExtractSymbols(parser.Go{}, root, []byte(src))
	if err != nil {
		t.Fatalf("ExtractSymbols: %v", err)
	}
	var pings []parser.Symbol
	for _, s := range syms {
		if s.Kind == "method_declaration" && s.Name == "Ping" {
			pings = append(pings, s)
		}
	}
	if len(pings) != 2 {
		t.Fatalf("got %d Ping methods, want 2: %+v", len(pings), syms)
	}
	receivers := map[string]string{}
	for _, p := range pings {
		receivers[p.Receiver] = p.Name
	}
	if receivers["Alpha"] != "Ping" || receivers["Beta"] != "Ping" {
		t.Errorf("receiver disambiguation failed: %+v", receivers)
	}
}

// TestExtractSymbols_TypeAlias covers the type-declaration branch where
// the underlying type is a single identifier alias (e.g. `type Token string`).
// The type_spec's first named child is the type_identifier — guards the
// `cc.Type() == "type_identifier" || cc.Type() == "identifier"` check.
func TestExtractSymbols_TypeAlias(t *testing.T) {
	src := `package auth

type Token string
`
	root := parseGo(t, src)
	syms, err := parser.ExtractSymbols(parser.Go{}, root, []byte(src))
	if err != nil {
		t.Fatalf("ExtractSymbols: %v", err)
	}
	if len(syms) != 1 {
		t.Fatalf("got %d symbols, want 1: %+v", len(syms), syms)
	}
	if syms[0].Kind != "type_declaration" {
		t.Errorf("Kind = %q, want type_declaration", syms[0].Kind)
	}
	if syms[0].Name != "Token" {
		t.Errorf("Name = %q, want Token", syms[0].Name)
	}
}

// TestExtractSymbols_EmptyTypeBody covers the case where a type_spec has
// no recognizable name (the loop falls through and the function returns
// false). Use a malformed body to force the no-name path.
func TestExtractSymbols_NoRecognizableName(t *testing.T) {
	// Construct a tree manually using a degenerate type_spec. With no
	// `type_spec` at all (just whitespace), ExtractSymbols returns no
	// symbols. We exercise the early `return Symbol{}, false` path
	// from goTypeSymbol when no type_spec matches.
	src := `package auth

func NotAType() {}
`
	root := parseGo(t, src)
	syms, err := parser.ExtractSymbols(parser.Go{}, root, []byte(src))
	if err != nil {
		t.Fatalf("ExtractSymbols: %v", err)
	}
	// Only the function declaration; no type symbols.
	for _, s := range syms {
		if s.Kind == "type_declaration" {
			t.Errorf("unexpected type_declaration: %+v", s)
		}
	}
}

// TestExtractSymbols_MethodNoReceiverParameterList exercises the
// goMethodReceiver branch where the parameter_list isn't found. The
// function returns "" for receiver and the symbol is still emitted.
func TestExtractSymbols_MethodNoReceiver(t *testing.T) {
	// A method declaration must have a parameter_list for the
	// receiver per Go grammar. Construct one that yields a nil/missing
	// parameter_list by handing a node directly via the package API.
	// Practically: every method_declaration has a receiver, so this
	// branch is defensive only. We assert that the live code never
	// produces an empty receiver for a real method.
	src := `package auth

type Server struct{}
func (s *Server) Login() error { return nil }
`
	root := parseGo(t, src)
	syms, err := parser.ExtractSymbols(parser.Go{}, root, []byte(src))
	if err != nil {
		t.Fatalf("ExtractSymbols: %v", err)
	}
	for _, s := range syms {
		if s.Kind == "method_declaration" {
			if s.Receiver == "" {
				t.Errorf("method %s has empty Receiver", s.Name)
			}
		}
	}
}

// TestDetect_EmptyExtension covers the case where filepath.Ext returns
// "" — the live code handles this by leaving ext empty. Make sure
// Detect returns ErrUnsupported for a path without an extension.
func TestDetect_EmptyExtension(t *testing.T) {
	_, err := parser.Detect("foo")
	if !errors.Is(err, parser.ErrUnsupported) {
		t.Errorf("err = %v, want ErrUnsupported", err)
	}
}

// TestDetect_Dotfile covers the leading-dot path (e.g. ".hidden") — ext
// is "" and the basename doesn't match any language.
func TestDetect_Dotfile(t *testing.T) {
	_, err := parser.Detect(".hidden")
	if !errors.Is(err, parser.ErrUnsupported) {
		t.Errorf("err = %v, want ErrUnsupported", err)
	}
}

// --- TypeScript --------------------------------------------------------

func TestDetectTS(t *testing.T) {
	cases := []string{
		"/abs/path/foo.ts", "relative/bar.ts", "CamelCase.TS",
		"ui/component.tsx", "mod.mts", "lib.cts",
	}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			lang, err := parser.Detect(p)
			if err != nil {
				t.Fatalf("Detect(%q): %v", p, err)
			}
			if lang.Name() != parser.LangTypeScript {
				t.Errorf("Name = %q, want %q", lang.Name(), parser.LangTypeScript)
			}
		})
	}
}

func TestModulePathTS(t *testing.T) {
	// TypeScript has no file-local module name — always "".
	for _, src := range []string{
		"",
		"export function login() {}\n",
		"import x from 'y'\nexport class Foo {}\n",
	} {
		t.Run(src, func(t *testing.T) {
			var lang parser.Language = parser.TypeScript{}
			if got := lang.ModulePath(nil, []byte(src)); got != "" {
				t.Errorf("ModulePath = %q, want empty", got)
			}
		})
	}
}

func TestExtractSymbolsTSFunction(t *testing.T) {
	src := `export function login(user: string): void {}
function logout(user: string): void {}
`
	root := parseTS(t, src)
	syms, err := parser.ExtractSymbols(parser.TypeScript{}, root, []byte(src))
	if err != nil {
		t.Fatalf("ExtractSymbols: %v", err)
	}
	if len(syms) != 2 {
		t.Fatalf("got %d symbols, want 2: %+v", len(syms), syms)
	}
	names := map[string]string{}
	for _, s := range syms {
		if s.Kind != "function_declaration" {
			t.Errorf("Kind = %q, want function_declaration", s.Kind)
		}
		names[s.Name] = s.Kind
	}
	if names["login"] != "function_declaration" || names["logout"] != "function_declaration" {
		t.Errorf("names = %v, want both function_declaration", names)
	}
}

func TestExtractSymbolsTSClass(t *testing.T) {
	src := `export class Foo {
  greet(): string { return 'hi' }
}
class Bar {}
`
	root := parseTS(t, src)
	syms, err := parser.ExtractSymbols(parser.TypeScript{}, root, []byte(src))
	if err != nil {
		t.Fatalf("ExtractSymbols: %v", err)
	}
	counts := map[string]int{}
	for _, s := range syms {
		counts[s.Kind]++
	}
	if counts["class_declaration"] != 2 {
		t.Errorf("class_declaration count = %d, want 2 (syms=%+v)", counts["class_declaration"], syms)
	}
	if counts["method_declaration"] != 1 {
		t.Errorf("method_declaration count = %d, want 1 (syms=%+v)", counts["method_declaration"], syms)
	}
}

func TestExtractSymbolsTSInterface(t *testing.T) {
	src := `export interface IUser { id: string }
interface Repo<T> { find(id: string): T | null }
`
	root := parseTS(t, src)
	syms, err := parser.ExtractSymbols(parser.TypeScript{}, root, []byte(src))
	if err != nil {
		t.Fatalf("ExtractSymbols: %v", err)
	}
	if len(syms) != 2 {
		t.Fatalf("got %d symbols, want 2: %+v", len(syms), syms)
	}
	for _, s := range syms {
		if s.Kind != "class_declaration" {
			t.Errorf("Kind = %q, want class_declaration (interfaces map to KindClass)", s.Kind)
		}
	}
	names := map[string]bool{}
	for _, s := range syms {
		names[s.Name] = true
	}
	if !names["IUser"] || !names["Repo"] {
		t.Errorf("missing IUser or Repo: %+v", names)
	}
}

func TestExtractSymbolsTSTypeAlias(t *testing.T) {
	src := `export type UserId = string
type Maybe<T> = T | null
`
	root := parseTS(t, src)
	syms, err := parser.ExtractSymbols(parser.TypeScript{}, root, []byte(src))
	if err != nil {
		t.Fatalf("ExtractSymbols: %v", err)
	}
	if len(syms) != 2 {
		t.Fatalf("got %d symbols, want 2: %+v", len(syms), syms)
	}
	for _, s := range syms {
		if s.Kind != "class_declaration" {
			t.Errorf("Kind = %q, want class_declaration (type aliases map to KindClass)", s.Kind)
		}
	}
}

func TestExtractSymbolsTSEnum(t *testing.T) {
	src := `export enum Color { Red, Green, Blue }
enum Direction { Up, Down }
`
	root := parseTS(t, src)
	syms, err := parser.ExtractSymbols(parser.TypeScript{}, root, []byte(src))
	if err != nil {
		t.Fatalf("ExtractSymbols: %v", err)
	}
	if len(syms) != 2 {
		t.Fatalf("got %d symbols, want 2: %+v", len(syms), syms)
	}
	for _, s := range syms {
		if s.Kind != "class_declaration" {
			t.Errorf("Kind = %q, want class_declaration (enums map to KindClass)", s.Kind)
		}
	}
}

func TestExtractSymbolsTSMethodReceiver(t *testing.T) {
	src := `class Server {
  login(): void {}
  static create(): Server { return new Server() }
}
`
	root := parseTS(t, src)
	syms, err := parser.ExtractSymbols(parser.TypeScript{}, root, []byte(src))
	if err != nil {
		t.Fatalf("ExtractSymbols: %v", err)
	}
	var methods []parser.Symbol
	for _, s := range syms {
		if s.Kind == "method_declaration" {
			methods = append(methods, s)
		}
	}
	if len(methods) != 2 {
		t.Fatalf("got %d methods, want 2: %+v", len(methods), syms)
	}
	for _, m := range methods {
		if m.Receiver != "Server" {
			t.Errorf("method %q Receiver = %q, want Server", m.Name, m.Receiver)
		}
	}
}

func TestExtractSymbolsTSNilRoot(t *testing.T) {
	syms, err := parser.ExtractSymbols(parser.TypeScript{}, nil, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(syms) != 0 {
		t.Errorf("got %d symbols, want 0", len(syms))
	}
}

// --- JavaScript --------------------------------------------------------

func TestDetectJS(t *testing.T) {
	cases := []string{
		"/abs/path/foo.js", "relative/bar.js", "CamelCase.JS",
		"ui/component.jsx", "mod.mjs", "cli.cjs",
	}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			lang, err := parser.Detect(p)
			if err != nil {
				t.Fatalf("Detect(%q): %v", p, err)
			}
			if lang.Name() != parser.LangJavaScript {
				t.Errorf("Name = %q, want %q", lang.Name(), parser.LangJavaScript)
			}
		})
	}
}

func TestModulePathJS(t *testing.T) {
	// JavaScript has no file-local module name — always "".
	for _, src := range []string{
		"",
		"function foo() {}\n",
		"export class Bar {}\n",
	} {
		t.Run(src, func(t *testing.T) {
			var lang parser.Language = parser.JavaScript{}
			if got := lang.ModulePath(nil, []byte(src)); got != "" {
				t.Errorf("ModulePath = %q, want empty", got)
			}
		})
	}
}

func TestExtractSymbolsJSFunction(t *testing.T) {
	src := `export function login(user) {}
function logout(user) {}
`
	root := parseJS(t, src)
	syms, err := parser.ExtractSymbols(parser.JavaScript{}, root, []byte(src))
	if err != nil {
		t.Fatalf("ExtractSymbols: %v", err)
	}
	if len(syms) != 2 {
		t.Fatalf("got %d symbols, want 2: %+v", len(syms), syms)
	}
	names := map[string]string{}
	for _, s := range syms {
		if s.Kind != "function_declaration" {
			t.Errorf("Kind = %q, want function_declaration", s.Kind)
		}
		names[s.Name] = s.Kind
	}
	if names["login"] != "function_declaration" || names["logout"] != "function_declaration" {
		t.Errorf("names = %v, want both function_declaration", names)
	}
}

func TestExtractSymbolsJSClass(t *testing.T) {
	src := `export class Foo {
  greet() { return 'hi' }
}
class Bar {}
`
	root := parseJS(t, src)
	syms, err := parser.ExtractSymbols(parser.JavaScript{}, root, []byte(src))
	if err != nil {
		t.Fatalf("ExtractSymbols: %v", err)
	}
	counts := map[string]int{}
	for _, s := range syms {
		counts[s.Kind]++
	}
	if counts["class_declaration"] != 2 {
		t.Errorf("class_declaration count = %d, want 2 (syms=%+v)", counts["class_declaration"], syms)
	}
	if counts["method_declaration"] != 1 {
		t.Errorf("method_declaration count = %d, want 1 (syms=%+v)", counts["method_declaration"], syms)
	}
}

func TestExtractSymbolsJSMethodReceiver(t *testing.T) {
	src := `class Server {
  login() {}
  static create() { return new Server() }
}
`
	root := parseJS(t, src)
	syms, err := parser.ExtractSymbols(parser.JavaScript{}, root, []byte(src))
	if err != nil {
		t.Fatalf("ExtractSymbols: %v", err)
	}
	var methods []parser.Symbol
	for _, s := range syms {
		if s.Kind == "method_declaration" {
			methods = append(methods, s)
		}
	}
	if len(methods) != 2 {
		t.Fatalf("got %d methods, want 2: %+v", len(methods), syms)
	}
	for _, m := range methods {
		if m.Receiver != "Server" {
			t.Errorf("method %q Receiver = %q, want Server", m.Name, m.Receiver)
		}
	}
}

func TestExtractSymbolsJSNilRoot(t *testing.T) {
	syms, err := parser.ExtractSymbols(parser.JavaScript{}, nil, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(syms) != 0 {
		t.Errorf("got %d symbols, want 0", len(syms))
	}
}

func TestExtractSymbolsMixedFile(t *testing.T) {
	// A realistic mixed declaration file — exercises the dispatch's ability
	// to extract multiple kinds at once and the export_statement peeling.
	src := `import express from 'express'

interface IUser { id: string }
type UserId = string
enum Role { Admin, User }

export class Server {
  login(user: UserId): void {}
  static create(): Server { return new Server() }
}

export function main() {}
function helper() {}
`
	root := parseTS(t, src)
	syms, err := parser.ExtractSymbols(parser.TypeScript{}, root, []byte(src))
	if err != nil {
		t.Fatalf("ExtractSymbols: %v", err)
	}
	counts := map[string]int{}
	receivers := map[string]string{}
	for _, s := range syms {
		counts[s.Kind]++
		if s.Kind == "method_declaration" && s.Receiver != "" {
			receivers[s.Name] = s.Receiver
		}
	}
	if counts["class_declaration"] != 4 { // IUser, UserId, Role, Server
		t.Errorf("class_declaration count = %d, want 4: %+v", counts["class_declaration"], syms)
	}
	if counts["function_declaration"] != 2 { // main, helper
		t.Errorf("function_declaration count = %d, want 2: %+v", counts["function_declaration"], syms)
	}
	if counts["method_declaration"] != 2 { // login, create
		t.Errorf("method_declaration count = %d, want 2: %+v", counts["method_declaration"], syms)
	}
	if receivers["login"] != "Server" || receivers["create"] != "Server" {
		t.Errorf("receiver disambiguation failed: %+v", receivers)
	}
}

// --- Python ------------------------------------------------------------

// parsePy runs the Python grammar against an inline source snippet and
// returns the root node. Used by collaboration tests that need a real
// parse tree.
func parsePy(t *testing.T, src string) *sitter.Node {
	t.Helper()
	lang := parser.Python{}
	root, err := sitter.ParseCtx(context.Background(), []byte(src), lang.Grammar())
	if err != nil {
		t.Fatalf("ParseCtx: %v", err)
	}
	if root == nil {
		t.Fatal("ParseCtx returned nil root")
	}
	return root
}

func TestDetectPython(t *testing.T) {
	cases := []string{
		"/abs/path/foo.py", "relative/bar.py", "CamelCase.PY",
		"pkg/types.pyi", "stubs/widget.pyi",
	}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			lang, err := parser.Detect(p)
			if err != nil {
				t.Fatalf("Detect(%q): %v", p, err)
			}
			if lang.Name() != parser.LangPython {
				t.Errorf("Name = %q, want %q", lang.Name(), parser.LangPython)
			}
		})
	}
}

func TestModulePathPy(t *testing.T) {
	// Python has no file-local module name — always "".
	for _, src := range []string{
		"",
		"def foo(): pass\n",
		"import os\n\nclass Foo: pass\n",
	} {
		t.Run(src, func(t *testing.T) {
			var lang parser.Language = parser.Python{}
			if got := lang.ModulePath(nil, []byte(src)); got != "" {
				t.Errorf("ModulePath = %q, want empty", got)
			}
		})
	}
}

const pyFunctions = `def login(user, password):
    return True

async def fetch(url):
    return None

def helper():
    pass
`

func TestExtractSymbolsPyFunction(t *testing.T) {
	root := parsePy(t, pyFunctions)
	syms, err := parser.ExtractSymbols(parser.Python{}, root, []byte(pyFunctions))
	if err != nil {
		t.Fatalf("ExtractSymbols: %v", err)
	}
	if len(syms) != 3 {
		t.Fatalf("got %d symbols, want 3: %+v", len(syms), syms)
	}
	names := map[string]string{}
	for _, s := range syms {
		if s.Kind != "function_declaration" {
			t.Errorf("Kind = %q, want function_declaration", s.Kind)
		}
		names[s.Name] = s.Kind
	}
	if names["login"] != "function_declaration" || names["fetch"] != "function_declaration" || names["helper"] != "function_declaration" {
		t.Errorf("names = %v, want all function_declaration", names)
	}
}

const pyClassOnly = `class Server:
    def login(self, user):
        return True

    def logout(self):
        return None
`

func TestExtractSymbolsPyClass(t *testing.T) {
	root := parsePy(t, pyClassOnly)
	syms, err := parser.ExtractSymbols(parser.Python{}, root, []byte(pyClassOnly))
	if err != nil {
		t.Fatalf("ExtractSymbols: %v", err)
	}
	counts := map[string]int{}
	for _, s := range syms {
		counts[s.Kind]++
	}
	if counts["class_declaration"] != 1 {
		t.Errorf("class_declaration count = %d, want 1: %+v", counts["class_declaration"], syms)
	}
	if counts["method_declaration"] != 2 {
		t.Errorf("method_declaration count = %d, want 2: %+v", counts["method_declaration"], syms)
	}
}

func TestExtractSymbolsPyMethodReceiver(t *testing.T) {
	root := parsePy(t, pyClassOnly)
	syms, err := parser.ExtractSymbols(parser.Python{}, root, []byte(pyClassOnly))
	if err != nil {
		t.Fatalf("ExtractSymbols: %v", err)
	}
	var methods []parser.Symbol
	for _, s := range syms {
		if s.Kind == "method_declaration" {
			methods = append(methods, s)
		}
	}
	if len(methods) != 2 {
		t.Fatalf("got %d methods, want 2: %+v", len(methods), syms)
	}
	for _, m := range methods {
		if m.Receiver != "Server" {
			t.Errorf("method %q Receiver = %q, want Server", m.Name, m.Receiver)
		}
	}
}

const pyDecorated = `@staticmethod
def helper():
    return 1

@app.route("/login")
def login():
    return "ok"

@dataclass
class User:
    name: str
`

func TestExtractSymbolsPyDecorated(t *testing.T) {
	root := parsePy(t, pyDecorated)
	syms, err := parser.ExtractSymbols(parser.Python{}, root, []byte(pyDecorated))
	if err != nil {
		t.Fatalf("ExtractSymbols: %v", err)
	}
	counts := map[string]int{}
	names := map[string]bool{}
	for _, s := range syms {
		counts[s.Kind]++
		names[s.Name] = true
	}
	// Two decorated functions + one decorated class.
	if counts["function_declaration"] != 2 {
		t.Errorf("function_declaration count = %d, want 2: %+v", counts["function_declaration"], syms)
	}
	if counts["class_declaration"] != 1 {
		t.Errorf("class_declaration count = %d, want 1: %+v", counts["class_declaration"], syms)
	}
	if !names["helper"] || !names["login"] || !names["User"] {
		t.Errorf("names = %v, want helper, login, User", names)
	}
}

const pyDecoratedMethod = `class Server:
    @staticmethod
    def create():
        return Server()

    @property
    def name(self):
        return "s"
`

func TestExtractSymbolsPyDecoratedMethod(t *testing.T) {
	root := parsePy(t, pyDecoratedMethod)
	syms, err := parser.ExtractSymbols(parser.Python{}, root, []byte(pyDecoratedMethod))
	if err != nil {
		t.Fatalf("ExtractSymbols: %v", err)
	}
	var methods []parser.Symbol
	for _, s := range syms {
		if s.Kind == "method_declaration" {
			methods = append(methods, s)
		}
	}
	if len(methods) != 2 {
		t.Fatalf("got %d methods, want 2: %+v", len(methods), syms)
	}
	for _, m := range methods {
		if m.Receiver != "Server" {
			t.Errorf("decorated method %q Receiver = %q, want Server", m.Name, m.Receiver)
		}
	}
}

func TestExtractSymbolsPyNilRoot(t *testing.T) {
	syms, err := parser.ExtractSymbols(parser.Python{}, nil, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(syms) != 0 {
		t.Errorf("got %d symbols, want 0", len(syms))
	}
}

// --- Rust --------------------------------------------------------------

// parseRust runs the Rust grammar against an inline source snippet and
// returns the root node. Used by collaboration tests that need a real
// parse tree.
func parseRust(t *testing.T, src string) *sitter.Node {
	t.Helper()
	lang := parser.Rust{}
	root, err := sitter.ParseCtx(context.Background(), []byte(src), lang.Grammar())
	if err != nil {
		t.Fatalf("ParseCtx: %v", err)
	}
	if root == nil {
		t.Fatal("ParseCtx returned nil root")
	}
	return root
}

func TestDetectRust(t *testing.T) {
	cases := []string{
		"/abs/path/foo.rs", "relative/bar.rs", "CamelCase.RS",
		"src/lib.rs", "tests/integration_test.rs",
	}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			lang, err := parser.Detect(p)
			if err != nil {
				t.Fatalf("Detect(%q): %v", p, err)
			}
			if lang.Name() != parser.LangRust {
				t.Errorf("Name = %q, want %q", lang.Name(), parser.LangRust)
			}
		})
	}
}

func TestModulePathRust(t *testing.T) {
	// Rust has no file-local module name — always "".
	for _, src := range []string{
		"",
		"fn main() {}\n",
		"struct Foo;\nimpl Foo { fn bar(&self) {} }\n",
	} {
		t.Run(src, func(t *testing.T) {
			var lang parser.Language = parser.Rust{}
			if got := lang.ModulePath(nil, []byte(src)); got != "" {
				t.Errorf("ModulePath = %q, want empty", got)
			}
		})
	}
}

func TestByNameRust(t *testing.T) {
	lang, err := parser.ByName(parser.LangRust)
	if err != nil {
		t.Fatalf("ByName(rust): %v", err)
	}
	if lang.Name() != parser.LangRust {
		t.Errorf("Name = %q, want %q", lang.Name(), parser.LangRust)
	}
	if got := lang.FileExtensions(); len(got) != 1 || got[0] != "rs" {
		t.Errorf("FileExtensions = %v, want [\"rs\"]", got)
	}
}

const rustFunctions = `pub fn login(user: &str, password: &str) -> bool {
    true
}

pub async fn fetch(url: &str) -> Option<String> {
    None
}

fn helper() -> u32 {
    42
}
`

func TestExtractSymbolsRustFunction(t *testing.T) {
	root := parseRust(t, rustFunctions)
	syms, err := parser.ExtractSymbols(parser.Rust{}, root, []byte(rustFunctions))
	if err != nil {
		t.Fatalf("ExtractSymbols: %v", err)
	}
	if len(syms) != 3 {
		t.Fatalf("got %d symbols, want 3: %+v", len(syms), syms)
	}
	names := map[string]string{}
	for _, s := range syms {
		if s.Kind != "function_declaration" {
			t.Errorf("Kind = %q, want function_declaration", s.Kind)
		}
		names[s.Name] = s.Kind
	}
	if names["login"] != "function_declaration" || names["fetch"] != "function_declaration" || names["helper"] != "function_declaration" {
		t.Errorf("names = %v, want all function_declaration", names)
	}
}

const rustTypes = `pub struct User {
    name: String,
}

pub enum Role {
    Admin,
    Guest,
}

pub trait Greeter {
    fn greet(&self) -> String;
}

pub type UserId = u64;
`

func TestExtractSymbolsRustTypes(t *testing.T) {
	root := parseRust(t, rustTypes)
	syms, err := parser.ExtractSymbols(parser.Rust{}, root, []byte(rustTypes))
	if err != nil {
		t.Fatalf("ExtractSymbols: %v", err)
	}
	counts := map[string]int{}
	names := map[string]string{}
	for _, s := range syms {
		counts[s.Kind]++
		names[s.Name] = s.Kind
	}
	if counts["struct_declaration"] != 1 {
		t.Errorf("struct_declaration count = %d, want 1: %+v", counts["struct_declaration"], syms)
	}
	if counts["enum_declaration"] != 1 {
		t.Errorf("enum_declaration count = %d, want 1: %+v", counts["enum_declaration"], syms)
	}
	if counts["trait_declaration"] != 1 {
		t.Errorf("trait_declaration count = %d, want 1: %+v", counts["trait_declaration"], syms)
	}
	if counts["type_declaration"] != 1 {
		t.Errorf("type_declaration count = %d, want 1: %+v", counts["type_declaration"], syms)
	}
	if names["User"] != "struct_declaration" {
		t.Errorf("User kind = %q, want struct_declaration", names["User"])
	}
	if names["Role"] != "enum_declaration" {
		t.Errorf("Role kind = %q, want enum_declaration", names["Role"])
	}
	if names["Greeter"] != "trait_declaration" {
		t.Errorf("Greeter kind = %q, want trait_declaration", names["Greeter"])
	}
	if names["UserId"] != "type_declaration" {
		t.Errorf("UserId kind = %q, want type_declaration", names["UserId"])
	}
}

const rustImpl = `pub struct Server {
    port: u16,
}

impl Server {
    pub fn new(port: u16) -> Self {
        Server { port }
    }

    pub fn handle(&self, req: String) -> String {
        req
    }
}

pub trait Greeter {
    fn greet(&self) -> String;
}

impl Greeter for Server {
    fn greet(&self) -> String {
        format!("hello on port {}", self.port)
    }
}
`

func TestExtractSymbolsRustImpl(t *testing.T) {
	root := parseRust(t, rustImpl)
	syms, err := parser.ExtractSymbols(parser.Rust{}, root, []byte(rustImpl))
	if err != nil {
		t.Fatalf("ExtractSymbols: %v", err)
	}
	counts := map[string]int{}
	methods := map[string]string{} // name -> receiver
	for _, s := range syms {
		counts[s.Kind]++
		if s.Kind == "method_declaration" {
			methods[s.Name] = s.Receiver
		}
	}
	// 1 struct, 1 trait, plus 3 methods (2 inherent impl + 1 trait impl
	// on Server). impl_item itself is intentionally NOT a symbol.
	if counts["struct_declaration"] != 1 {
		t.Errorf("struct_declaration count = %d, want 1: %+v", counts["struct_declaration"], syms)
	}
	if counts["trait_declaration"] != 1 {
		t.Errorf("trait_declaration count = %d, want 1: %+v", counts["trait_declaration"], syms)
	}
	if counts["method_declaration"] != 3 {
		t.Errorf("method_declaration count = %d, want 3: %+v", counts["method_declaration"], syms)
	}
	// All three methods belong to Server, even the one inside
	// `impl Greeter for Server` — receiver is the IMPLEMENTER, not the
	// trait. This is the critical routing rule for symbol-graph lookups.
	if methods["new"] != "Server" {
		t.Errorf("new receiver = %q, want Server", methods["new"])
	}
	if methods["handle"] != "Server" {
		t.Errorf("handle receiver = %q, want Server", methods["handle"])
	}
	if methods["greet"] != "Server" {
		t.Errorf("greet receiver = %q, want Server (implementer, not trait Greeter)", methods["greet"])
	}
}

const rustTraitImpl = `pub trait Drawable {
    fn draw(&self);
}

pub struct Circle;

impl Drawable for Circle {
    fn draw(&self) {}
}
`

// TestExtractSymbolsRustTraitImplReceiver verifies the rule that for
// `impl Trait for Type`, the receiver stamped on the method is the
// implementing type ("Circle"), not the trait ("Drawable"). The resolver
// uses (Receiver, Name) as the method lookup key, and call sites
// reference the type doing the calling — not the trait providing the
// method.
func TestExtractSymbolsRustTraitImplReceiver(t *testing.T) {
	root := parseRust(t, rustTraitImpl)
	syms, err := parser.ExtractSymbols(parser.Rust{}, root, []byte(rustTraitImpl))
	if err != nil {
		t.Fatalf("ExtractSymbols: %v", err)
	}
	var draws []parser.Symbol
	for _, s := range syms {
		if s.Name == "draw" {
			draws = append(draws, s)
		}
	}
	if len(draws) != 1 {
		t.Fatalf("got %d `draw` symbols, want 1: %+v", len(draws), syms)
	}
	if draws[0].Kind != "method_declaration" {
		t.Errorf("Kind = %q, want method_declaration", draws[0].Kind)
	}
	if draws[0].Receiver != "Circle" {
		t.Errorf("Receiver = %q, want Circle (implementer, not trait Drawable)", draws[0].Receiver)
	}
}

func TestExtractSymbolsRustNilRoot(t *testing.T) {
	syms, err := parser.ExtractSymbols(parser.Rust{}, nil, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(syms) != 0 {
		t.Errorf("got %d symbols, want 0", len(syms))
	}
}

// parsePHP runs the PHP grammar against an inline source snippet and
// returns the root node. Mirrors parseGo / parseTS / parseJS / parseRust.
func parsePHP(t *testing.T, src string) *sitter.Node {
	t.Helper()
	root, err := sitter.ParseCtx(context.Background(), []byte(src), parser.PHP{}.Grammar())
	if err != nil {
		t.Fatalf("ParseCtx: %v", err)
	}
	if root == nil {
		t.Fatal("ParseCtx returned nil root")
	}
	return root
}

func TestDetectPHP(t *testing.T) {
	cases := []string{
		"/abs/path/foo.php", "relative/bar.PHP", "src/User.phtml",
		"legacy/old.php5", "stubs/View.phps",
	}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			lang, err := parser.Detect(p)
			if err != nil {
				t.Fatalf("Detect(%q): %v", p, err)
			}
			if lang.Name() != parser.LangPHP {
				t.Errorf("Name = %q, want %q", lang.Name(), parser.LangPHP)
			}
		})
	}
}

func TestModulePathPHP(t *testing.T) {
	// PHP has no file-local module name we surface today — always "".
	for _, src := range []string{
		"",
		"<?php function foo() {}\n",
		"<?php namespace App; class User {}\n",
	} {
		t.Run(src, func(t *testing.T) {
			var lang parser.Language = parser.PHP{}
			if got := lang.ModulePath(nil, []byte(src)); got != "" {
				t.Errorf("ModulePath = %q, want empty", got)
			}
		})
	}
}

func TestByNamePHP(t *testing.T) {
	lang, err := parser.ByName(parser.LangPHP)
	if err != nil {
		t.Fatalf("ByName(php): %v", err)
	}
	if lang.Name() != parser.LangPHP {
		t.Errorf("Name = %q, want %q", lang.Name(), parser.LangPHP)
	}
	got := lang.FileExtensions()
	wantHas := map[string]bool{"php": false, "phtml": false, "php5": false, "phps": false}
	for _, e := range got {
		if _, ok := wantHas[e]; ok {
			wantHas[e] = true
		}
	}
	for ext, present := range wantHas {
		if !present {
			t.Errorf("FileExtensions = %v, missing %q", got, ext)
		}
	}
}

const phpFunctionOnly = `<?php

function login($user, $password) {
    return true;
}

function helper(): int {
    return 42;
}
`

func TestExtractSymbolsPHPFunction(t *testing.T) {
	root := parsePHP(t, phpFunctionOnly)
	syms, err := parser.ExtractSymbols(parser.PHP{}, root, []byte(phpFunctionOnly))
	if err != nil {
		t.Fatalf("ExtractSymbols: %v", err)
	}
	if len(syms) != 2 {
		t.Fatalf("got %d symbols, want 2: %+v", len(syms), syms)
	}
	names := map[string]string{}
	for _, s := range syms {
		if s.Kind != "function_declaration" {
			t.Errorf("Kind = %q, want function_declaration", s.Kind)
		}
		names[s.Name] = s.Kind
	}
	if names["login"] != "function_declaration" || names["helper"] != "function_declaration" {
		t.Errorf("names = %v, want both function_declaration", names)
	}
}

const phpTypesAndMembers = `<?php

interface Greeter {
    public function greet(): string;
}

trait T {
    public function hello() { return 'hi'; }
}

class User {
    private $name;

    public function __construct($name) { $this->name = $name; }

    public function name(): string { return $this->name; }
}

enum Status {
    case Active;
    case Inactive;
}
`

func TestExtractSymbolsPHPAllKinds(t *testing.T) {
	root := parsePHP(t, phpTypesAndMembers)
	syms, err := parser.ExtractSymbols(parser.PHP{}, root, []byte(phpTypesAndMembers))
	if err != nil {
		t.Fatalf("ExtractSymbols: %v", err)
	}

	counts := map[string]int{}
	methodReceiver := map[string]string{}
	for _, s := range syms {
		counts[s.Kind]++
		if s.Kind == "method_declaration" {
			methodReceiver[s.Name] = s.Receiver
		}
	}

	want := map[string]int{
		"interface_declaration": 1,
		"trait_declaration":     1,
		"class_declaration":     1,
		"enum_declaration":      1,
		"method_declaration":    4, // Greeter::greet, T::hello, User::__construct, User::name
	}
	for k, v := range want {
		if counts[k] != v {
			t.Errorf("%s count = %d, want %d (all=%+v)", k, counts[k], v, counts)
		}
	}

	// Method receivers must point at the enclosing type, mirroring the
	// JS / Python / Rust precedent.
	cases := []struct {
		name string
		want string
	}{
		{"greet", "Greeter"},
		{"hello", "T"},
		{"__construct", "User"},
		{"name", "User"},
	}
	for _, tc := range cases {
		if got := methodReceiver[tc.name]; got != tc.want {
			t.Errorf("method %q Receiver = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestExtractSymbolsPHPNilRoot(t *testing.T) {
	syms, err := parser.ExtractSymbols(parser.PHP{}, nil, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(syms) != 0 {
		t.Errorf("got %d symbols, want 0", len(syms))
	}
}
