package id_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/kellenff/yactt/internal/id"
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
		{"two segments", "meth:auth.Server", "", "", "", true},
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
