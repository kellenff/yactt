# AGENTS.md

## Cursor Cloud specific instructions

`yactt` is a single Go module (`github.com/kellenff/yactt`, Go 1.26.5) — a read-only
code-intelligence MCP server + CLI. There is no database, web frontend, or other
external service; the binary is self-contained (tree-sitter grammars are compiled in).
The `plugins/`, `pi-extension/`, and `junie-extension/` directories are just installers
for the same binary.

Standard dev commands (source of truth: `README.md` and `.github/workflows/ci.yml`):
- Lint/static check: `go vet ./...`
- Test: `go test ./...` (the `internal/lsp` package takes ~20s because it spins up the
  bundled `stubserver` fake LSP; total suite ~30–40s — do not assume it hung)
- Build (dev): `go build -trimpath -o /tmp/yactt ./cmd/yactt`
- Run CLI: `/tmp/yactt overview <abs-path>`; MCP over stdio: `/tmp/yactt mcp serve`;
  MCP over HTTP: `/tmp/yactt mcp serve-http --port=8080` (endpoints `/mcp`, `/healthz`).

Non-obvious caveats:
- Language servers (`gopls`, `typescript-language-server`, `pyright-langserver`,
  `rust-analyzer`) are OPTIONAL. When absent from `PATH`, yactt logs
  `lsp: startup declined ... server not on PATH` and falls back to tree-sitter
  (`provenance.tool = "tree-sitter"`). This is expected, not an error — the product is
  fully functional without them.
- MCP HTTP: after `initialize`, every subsequent request MUST send the
  `MCP-Protocol-Version: 2025-03-26` header AND the `Mcp-Session-Id` header returned by
  `initialize`; otherwise the server rejects with `unsupported protocol version`. `Accept`
  must include both `application/json` and `text/event-stream`. Tool calls stream back as
  SSE (`event: message` / `data: {...}`).
- Tool argument names: `find_symbol` uses `name_path` (not `name`); `search_code` uses
  `pattern` (not `query`). Every targeting tool requires a `project` field shaped as an
  absolute `file:///abs/path` URI, and the repo must first be loaded via `index_repository`.
- CI security gates: `gitleaks detect --source .` passes clean. `govulncheck ./...` is
  pinned to `@v1.1.4` in CI, which is too old to analyze Go 1.26 code (it prints
  `package requires newer Go version go1.26`). This is a tool-version limitation of the
  CI gate, not a code problem, and does not affect local dev/build/test.
- There is no Makefile despite a `make check` mention in CI comments; `go vet ./...` +
  `go test ./...` are the equivalent gate.
