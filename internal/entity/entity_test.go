package entity

import (
	"strings"
	"testing"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/id"
	"github.com/kellenff/yactt/internal/parser"
)

// TestKindMapping_TableIntegrity guards against accidental edits that
// drop a canonical kind or rearrange the order silently.
func TestKindMapping_TableIntegrity(t *testing.T) {
	wantCanonical := []domain.NodeKind{
		domain.KindRepo, domain.KindPackage, domain.KindFile,
		domain.KindFunction, domain.KindMethod, domain.KindClass, domain.KindModule,
	}
	got := canonicalKinds
	if len(got) != len(wantCanonical) {
		t.Fatalf("canonicalKinds len = %d, want %d", len(got), len(wantCanonical))
	}
	for i, w := range wantCanonical {
		if got[i].Canonical != w {
			t.Errorf("canonicalKinds[%d].Canonical = %q, want %q", i, got[i].Canonical, w)
		}
	}
}

// TestAllMappingsByKind_RoundTrip every entry of the table recovers
// its canonical kind via canonicalFromID and idKindFromCanonical.
// Catches "added a canonical but forgot to wire it into the lookups".
func TestAllMappingsByKind_RoundTrip(t *testing.T) {
	m := AllMappingsByKind()
	for canonical, mp := range m {
		if mp.ID == "" {
			t.Errorf("%q has empty ID prefix", canonical)
		}
		if got := canonicalFromID(id.Kind(mp.ID)); got != canonical {
			t.Errorf("canonicalFromID(%q) = %q, want %q", mp.ID, got, canonical)
		}
		if got := idKindFromCanonical(canonical); string(got) != mp.ID {
			t.Errorf("idKindFromCanonical(%q) = %q, want %q", canonical, got, mp.ID)
		}
	}
}

// TestCanonicalFromGrammar covers every grammar form in the table
// plus unknown and empty inputs. The unknown case is the ponytail
// decision: default to KindFunction (matching id.For), not KindModule.
func TestCanonicalFromGrammar(t *testing.T) {
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
		{"struct_declaration", domain.KindClass},
		{"trait_declaration", domain.KindClass},
		{"module", domain.KindModule},
		{"module_declaration", domain.KindModule},
		{"something_else", domain.KindFunction}, // ponytail: default is KindFunction
		{"", domain.KindFunction},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := canonicalFromGrammar(tc.in); got != tc.want {
				t.Errorf("canonicalFromGrammar(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestIDKindFromGrammar covers the same set plus the empty-Receiver-
// on-method safety net: a method_declaration with no Receiver falls
// back to KindFunction so the ID stays round-trippable.
func TestIDKindFromGrammar(t *testing.T) {
	cases := []struct {
		grammar  string
		receiver string
		want     id.Kind
	}{
		{"function_declaration", "", id.KindFunction},
		{"method_declaration", "Server", id.KindMethod},
		{"method_declaration", "", id.KindFunction}, // empty-Receiver safety net
		{"type_declaration", "", id.KindClass},
		{"unknown_grammar", "R", id.KindFunction},
	}
	for _, tc := range cases {
		t.Run(tc.grammar+"/"+tc.receiver, func(t *testing.T) {
			if got := idKindFromGrammar(tc.grammar, tc.receiver); got != tc.want {
				t.Errorf("idKindFromGrammar(%q, %q) = %q, want %q", tc.grammar, tc.receiver, got, tc.want)
			}
		})
	}
}

// TestFromParser_AllGrammarsFromTable is the canonical mapping pin:
// for every (canonical, grammar) pair in canonicalKinds, FromParser
// produces an Entity whose DomainKind/IDKind match the table.
//
// ponytail: this is the test that protects the "issue #25 fix" — if
// the canonical→grammar map drifts, this fires before any tool layer
// test does.
func TestFromParser_AllGrammarsFromTable(t *testing.T) {
	for _, e := range canonicalKinds {
		for _, g := range e.Mapping.Grammars {
			t.Run(string(e.Canonical)+"/"+g, func(t *testing.T) {
				// method_declaration needs a Receiver populated —
				// the parser always populates it, and idKindFromGrammar
				// falls back to fn: when it's empty.
				sym := parser.Symbol{Kind: g, Name: "X", Receiver: "R"}
				ent := FromParser(sym, "pkg/file.go", "pkg")
				if ent.DomainKind() != e.Canonical {
					t.Errorf("DomainKind = %q, want %q", ent.DomainKind(), e.Canonical)
				}
				if string(ent.IDKind()) != e.Mapping.ID {
					t.Errorf("IDKind = %q, want %q", ent.IDKind(), e.Mapping.ID)
				}
				if ent.GrammarKind() != g {
					t.Errorf("GrammarKind = %q, want %q", ent.GrammarKind(), g)
				}
			})
		}
	}
}

// TestFromParser_IDComposition covers the ID() formula for every kind.
// For methods the ID body must include the receiver name; for non-
// methods the body must include only (pkg, name).
func TestFromParser_IDComposition(t *testing.T) {
	cases := []struct {
		name string
		sym  parser.Symbol
		pkg  string
		want string
	}{
		{"function", parser.Symbol{Kind: "function_declaration", Name: "Login"}, "auth", "fn:auth.Login"},
		{"method", parser.Symbol{Kind: "method_declaration", Name: "Handle", Receiver: "Server"}, "auth", "meth:auth.Server.Handle"},
		{"class", parser.Symbol{Kind: "type_declaration", Name: "Server"}, "auth", "class:auth.Server"},
		{"empty-pkg function", parser.Symbol{Kind: "function_declaration", Name: "Foo"}, "", "fn:Foo"},
		{"unknown grammar", parser.Symbol{Kind: "weird_thing", Name: "X"}, "pkg", "fn:pkg.X"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ent := FromParser(tc.sym, "file.go", tc.pkg)
			if got := ent.ID(); got != tc.want {
				t.Errorf("ID() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestFromParser_Summary covers the "Kind: Name" wire form via the
// summarizer pipeline (no doc comment, just name).
func TestFromParser_Summary(t *testing.T) {
	cases := []struct {
		sym  parser.Symbol
		want string
	}{
		{parser.Symbol{Kind: "function_declaration", Name: "Login"}, "Function: Login"},
		{parser.Symbol{Kind: "method_declaration", Name: "Handle", Receiver: "Server"}, "Method: Handle"},
		{parser.Symbol{Kind: "type_declaration", Name: "Server"}, "Class: Server"},
		{parser.Symbol{Kind: "weird_thing", Name: "X"}, "Symbol: X"},
		{parser.Symbol{Kind: "function_declaration", Name: ""}, "Function:"},
	}
	for _, tc := range cases {
		t.Run(tc.sym.Kind+"/"+tc.sym.Name, func(t *testing.T) {
			ent := FromParser(tc.sym, "f.go", "pkg")
			if got := ent.Summary(); got != tc.want {
				t.Errorf("Summary() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestFromParser_ReceiverNamePreserved pins that the raw receiver
// name survives FromParser even before WithReceiver runs. Without
// this, ID() would return a different shape after the resolver runs.
func TestFromParser_ReceiverNamePreserved(t *testing.T) {
	s := parser.Symbol{Kind: "method_declaration", Name: "Handle", Receiver: "Server"}
	e := FromParser(s, "f.go", "pkg")
	if e.ReceiverName() != "Server" {
		t.Errorf("ReceiverName() = %q, want %q", e.ReceiverName(), "Server")
	}
	if e.Receiver() != nil {
		t.Errorf("Receiver() = non-nil, want nil before resolution")
	}
}

// TestFromID_RoundTrip is the per-kind round-trip pin: for every
// (canonical, grammar) pair, take a synthesized Entity → ID() →
// Parse → FromID and confirm DomainKind matches the canonical kind.
func TestFromID_RoundTrip(t *testing.T) {
	for _, e := range canonicalKinds {
		for _, g := range e.Mapping.Grammars {
			t.Run(string(e.Canonical)+"/"+g, func(t *testing.T) {
				s := parser.Symbol{Kind: g, Name: "X", Receiver: "R"}
				orig := FromParser(s, "pkg/file.go", "pkg")

				parsed, err := id.Parse(orig.ID())
				if err != nil {
					t.Fatalf("Parse(%q) = %v", orig.ID(), err)
				}
				recovered := FromID(parsed)

				if recovered.DomainKind() != e.Canonical {
					t.Errorf("DomainKind round-trip = %q, want %q", recovered.DomainKind(), e.Canonical)
				}
				if recovered.ID() != orig.ID() {
					t.Errorf("ID round-trip = %q, want %q", recovered.ID(), orig.ID())
				}
			})
		}
	}
}

// TestFromID_ReconstructsPackageAndName covers the body-splitting
// for fn:, meth:, and class: ID bodies.
func TestFromID_ReconstructsPackageAndName(t *testing.T) {
	cases := []struct {
		in        string
		wantPkg   string
		wantName  string
		wantRecv  string
	}{
		{"fn:auth.Login", "auth", "Login", ""},
		{"fn:Foo", "", "Foo", ""},
		{"meth:auth.Server.Handle", "auth", "Handle", "Server"},
		{"meth:Server.Handle", "", "Handle", "Server"},
		{"class:auth.User", "auth", "User", ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			parsed, err := id.Parse(tc.in)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			e := FromID(parsed)
			if e.pkg != tc.wantPkg {
				t.Errorf("pkg = %q, want %q", e.pkg, tc.wantPkg)
			}
			if e.name != tc.wantName {
				t.Errorf("name = %q, want %q", e.name, tc.wantName)
			}
			if e.receiverName != tc.wantRecv {
				t.Errorf("receiverName = %q, want %q", e.receiverName, tc.wantRecv)
			}
		})
	}
}

// TestWithReceiver_Idempotent pins that calling WithReceiver twice
// keeps the second value (the tool layer's resolver does this when a
// partial match gets upgraded).
func TestWithReceiver_Idempotent(t *testing.T) {
	method := FromParser(parser.Symbol{Kind: "method_declaration", Name: "Handle", Receiver: "Server"}, "f.go", "pkg")
	recvA := FromParser(parser.Symbol{Kind: "type_declaration", Name: "Server"}, "server.go", "pkg")
	recvB := FromParser(parser.Symbol{Kind: "type_declaration", Name: "Server"}, "v2/server.go", "pkg")

	method.WithReceiver(&recvA)
	if method.Receiver() == nil || method.Receiver().name != "Server" {
		t.Fatalf("first WithReceiver didn't stick: %+v", method)
	}
	method.WithReceiver(&recvB)
	if method.Receiver().file != "v2/server.go" {
		t.Errorf("second WithReceiver didn't replace: file = %q", method.Receiver().file)
	}
}

// TestReceiverView_NilForUnresolved is the wire-shape contract: an
// unresolved receiver produces a nil ReceiverView, so the JSON omits
// the field entirely. Tool-layer consumers rely on this.
func TestReceiverView_NilForUnresolved(t *testing.T) {
	s := parser.Symbol{Kind: "method_declaration", Name: "Handle", Receiver: "Server"}
	e := FromParser(s, "f.go", "pkg")
	if rv := e.ReceiverView(); rv != nil {
		t.Errorf("ReceiverView() = %+v, want nil", rv)
	}
}

// TestReceiverView_BoundedWhenResolved covers the four-field wire
// view: id, kind, name, summary. No layers, no source, no recursion.
func TestReceiverView_BoundedWhenResolved(t *testing.T) {
	method := FromParser(parser.Symbol{Kind: "method_declaration", Name: "Handle", Receiver: "Server"}, "f.go", "pkg")
	recv := FromParser(parser.Symbol{Kind: "type_declaration", Name: "Server"}, "server.go", "pkg")
	method.WithReceiver(&recv)

	rv := method.ReceiverView()
	if rv == nil {
		t.Fatal("ReceiverView() = nil, want non-nil")
	}
	if rv.ID != "class:pkg.Server" {
		t.Errorf("ID = %q, want %q", rv.ID, "class:pkg.Server")
	}
	if rv.Kind != domain.KindClass {
		t.Errorf("Kind = %q, want %q", rv.Kind, domain.KindClass)
	}
	if rv.Name != "Server" {
		t.Errorf("Name = %q, want %q", rv.Name, "Server")
	}
	if rv.Summary != "Class: Server" {
		t.Errorf("Summary = %q, want %q", rv.Summary, "Class: Server")
	}
}

// TestResolveReceivers covers the resolver: it walks a slice, fills
// in receivers where lookup succeeds, and leaves nil elsewhere. A
// nil lookup is a no-op (defensive for callers without an index).
func TestResolveReceivers(t *testing.T) {
	m1 := FromParser(parser.Symbol{Kind: "method_declaration", Name: "M1", Receiver: "Server"}, "f.go", "pkg")
	m2 := FromParser(parser.Symbol{Kind: "method_declaration", Name: "M2", Receiver: "Unknown"}, "f.go", "pkg")
	f := FromParser(parser.Symbol{Kind: "function_declaration", Name: "Free"}, "f.go", "pkg")
	ents := []Entity{m1, m2, f}

	lookup := ReceiverLookup(func(name, pkg string) (Entity, bool) {
		if name == "Server" {
			return FromParser(parser.Symbol{Kind: "type_declaration", Name: "Server"}, "server.go", pkg), true
		}
		return Entity{}, false
	})

	ResolveReceivers(ents, lookup)

	if ents[0].Receiver() == nil {
		t.Error("m1 receiver should be resolved")
	}
	if ents[1].Receiver() != nil {
		t.Error("m2 receiver should be nil (lookup miss)")
	}
	if ents[2].Receiver() != nil {
		t.Error("function entity should never have a resolved receiver")
	}
}

// TestResolveReceivers_NilLookupIsNoOp defends against callers that
// haven't wired a lookup yet (e.g. tests of FromParser in isolation).
func TestResolveReceivers_NilLookupIsNoOp(t *testing.T) {
	m := FromParser(parser.Symbol{Kind: "method_declaration", Name: "M", Receiver: "S"}, "f.go", "pkg")
	ents := []Entity{m}
	ResolveReceivers(ents, nil)
	if ents[0].Receiver() != nil {
		t.Error("nil lookup should leave receiver nil")
	}
}

// TestString_DebugFormat is a small pin against accidentally changing
// the debug formatter's shape (some test logs depend on it).
func TestString_DebugFormat(t *testing.T) {
	m := FromParser(parser.Symbol{Kind: "method_declaration", Name: "Handle", Receiver: "Server"}, "f.go", "pkg")
	s := m.String()
	for _, want := range []string{"meth:pkg.Server.Handle", "METHOD", "method_declaration", "Server"} {
		if !strings.Contains(s, want) {
			t.Errorf("String() = %q, missing %q", s, want)
		}
	}
}