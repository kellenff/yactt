// Package lsp is the Tier-1 backend per design §5.5: a JSON-RPC 2.0 client
// over stdio that talks to a language server (gopls for now). It produces
// typed answers — hover contents, references, definitions — that the rest of
// the program can stamp with `Provenance.Tool: "gopls"`.
//
// The package is small by design. It owns one client per `Repo` (started
// opportunistically at Load if gopls is on PATH), one JSON-RPC stdio
// connection, and typed wrappers around the handful of LSP methods the
// materializers need.
package lsp

// Position is a 0-based line/character offset on a document. LSP positions
// are 0-based per the LSP spec; tree-sitter rows are also 0-based, so no
// translation is needed.
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Range is the inclusive-exclusive [Start, End) span on a document.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Location links a position range to a remote document URI.
type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

// MarkupContent is one of the three LSP hover-content shapes (plaintext,
// markdown, or an array of marked strings). We model the object form only —
// gopls returns it for Go.
type MarkupContent struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// HoverResult is the body of a `textDocument/hover` reply.
type HoverResult struct {
	Contents MarkupContent `json:"contents"`
	Range    Range         `json:"range"`
}

// TextDocumentIdentifier names one open document by URI.
type TextDocumentIdentifier struct {
	URI string `json:"uri"`
}

// HoverParams is the body of a `textDocument/hover` request.
type HoverParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

// ReferencesParams is the body of a `textDocument/references` request.
type ReferencesParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
	IncludeDecl  bool                   `json:"includeDeclaration"`
}

// DefinitionParams is the body of a `textDocument/definition` request.
type DefinitionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

// DocumentSymbolParams is the body of a `textDocument/documentSymbol`
// request, used by OpenWorkspace to force gopls to finish indexing a
// freshly opened file. We don't decode the result — we only care that
// the round-trip completed, which implies the file is fully processed.
type DocumentSymbolParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

// InitializeParams is the body of the `initialize` handshake request.
//
// Capabilities are advertised as `{}` — the LSP spec lets servers assume
// defaults; gopls only needs a root + trace.
type InitializeParams struct {
	ProcessID *int   `json:"processId,omitempty"`
	RootURI   string `json:"rootUri"`
	Trace     string `json:"trace,omitempty"`
	// Capabilities is intentionally omitted: omitempty drops it on the wire
	// and gopls treats its absence as "all defaults".
}

// InitializeResult is the body of the `initialize` reply.
//
// We only read the fields we use; gopls returns a lot more.
type InitializeResult struct {
	ServerInfo ServerInfo `json:"serverInfo"`
}

// ServerInfo identifies the server. The Name + Version pair is what we
// capture into `Provenance`.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// WorkspaceFoldersParams is the body of a `workspace/didChangeWatchedFiles`
// notification (we just trigger an empty workspaceFolders handshake so gopls
// is happy; the heavy lifting is done via `textDocument/didOpen`).
type WorkspaceFoldersParams struct {
	Event interface{} `json:"event"`
}

// DidOpenTextDocumentParams is the body of a `textDocument/didOpen` notification.
type DidOpenTextDocumentParams struct {
	TextDocument TextDocumentItem `json:"textDocument"`
}

// TextDocumentItem is the body of `textDocument/didOpen`.
type TextDocumentItem struct {
	URI        string `json:"uri"`
	LanguageID string `json:"languageId"`
	Version    int    `json:"version"`
	Text       string `json:"text"`
}
