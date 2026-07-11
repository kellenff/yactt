package entity

import (
	"strings"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/id"
	"github.com/kellenff/yactt/internal/parser"
	"github.com/kellenff/yactt/internal/summarizer"
)

// Entity is the layer-agnostic view of a code entity. Per issue #25,
// it bundles the three representations of "kind" — parser (tree-sitter
// grammar), domain (canonical uppercase), and ID (URL-safe prefix) —
// so callers don't have to know which layer they're in to write a
// working kind condition.
//
// Receiver is itself an *Entity because a method's receiver is a
// class with its own canonical/grammar/id triple. The pointer is
// nullable: it's nil if (a) this isn't a method, (b) the receiver
// type isn't visible in the indexed set, or (c) the resolver pass
// hasn't run. ReceiverName holds the raw receiver type name so the
// canonical ID is correct even before resolution.
//
// Fields are unexported; the layer-specific accessors (DomainKind,
// GrammarKind, IDKind, ID, Summary, Receiver, ReceiverName) are the
// only way to read them. This keeps the canonical-mapping logic in
// one place instead of letting callers reach past it.
type Entity struct {
	canonical    domain.NodeKind
	grammar      string
	idKind       id.Kind
	pkg          string
	name         string
	receiverName string // receiver type name as it appears in source (methods only)
	receiver     *Entity // resolved receiver entity; nil until WithReceiver runs
	file         string
	startRow     int
	endRow       int
}

// FromParser lifts a tree-sitter declaration into a holistic Entity.
// The same parser.Symbol + (file, pkg) triple always produces the
// same (canonical, grammar, idKind) triple; the (file, pkg) inputs
// are what makes ID() repo-specific.
//
// Receiver is left nil — the parser gives us the receiver's name, not
// its full entity. Use ResolveReceivers (or WithReceiver for ad-hoc
// resolution) to fill it in.
func FromParser(s parser.Symbol, file, pkg string) Entity {
	return Entity{
		canonical:    canonicalFromGrammar(s.Kind),
		grammar:      s.Kind,
		idKind:       idKindFromGrammar(s.Kind, s.Receiver),
		pkg:          pkg,
		name:         s.Name,
		receiverName: s.Receiver,
		file:         file,
		startRow:     s.StartRow,
		endRow:       s.EndRow,
	}
}

// FromID reconstructs an Entity from a parsed canonical ID. The
// grammar kind is recovered via canonical → primary grammar; the
// receiver pointer is left nil (the ID body encodes the receiver's
// name, not its full entity).
//
// Body splitting is done inline rather than via id.FunctionParts /
// id.MethodParts / id.ClassParts: those helpers have stricter
// contracts (e.g. ClassParts rejects non-class IDs, FunctionParts
// requires a dot) than FromID needs. The body format itself is
// simple — split on '.' with the right depth — so inlining keeps the
// package self-contained.
func FromID(i id.ID) Entity {
	e := Entity{
		canonical: canonicalFromID(i.Kind),
		idKind:    i.Kind,
	}
	pkg, recv, name := splitIDBody(i.Body, i.Kind)
	e.pkg = pkg
	e.receiverName = recv
	e.name = name
	e.grammar = primaryGrammarFor(e.canonical)
	return e
}

// splitIDBody parses the body of a canonical ID into (pkg, receiver,
// name). receiver is non-empty only for the meth: kind. Returns ""
// for any missing component (no-package variants round-trip as
// pkg="", receiver/class="", name="<the whole body>").
//
// Body grammar:
//   - fn:<pkg?>.<name>          — pkg optional
//   - meth:<pkg?>.<class>.<name> — pkg optional
//   - class:<pkg>.<name>        — pkg required
//   - module:<pkg>.<name>       — pkg required
func splitIDBody(body string, kind id.Kind) (pkg, recv, name string) {
	switch kind {
	case id.KindMethod:
		parts := strings.SplitN(body, ".", 3)
		switch len(parts) {
		case 3:
			return parts[0], parts[1], parts[2]
		case 2:
			return "", parts[0], parts[1]
		}
		return "", "", body
	case id.KindFunction:
		// fn: bodies are pkg + name; the rare fn:<pkg>.<receiver>.<name>
		// shape (free function with a typed receiver) is reconstructed
		// as pkg="<pkg>", name="<receiver>.<name>" — close enough for
		// round-trip display; ID() rebuilds the same string.
		parts := strings.SplitN(body, ".", 2)
		switch len(parts) {
		case 2:
			return parts[0], "", parts[1]
		case 1:
			return "", "", parts[0]
		}
		return "", "", body
	default: // KindClass, KindModule, anything structural
		parts := strings.SplitN(body, ".", 2)
		switch len(parts) {
		case 2:
			return parts[0], "", parts[1]
		case 1:
			return "", "", parts[0]
		}
		return "", "", body
	}
}

// DomainKind returns the canonical uppercase kind (e.g. "FUNCTION").
func (e Entity) DomainKind() domain.NodeKind { return e.canonical }

// GrammarKind returns the parser-layer grammar spelling (e.g.
// "function_declaration"). For structural kinds (REPO/PACKAGE/FILE)
// the grammar spelling isn't applicable; this returns "".
func (e Entity) GrammarKind() string { return e.grammar }

// IDKind returns the URL-safe prefix (e.g. "fn").
func (e Entity) IDKind() id.Kind { return e.idKind }

// IsCode reports whether this entity is a code declaration (vs. a
// structural kind like REPO/PACKAGE/FILE). Delegates to the domain
// kind so the rule lives in one place.
func (e Entity) IsCode() bool { return e.canonical.IsCode() }

// ID returns the canonical ID string (e.g. "fn:auth.Login"). Uses
// receiverName (not receiver), so the ID is correct even before the
// resolver runs — matching id.For's behavior precisely.
func (e Entity) ID() string {
	name := e.name
	if e.idKind == id.KindMethod {
		name = e.receiverName + "." + e.name
	}
	return id.ID{Kind: e.idKind, Body: id.JoinDotted(e.pkg, name)}.String()
}

// Summary returns the "Kind: Name" style summary without a doc
// comment. Replaces parser.SymbolSummary.
func (e Entity) Summary() string {
	return summarizer.Summarize(displayLabel(e.grammar), "", domain.SanitizeName(e.name))
}

// Receiver returns the resolved receiver entity, or nil if the
// receiver hasn't been resolved (or wasn't resolvable — external
// types, unindexed receivers, etc.).
func (e Entity) Receiver() *Entity { return e.receiver }

// ReceiverName returns the raw receiver type name as it appears in
// source. Always populated for methods (from parser.Symbol.Receiver);
// empty otherwise.
func (e Entity) ReceiverName() string { return e.receiverName }

// WithReceiver sets the resolved receiver entity. Idempotent (calling
// twice replaces). Returns a pointer for chaining; mutates in place.
// Callers can also leave the receiver nil and rely on ReceiverName
// for the canonical ID and display.
func (e *Entity) WithReceiver(r *Entity) *Entity {
	e.receiver = r
	return e
}

// ReceiverView returns the bounded wire form of the receiver entity
// (id + kind + name + summary), or nil if the receiver is unresolved.
// Tool-layer response structs embed this so the wire shape is
// explicit without leaking the full Entity (no layers, no source,
// no recursion into the receiver's receiver).
func (e Entity) ReceiverView() *ReceiverView {
	if e.receiver == nil {
		return nil
	}
	return &ReceiverView{
		ID:      e.receiver.ID(),
		Kind:    e.receiver.DomainKind(),
		Name:    e.receiver.name,
		Summary: e.receiver.Summary(),
	}
}

// String renders a debug-friendly form showing all three kind
// representations side by side. Used by tests and ad-hoc logs.
func (e Entity) String() string {
	recv := "<nil>"
	if e.receiver != nil {
		recv = e.receiver.ID()
	}
	return "Entity{id=" + e.ID() +
		", domain=" + string(e.canonical) +
		", grammar=" + e.grammar +
		", idKind=" + string(e.idKind) +
		", receiverName=" + e.receiverName +
		", receiver=" + recv +
		"}"
}

// displayLabel mirrors parser.kindLabel: grammar kind → display label.
// Kept here (rather than imported from parser) because the entity
// package is the new home of layer-marshalling logic and the label
// table is part of that. Same strings, single source of truth at the
// grammar→label boundary.
//
// ponytail: a 5-case switch in a 100-line package reads as intent.
// Don't lift it to a map until it grows.
func displayLabel(grammar string) string {
	switch grammar {
	case "function_declaration":
		return "Function"
	case "method_declaration":
		return "Method"
	case "type_declaration", "class_declaration",
		"interface_declaration", "type_alias_declaration",
		"enum_declaration", "struct_declaration", "trait_declaration":
		return "Class"
	}
	return "Symbol"
}

// primaryGrammarFor returns the "primary" grammar form for a
// canonical kind — the first entry in the canonicalKinds table for
// that kind. Used by FromID where we have a canonical kind but no
// grammar string. Tests assert this is the same string a FromParser
// caller would have started from for any grammar form under that
// canonical kind.
func primaryGrammarFor(k domain.NodeKind) string {
	for _, e := range canonicalKinds {
		if e.Canonical == k && len(e.Mapping.Grammars) > 0 {
			return e.Mapping.Grammars[0]
		}
	}
	return ""
}

// QualifiedName returns the qualified name for an entity as it would
// appear in source (e.g., "Auth.Login" for a method). For functions,
// modules, and classes this is just the entity name.
func QualifiedName(e Entity) string {
	if e.idKind == id.KindMethod {
		return e.receiverName + "." + e.name
	}
	return e.name
}

// StartRow returns the 0-based starting row (line) for this entity's declaration.
func (e Entity) StartRow() int { return e.startRow }

// EndRow returns the exclusive end row for this entity's declaration.
func (e Entity) EndRow() int { return e.endRow }
