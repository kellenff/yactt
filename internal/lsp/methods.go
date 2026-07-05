package lsp

import "context"

// URIFor converts a filesystem path to the file:// URI form that LSP
// expects in TextDocumentIdentifier.
func URIFor(path string) string { return fileURI(path) }

// PathOf converts a file:// URI back to a filesystem path. Useful when
// reading `Locations` from a server reply.
func PathOf(uri string) string {
	const scheme = "file://"
	if len(uri) > len(scheme) && uri[:len(scheme)] == scheme {
		return uri[len(scheme):]
	}
	return uri
}

// Hover calls `textDocument/hover` for the given (file, line, col) and
// returns the typed reply. An empty MarkupContent.Value means gopls had
// nothing to say (e.g. on `package`-level identifiers) — callers should
// treat that as "no Tier 1 answer; fall back to tree-sitter".
func (c *Client) Hover(ctx context.Context, file string, line, col int) (HoverResult, error) {
	var h HoverResult
	err := c.Request(ctx, "textDocument/hover", HoverParams{
		TextDocument: TextDocumentIdentifier{URI: URIFor(file)},
		Position:     Position{Line: line, Character: col},
	}, &h)
	return h, err
}

// References calls `textDocument/references` for the symbol at (file,
// line, col). When `includeDecl` is true the declaration location is
// included in the result (gopls's default is the opposite, so we pass the
// flag explicitly).
func (c *Client) References(ctx context.Context, file string, line, col int, includeDecl bool) ([]Location, error) {
	var locs []Location
	err := c.Request(ctx, "textDocument/references", ReferencesParams{
		TextDocument: TextDocumentIdentifier{URI: URIFor(file)},
		Position:     Position{Line: line, Character: col},
		IncludeDecl:  includeDecl,
	}, &locs)
	if err != nil {
		return nil, err
	}
	return locs, nil
}

// Definition calls `textDocument/definition` and returns the resolved
// locations. Note: `Definition` is implemented but not yet wired into the
// materializers (Tier 1.5 per the plan).
func (c *Client) Definition(ctx context.Context, file string, line, col int) ([]Location, error) {
	var locs []Location
	err := c.Request(ctx, "textDocument/definition", DefinitionParams{
		TextDocument: TextDocumentIdentifier{URI: URIFor(file)},
		Position:     Position{Line: line, Character: col},
	}, &locs)
	if err != nil {
		return nil, err
	}
	return locs, nil
}
