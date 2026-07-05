package id_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/kellenff/yactt/internal/id"
	"github.com/kellenff/yactt/internal/parser"
)

func TestParseOK(t *testing.T) {
	cases := []struct {
		in   string
		kind id.Kind
		body string
	}{
		{"repo:/abs/path", id.KindRepo, "/abs/path"},
		{"pkg:net/http", id.KindPackage, "net/http"},
		{"file:auth/login.go", id.KindFile, "auth/login.go"},
		{"fn:auth.Login", id.KindFunction, "auth.Login"},
		{"fn:auth.(*Server).Login", id.KindFunction, "auth.(*Server).Login"},
		{"meth:auth.(*Server).Login", id.KindMethod, "auth.(*Server).Login"},
		{"class:auth.Session", id.KindClass, "auth.Session"},
		{"module:auth.helpers", id.KindModule, "auth.helpers"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := id.Parse(tc.in)
			if err != nil {
				t.Fatalf("Parse(%q) error: %v", tc.in, err)
			}
			if got.Kind != tc.kind {
				t.Errorf("Kind = %q, want %q", got.Kind, tc.kind)
			}
			if got.Body != tc.body {
				t.Errorf("Body = %q, want %q", got.Body, tc.body)
			}
			if got.String() != tc.in {
				t.Errorf("String round-trip = %q, want %q", got.String(), tc.in)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want error
	}{
		{"empty", "", id.ErrEmpty},
		{"no colon", "nocolon", id.ErrBadFormat},
		{"empty head", ":body", id.ErrBadFormat},
		{"empty body", "repo:", id.ErrBadFormat},
		{"unknown kind", "wat:foo", id.ErrUnknownKind},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := id.Parse(tc.in)
			if err == nil {
				t.Fatalf("Parse(%q) error = nil, want %v", tc.in, tc.want)
			}
			if !errors.Is(err, tc.want) {
				t.Errorf("Parse(%q) error = %v, want errors.Is(%v)", tc.in, err, tc.want)
			}
		})
	}
}

func TestParseErrorMessages(t *testing.T) {
	// Errors should mention the offending kind (or a fragment of the input)
	// for debuggability — that's the contract callers will rely on.
	cases := []struct {
		in  string
		has string
	}{
		{"wat:foo", "wat"},
		{"nocolon", "nocolon"},
		{":body", "body"},
		{"repo:", "repo"},
	}
	for _, tc := range cases {
		_, err := id.Parse(tc.in)
		if err == nil {
			continue
		}
		if !strings.Contains(err.Error(), tc.has) {
			t.Errorf("error %q should mention %q", err.Error(), tc.has)
		}
	}
}

func TestConstructorsRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		id   id.ID
	}{
		{"repo", id.Repo("/abs/path")},
		{"package", id.Package("net/http")},
		{"file", id.File("auth/login.go")},
		{"function no receiver", id.Function("auth", "", "Login")},
		{"function with receiver", id.Function("auth", "Server", "Login")},
		{"method", id.Method("auth", "Server", "Login")},
		{"class", id.Class("auth", "Session")},
		{"module", id.Module("auth", "helpers")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.id.String()
			got, err := id.Parse(s)
			if err != nil {
				t.Fatalf("Parse(%q) error: %v", s, err)
			}
			if !reflect.DeepEqual(got, tc.id) {
				t.Errorf("round-trip differs:\n got = %+v\nwant = %+v", got, tc.id)
			}
		})
	}
}

func TestFileConstructorCleansPath(t *testing.T) {
	got := id.File("auth/./login.go")
	want := id.File("auth/login.go")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("File cleanup: got %q, want %q", got, want)
	}
}

func TestFunctionParts(t *testing.T) {
	cases := []struct {
		name        string
		in          string
		wantPkg     string
		wantRecv    string
		wantName    string
		wantErrPart error
	}{
		{"three segment", "fn:auth.(*Server).Login", "auth", "(*Server)", "Login", nil},
		{"two segment", "fn:auth.Login", "auth", "", "Login", nil},
		{"bad kind", "class:auth.Login", "", "", "", id.ErrBadFormat},
		{"one segment", "fn:Login", "", "", "", id.ErrBadFormat}, // no dot -> missing receiver/name
		{"empty body", "fn:", "", "", "", id.ErrBadFormat},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			i, err := id.Parse(tc.in)
			if err != nil {
				// Some invalid inputs fail at Parse — that's OK if Parts also rejects.
				if tc.wantErrPart != nil {
					return
				}
				t.Fatalf("Parse(%q) error: %v", tc.in, err)
			}
			pkg, recv, name, err := i.FunctionParts()
			if tc.wantErrPart != nil {
				if !errors.Is(err, tc.wantErrPart) {
					t.Fatalf("err = %v, want errors.Is(%v)", err, tc.wantErrPart)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if pkg != tc.wantPkg || recv != tc.wantRecv || name != tc.wantName {
				t.Errorf("parts = (%q, %q, %q), want (%q, %q, %q)",
					pkg, recv, name, tc.wantPkg, tc.wantRecv, tc.wantName)
			}
		})
	}
}

func TestMethodParts(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantPkg  string
		wantCls  string
		wantName string
		wantErr  bool
	}{
		{"valid", "meth:auth.Server.Login", "auth", "Server", "Login", false},
		{"bad kind", "fn:auth.Server.Login", "", "", "", true},
		// Two-segment bodies are valid for TS/JS class methods with no
		// enclosing module path. pkg is empty.
		{"two segments no pkg", "meth:Server.refresh", "", "Server", "refresh", false},
		{"one segment", "meth:auth", "", "", "", true},
		{"empty parts", "meth:auth..Login", "", "", "", true},
		{"empty body", "meth:", "", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			i, err := id.Parse(tc.in)
			if err != nil {
				if tc.wantErr {
					return
				}
				t.Fatalf("Parse(%q) error: %v", tc.in, err)
			}
			pkg, cls, name, err := i.MethodParts()
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !errors.Is(err, id.ErrBadFormat) {
					t.Errorf("err = %v, want errors.Is(ErrBadFormat)", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if pkg != tc.wantPkg || cls != tc.wantCls || name != tc.wantName {
				t.Errorf("parts = (%q, %q, %q), want (%q, %q, %q)",
					pkg, cls, name, tc.wantPkg, tc.wantCls, tc.wantName)
			}
		})
	}
}

func TestClassParts(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantPkg string
		wantN   string
		wantErr bool
	}{
		{"valid", "class:auth.Session", "auth", "Session", false},
		{"bad kind", "fn:auth.Session", "", "", true},
		{"no dot", "class:auth", "", "", true},
		{"empty name", "class:auth.", "", "", true},
		{"empty pkg", "class:.Session", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			i, err := id.Parse(tc.in)
			if err != nil {
				if tc.wantErr {
					return
				}
				t.Fatalf("Parse(%q) error: %v", tc.in, err)
			}
			pkg, name, err := i.ClassParts()
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !errors.Is(err, id.ErrBadFormat) {
					t.Errorf("err = %v, want errors.Is(ErrBadFormat)", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if pkg != tc.wantPkg || name != tc.wantN {
				t.Errorf("parts = (%q, %q), want (%q, %q)", pkg, name, tc.wantPkg, tc.wantN)
			}
		})
	}
}

func TestMustParsePanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("MustParse should panic on invalid input")
		}
	}()
	_ = id.MustParse("not a valid id")
}

func TestMustParseOK(t *testing.T) {
	got := id.MustParse("fn:auth.Login")
	if got.Kind != id.KindFunction || got.Body != "auth.Login" {
		t.Errorf("MustParse = %+v, want {Function, auth.Login}", got)
	}
}

// TestFunction_EmptyReceiverOmitsMiddleSegment is the regression guard for
// the `receiver == ""` branch in Function(). A mutation that flips the
// condition would emit `pkg + "." + "" + "." + name` ("auth..Login") for
// the empty-receiver case. The round-trip via Parse is lenient so the
// round-trip in TestConstructorsRoundTrip might survive a subtle mutation;
// this test pins the exact rendered Body.
func TestFunction_EmptyReceiverOmitsMiddleSegment(t *testing.T) {
	got := id.Function("auth", "", "Login")
	want := id.ID{Kind: id.KindFunction, Body: "auth.Login"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Function(empty receiver) = %+v, want %+v", got, want)
	}
	// And the String form has exactly one dot, not two.
	if s := got.String(); s != "fn:auth.Login" {
		t.Errorf("String() = %q, want fn:auth.Login (no empty middle segment)", s)
	}
}

// TestFunction_NonEmptyReceiverIncludesMiddleSegment covers the
// non-empty-receiver branch — protects against a flipped condition
// dropping the receiver entirely.
func TestFunction_NonEmptyReceiverIncludesMiddleSegment(t *testing.T) {
	got := id.Function("auth", "Server", "Login")
	want := id.ID{Kind: id.KindFunction, Body: "auth.Server.Login"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Function(non-empty receiver) = %+v, want %+v", got, want)
	}
	if s := got.String(); s != "fn:auth.Server.Login" {
		t.Errorf("String() = %q, want fn:auth.Server.Login", s)
	}
}

// TestFunctionParts_BodyWithLeadingDot covers the boundary case for
// `last := strings.LastIndexByte(...)`. When the body has a leading dot
// (e.g. ".Login"), `last == 0` — strictly greater than -1, so the live
// code treats it as a valid name. A mutation flipping `<` to `<=` would
// reject this input as malformed. This test pins the live behaviour.
func TestFunctionParts_BodyWithLeadingDot(t *testing.T) {
	i := id.ID{Kind: id.KindFunction, Body: ".Login"}
	pkg, recv, name, err := i.FunctionParts()
	if err != nil {
		t.Fatalf("FunctionParts(.Login) error: %v", err)
	}
	if pkg != "" || recv != "" || name != "Login" {
		t.Errorf("parts = (%q, %q, %q), want (\"\", \"\", \"Login\")", pkg, recv, name)
	}
}

// TestFunctionParts_HeadWithLeadingDot covers the boundary for
// `mid := strings.LastIndexByte(head, '.')`. When the head has a leading
// dot (e.g. body ".X.Y" → head=".X", mid==0), the live code keeps going
// because `mid < 0` is false at 0. A mutation to `<=` would reject this
// as malformed. Pin the live behaviour.
func TestFunctionParts_HeadWithLeadingDot(t *testing.T) {
	i := id.ID{Kind: id.KindFunction, Body: ".X.Y"}
	pkg, recv, name, err := i.FunctionParts()
	if err != nil {
		t.Fatalf("FunctionParts(.X.Y) error: %v", err)
	}
	if pkg != "" || recv != "X" || name != "Y" {
		t.Errorf("parts = (%q, %q, %q), want (\"\", \"X\", \"Y\")", pkg, recv, name)
	}
}

// TestFor_KindRouting pins the kind→prefix mapping. The single-source
// helper used by every emission site in the tool and search layers.
func TestFor_KindRouting(t *testing.T) {
	cases := []struct {
		name string
		sym  parser.Symbol
		pkg  string
		want string
	}{
		{"function", parser.Symbol{Kind: "function_declaration", Name: "Login"}, "auth", "fn:auth.Login"},
		{"generator function", parser.Symbol{Kind: "generator_function_declaration", Name: "Gen"}, "auth", "fn:auth.Gen"},
		{"method with receiver", parser.Symbol{Kind: "method_declaration", Name: "refresh", Receiver: "User"}, "auth", "meth:auth.User.refresh"},
		{"method without receiver", parser.Symbol{Kind: "method_declaration", Name: "M"}, "auth", "fn:auth.M"},
		{"type declaration", parser.Symbol{Kind: "type_declaration", Name: "Session"}, "auth", "class:auth.Session"},
		{"class declaration (TS/JS)", parser.Symbol{Kind: "class_declaration", Name: "User"}, "auth", "class:auth.User"},
		{"interface (TS)", parser.Symbol{Kind: "interface_declaration", Name: "Shape"}, "auth", "class:auth.Shape"},
		{"type alias (TS)", parser.Symbol{Kind: "type_alias_declaration", Name: "Maybe"}, "auth", "class:auth.Maybe"},
		{"enum (TS)", parser.Symbol{Kind: "enum_declaration", Name: "Color"}, "auth", "class:auth.Color"},
		{"module", parser.Symbol{Kind: "module_declaration", Name: "pkg"}, "auth", "module:auth.pkg"},
		{"unknown falls through to fn", parser.Symbol{Kind: "wat", Name: "X"}, "auth", "fn:auth.X"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := id.For(tc.sym, tc.pkg)
			if got != tc.want {
				t.Errorf("For(%+v, %q) = %q, want %q", tc.sym, tc.pkg, got, tc.want)
			}
		})
	}
}

// TestFor_ToleratesEmptyPackage pins the leading-dot guard: TS/JS files
// at the repo root have pkg=""; the body must drop the empty segment
// rather than emit `meth:.Server.refresh`.
func TestFor_ToleratesEmptyPackage(t *testing.T) {
	sym := parser.Symbol{Kind: "method_declaration", Name: "refresh", Receiver: "User"}
	got := id.For(sym, "")
	want := "meth:User.refresh"
	if got != want {
		t.Errorf("For(empty pkg) = %q, want %q", got, want)
	}
	if _, err := id.Parse(got); err != nil {
		t.Errorf("Parse(%q) error: %v", got, err)
	}
	if _, _, _, err := id.MustParse(got).MethodParts(); err != nil {
		t.Errorf("MethodParts(%q) error: %v", got, err)
	}
}

// TestJoinDotted covers the truth table for the dotted concatenation
// helper. Drops empty parts so callers never produce leading/trailing dots.
func TestJoinDotted(t *testing.T) {
	cases := []struct {
		name string
		pkg  string
		rest []string
		want string
	}{
		{"all empty", "", nil, ""},
		{"only pkg", "auth", nil, "auth"},
		{"only name", "", []string{"Foo"}, "Foo"},
		{"pkg + name", "auth", []string{"Login"}, "auth.Login"},
		{"pkg + class + name", "auth", []string{"Server", "Login"}, "auth.Server.Login"},
		{"drops empty pkg", "", []string{"Server", "refresh"}, "Server.refresh"},
		{"drops empty middle", "auth", []string{"", "Login"}, "auth.Login"},
		{"drops empty tail", "auth", []string{"Login", ""}, "auth.Login"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := id.JoinDotted(tc.pkg, tc.rest...)
			if got != tc.want {
				t.Errorf("JoinDotted(%q, %v) = %q, want %q", tc.pkg, tc.rest, got, tc.want)
			}
		})
	}
}
