// Package entity is the layer-agnostic view of a code entity. It
// bundles the three representations of "kind" — parser (tree-sitter
// grammar), domain (canonical uppercase), and ID (URL-safe prefix) —
// so callers don't translate between them at every layer boundary. Per
// issue #25.
package entity

import (
	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/id"
)

// KindMapping is the canonical three-way mapping for one canonical kind:
// every grammar form that folds into it, plus the URL-safe id prefix.
//
// Surfaces in get_graph_schema's `kindMap` field so a model can
// discover the layer translations from the schema tool itself rather
// than guessing which spelling each layer uses.
type KindMapping struct {
	Grammars []string `json:"grammar"` // grammar forms that fold into the canonical kind
	ID       string   `json:"id"`      // URL-safe prefix used in canonical IDs
}

// canonicalKinds is the single source of truth for grammar ↔ canonical ↔ id.
//
// The slice order matches the rest of the codebase (REPO, PACKAGE, FILE,
// FUNCTION, METHOD, CLASS, MODULE) so get_graph_schema's kindMap and the
// domain constants list the kinds in the same order. Adding a new
// NodeKind means adding an entry here AND in internal/domain/types.go —
// no codegen, two manual edits.
//
// ponytail: an explicit slice beats an `AllNodeKinds` mirror in the
// domain package. The cost is one line per canonical kind; the win is
// no import cycle (entity imports domain, never the other way).
var canonicalKinds = []struct {
	Canonical domain.NodeKind
	Mapping   KindMapping
}{
	{domain.KindRepo, KindMapping{Grammars: nil, ID: "repo"}},
	{domain.KindPackage, KindMapping{Grammars: nil, ID: "pkg"}},
	{domain.KindFile, KindMapping{Grammars: nil, ID: "file"}},
	{domain.KindFunction, KindMapping{Grammars: []string{"function_declaration"}, ID: "fn"}},
	{domain.KindMethod, KindMapping{Grammars: []string{"method_declaration"}, ID: "meth"}},
	{domain.KindClass, KindMapping{Grammars: []string{
		"type_declaration",
		"class_declaration",
		"interface_declaration",
		"type_alias_declaration",
		"enum_declaration",
		"struct_declaration",
		"trait_declaration",
	}, ID: "class"}},
	{domain.KindModule, KindMapping{Grammars: []string{"module", "module_declaration"}, ID: "module"}},
}

// AllMappings returns the canonical mapping table for callers that
// need to enumerate (e.g. get_graph_schema's kindMap field).
func AllMappings() []KindMapping {
	out := make([]KindMapping, len(canonicalKinds))
	for i, e := range canonicalKinds {
		out[i] = e.Mapping
	}
	return out
}

// AllMappingsByKind is the map-keyed form of canonicalKinds. Kept
// separate so AllMappings() stays a stable slice (callers may rely on
// the order) while map lookups stay O(1).
func AllMappingsByKind() map[domain.NodeKind]KindMapping {
	m := make(map[domain.NodeKind]KindMapping, len(canonicalKinds))
	for _, e := range canonicalKinds {
		m[e.Canonical] = e.Mapping
	}
	return m
}

// canonicalFromGrammar maps a tree-sitter grammar kind string to the
// canonical NodeKind. Replaces parser.SymbolKind.
//
// ponytail: unknown grammars default to KindFunction (matching id.For's
// prior behavior), not KindModule. Re-add a Module fallback here only
// if a real grammar kind starts slipping through unhandled.
func canonicalFromGrammar(grammar string) domain.NodeKind {
	for _, e := range canonicalKinds {
		for _, g := range e.Mapping.Grammars {
			if g == grammar {
				return e.Canonical
			}
		}
	}
	return domain.KindFunction
}

// canonicalFromID maps an id.Kind to the canonical NodeKind. Used by
// FromID to recover the canonical kind from a parsed canonical ID.
func canonicalFromID(k id.Kind) domain.NodeKind {
	for _, e := range canonicalKinds {
		if e.Mapping.ID == string(k) {
			return e.Canonical
		}
	}
	return domain.KindFunction
}

// idKindFromGrammar maps a tree-sitter grammar kind to the URL-safe id
// prefix. Replaces the `switch s.Kind` block in id.For.
//
// receiver is used for the empty-Receiver-on-method safety net: when
// a method_declaration symbol slips through with no Receiver (the
// parser layer always populates it, so this branch is defensive), fall
// back to KindFunction so the ID is at least round-trippable.
func idKindFromGrammar(grammar, receiver string) id.Kind {
	for _, e := range canonicalKinds {
		for _, g := range e.Mapping.Grammars {
			if g == grammar {
				kind := id.Kind(e.Mapping.ID)
				if kind == id.KindMethod && receiver == "" {
					return id.KindFunction
				}
				return kind
			}
		}
	}
	return id.KindFunction
}

// idKindFromCanonical maps a canonical NodeKind to the URL-safe id
// prefix directly. Used by FromID.
func idKindFromCanonical(k domain.NodeKind) id.Kind {
	for _, e := range canonicalKinds {
		if e.Canonical == k {
			return id.Kind(e.Mapping.ID)
		}
	}
	return id.KindFunction
}
