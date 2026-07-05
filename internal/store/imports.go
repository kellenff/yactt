package store

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// ExtractImportPath returns the bare import path from a tree-sitter Go
// `import_declaration` or a TypeScript/JavaScript `import_statement`
// node. Returns "" when the node has no parseable path or is not an
// import node.
//
// Tree-sitter Go emits `import_declaration` whose string child is an
// `interpreted_string_literal`. TS/JS uses `import_statement` whose
// path is a `string`. The walker descends once through the node tree
// because Go wraps the path inside an `import_spec` while TS/JS keeps
// the string at the statement level.
//
// The returned value has any surrounding quote characters stripped so
// callers can use it as a canonical import path or module specifier.
func ExtractImportPath(n *sitter.Node, src []byte) string {
	if n == nil {
		return ""
	}
	targetType := ""
	switch n.Type() {
	case "import_declaration":
		targetType = "interpreted_string_literal"
	case "import_statement":
		targetType = "string"
	default:
		return ""
	}
	var found *sitter.Node
	var walk func(*sitter.Node) bool
	walk = func(x *sitter.Node) bool {
		if x == nil {
			return false
		}
		if x.Type() == targetType {
			found = x
			return true
		}
		for i := 0; i < int(x.ChildCount()); i++ {
			if walk(x.Child(i)) {
				return true
			}
		}
		return false
	}
	walk(n)
	if found == nil {
		return ""
	}
	return stripImportQuotes(found.Content(src))
}

// stripImportQuotes removes surrounding "..." or '...' from a Go
// interpreted-string literal or a TS/JS string source fragment.
// Pass-through when the input doesn't look like a quoted string.
func stripImportQuotes(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		return s[1 : len(s)-1]
	}
	return s
}
