package repofixture

import "strings"

// ProjectURI returns the canonical wire reference for this fixture:
// "file://" + Root. The on-wire contract for the MCP project
// reference requires every targeting tool call to include a
// `project` field shaped exactly this way. Tests that need to
// embed the URI in args JSON call fx.ProjectURI() instead of
// hand-rolling the prefix so the canonical shape lives in one
// place.
//
// ponytail: this is what the plan originally called `ProjectURI
// string` field on Fixture. A method keeps the fixture constructor
// signature stable (adding a field would force every direct
// Fixture{...} literal to grow a new field — there's one in
// helpers_test.go's TestExploreFixture round-trip tests).
func (f *Fixture) ProjectURI() string {
	return "file://" + f.Root
}

// WithProject prepends a `project` (file:// URI) field to a JSON
// object args literal if the field is missing. Mirrors what every
// per-tool handler does at the start of a request: ensure
// `project` is set. Used by test helpers (callSnippet,
// findSymbols, etc.) so the per-tool args JSON in the test bodies
// doesn't have to know the tempdir path up front.
//
// Args are expected to be a JSON object literal starting with
// `{`. The returned string is also a JSON object literal.
//
// This is a top-level function (not a method) so it composes
// with helpers that don't have a Fixture handle — the registry
// tests seed a registry for an absolute path before any fixture
// exists, for example.
func WithProject(root, args string) string {
	if strings.Contains(args, `"project"`) {
		return args
	}
	if args == "{}" {
		return `{"project":"file://` + root + `"}`
	}
	return `{"project":"file://` + root + `",` + args[1:]
}