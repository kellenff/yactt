# Deep Dive — yactt

> Stage 1 of the GitHub README For Perfectionists workflow.
> Generated 2026-07-05.

**Graph tools available:** Yes (codebase-memory-mcp, 1432 nodes / 5158 edges).
**Methods used:** graph (primary); filename-fallback for the plugin README content and CI workflows.
**Project ID:** `Users-kellen-Projects-yactt`

---

## 1. Identity

| | |
|---|---|
| **Name** | YACTT |
| **Backronym** | *Yet Another Code Tree Tool* |
| **Tagline** | *Federated code intelligence for AI agents — walk the tree, choose your layer.* |
| **Module path** | `github.com/kellenff/yactt` |
| **License** | Dual: Apache-2.0 / MIT |
| **CLI binary** | `yactt` |
| **MCP server name** | `yactt` |
| **MCP server version** | `mvp` (advertises protocol `2024-11-05`) |

**Sources**: `cmd/yactt/main.go` (`Package main` doc); `design.md` § "Project identity"; `.mcp.json`.

---

## 2. The problem

`design.md` calls it the **"empty quadrant"**: every existing code intelligence tool sits on a tradeoff diagonal once you limit to polyglot Go/TS/JS/Python:

- **Lossy + deep** — CodeQL, Semgrep, Sourcegraph/SCIP/LSIF
- **Lossy + shallow** — `grep`, ripgrep
- **Lossless + shallow** — `cat`, file readers

YACTT targets the missing corner: **lossless + deep** in the polyglot zone — i.e. give an AI agent both the raw source AND the resolved semantics it needs to do serious work, without bolting together three tools and their respective quirks.

**Source**: `docs/design.md` § 1 ("Motivation — the empty quadrant")

---

## 3. Who it serves

Direct audience: **AI agents and the teams that wire them up** (the README of the bundled Claude Code plugin says: *"Federated code-intelligence MCP server + `code-explore` skill for Claude Code"*). The CLI verb `overview` and the `tree_overview` MCP tool make the same point — the user isn't a developer reading a codebase; it's a model exploring one.

Secondary audience: developers using Claude Code (or any MCP-capable client) as their AI pair — the plugin README installs without a Go toolchain.

**Sources**: `plugins/yactt/README.md`; `cmd/yactt/main.go`.

---

## 4. Core features (the 10 MCP tools)

Registered in `cmd/yactt/main.go:registerAllTools` and exposed per the design § 5.1 table:

| Tool | Purpose |
|---|---|
| `tree_overview` | Top of the repo tree, depth-limited (entry point for any agent) |
| `node_get` | One or more layers of a node — summary, signature, body, source, tokens |
| `node_source` | Lossless source, optionally line-bounded |
| `node_edges` | Cross-references — callers, callees, tests, overrides, imports |
| `search` | Symbols by name or doc-comment matching |
| `find_symbol` | Locate by qualified name path with glob support |
| `get_symbols_overview` | Top-level outline of a file |
| `find_code` | AST-aware (tree-sitter) or regex search across files |
| `find_referencing_symbols` | All references to a given symbol |
| `edit_impact` | Analyze the impact of a proposed set of renames (**does NOT apply**) |
| `persisted_query` | Run a registered persisted query by id (curated workflows) |

Every tool declares both an `InputSchema` and an `OutputSchema` (the MCP contract on `structuredContent` is enforced at registration time).

**Source**: `cmd/yactt/main.go:registerAllTools` (line 208).

---

## 5. Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                       cmd/yactt/main.go                     │
│          (CLI dispatch: overview | mcp serve | help)        │
└─────────────────┬───────────────────────────────────────────┘
                  │
        ┌─────────┼──────────────────┐
        ▼         ▼                  ▼
┌──────────────┐ ┌──────────────┐ ┌────────────────────┐
│ internal/mcp │ │ internal/    │ │ internal/persisted │
│ (JSON-RPC    │ │ tool/        │ │ (curated-workflow  │
│  server)     │ │ (10 handlers │ │  registry: ops by  │
│              │ │  + schemas)  │ │  id)               │
└──────────────┘ └──────┬───────┘ └────────────────────┘
                        │ depends on
                        ▼
                ┌───────────────┐
                │ internal/store│  ◄── the orchestration heart
                │   (Repo)      │
                └──┬───┬───┬───┘
                   │   │   │
        ┌──────────┘   │   └──────────┐
        ▼              ▼              ▼
   ┌─────────┐   ┌───────────┐   ┌─────────┐
   │ parser  │   │   cache   │   │   lsp   │
   │ (tree-  │   │ (mem +    │   │ (gopls, │
   │  sitter │   │  disk)    │   │ ts-ls)  │
   │ grammars│   │           │   │         │
   └─────────┘   └───────────┘   └─────────┘
        │
        ▼
   ┌─────────┐
   │ source  │  (parsed source.File: bytes + AST root)
   └─────────┘
```

### Package roles

| Package | Role |
|---|---|
| `cmd/yactt` | CLI; thin dispatcher. Loads repo via `store.Load`, wires `tool/*` handlers into an `mcp.Server`. |
| `internal/mcp` | `Server` struct, JSON-RPC 2.0 over stdio. Owns `ToolDef` registry; validates `OutputSchema` is `type:"object"` at registration time. |
| `internal/store` | Per-repo orchestration. Owns the file index (`map[path]*source.File`), symbol index (`symbolsByPath`), parsed-file cache, and an LSP subgraph keyed by `parser.Name`. `Repo` is "intentionally narrow — a value object, not a service." |
| `internal/parser` | Tree-sitter boundary: `Language` interface + concrete `Go`, `TypeScript`, `JavaScript` drivers. Languages listed in priority order in `All()`. |
| `internal/source` | `File` struct holding bytes + AST root, plus per-language load helpers. |
| `internal/lsp` | LSP client wrapper. Speaks to gopls (Go) and typescript-language-server (TS/JS — one client serves both keys). Opportunistic startup at `Load` time; tree-sitter is the unconditional floor. |
| `internal/cache` | Two-tier: in-memory `*Cache` + opt-in disk `*DiskCache`. Disk-cache path is `$XDG_CACHE_HOME/yactt/<sha256(root)[:16]>` (or `~/.cache/yactt/...`). Cap default: 512 MiB, override via `YACTT_DISK_CACHE_MAX_BYTES`. |
| `internal/tool` | 10 tool handlers + their JSON-Schema inputs/outputs + the `persisted_query` wrapper. |
| `internal/persisted` | Curated-workflow registry: ops registered by id; one MCP tool call per op in MVP. `Runner` dispatches against a `map[string]ToolFunc`. |
| `internal/domain` | Cross-cutting types (`Provenance`, etc.). |
| `plugins/yactt` | Claude Code plugin: `code-explore` skill, SessionStart bootstrap hook (downloads + SHA256-verifies the binary from GitHub releases). |

### Loading model

`store.Load(root, opts...)`:

1. Resolve `root` to absolute path; reject if not a directory.
2. Walk, skipping `.git`, `vendor`, `node_modules`, and any hidden dir.
3. `parser.Detect(path)` routes to the right grammar; unknown extensions skipped silently.
4. Disk-cache hit (mtime match) → skip re-parse. Miss → `source.LoadFile` + `parser.ExtractSymbols` + best-effort `diskCache.Put`.
5. `WithMaxFiles` cap (default 50 000) aborts the walk with `ErrMaxFilesExceeded` on overflow; partial repo still returned.
6. Optional disk-cache orphan sweep after the walk settles.
7. `detectGoModule` + `inferRootPackage` if a `go.mod` is at root.
8. `rebuildIndex()` to expose the cross-file symbol index used by `tree_overview`, `search`, `find_symbol`.
9. **Opportunistic LSP startup** (15 s overall timeout): try `gopls` for Go and `typescript-language-server` for TS+JS. Failures leave the slot nil — materializers fall through to tree-sitter with the "no-lsp-installed" provenance marker.
10. **Warm the language servers**: open every parsed file in the server that owns it (5 s per-file, 30 s overall) so the first agent request doesn't pay parse latency.

`Repo` is safe for concurrent use (`sync.RWMutex` on files/symbols maps).

**Source**: `internal/store/store.go` (full file), `internal/parser/language.go`, `internal/mcp/server.go`.

---

## 6. Entry points

| Path | Purpose |
|---|---|
| `cmd/yactt/main.go:main` (line 54) | CLI dispatcher — `help` / `version` / `overview <path>` / `mcp serve [path]` |
| `cmd/yactt/main.go:runOverview` (line 90) | Loads repo, prints 2-level tree as JSON |
| `cmd/yactt/main.go:runMCPServe` (line 118) | Loads repo, wires 11 tools, runs JSON-RPC over stdio until EOF/SIGINT/SIGTERM |
| `cmd/yactt/main.go:registerAllTools` (line 208) | The single registration site — combines `tool/*` handlers + `persisted_query` |
| `internal/store/store.go:Repo.Load` | Repo construction; also documented above |
| `plugins/yactt/.mcp.json` | Claude Code plugin config (`command: yactt`, `args: ["mcp", "serve", "${CLAUDE_PROJECT_DIR}"]`, `timeout: 30000`) |
| `.mcp.json` | Same shape, no timeout |

`mcp.NewServer` validates each tool's `OutputSchema` parses as JSON Schema and declares `type:"object"` at registration. The empty-schema backstop is the project's parse-don't-validate enforcement.

---

## 7. What makes yactt unique

1. **Sits in the "empty quadrant"** — lossless source + semantic depth, in polyglot Go/TS/JS/Python. Tree-sitter + LSP/SCIP, with tree-sitter as the unconditional floor when the server isn't wired.
2. **MCP-native, agent-first design** — `tree_overview` is the canonical entry point; tools are layered (summary → signature → body → source → tokens) so an agent pays only for what it needs.
3. **`parse don't validate`** — JSON-Schema enforcement at registration, not at call time. `OutputSchema` is required, must be `type:"object"`; tools return JSON objects by contract.
4. **Disk cache keyed per-repo** — `sha256(repoRoot)[:16]` keeps different repos from colliding; mtime-keyed LRU evicts orphans after each walk.
5. **`edit_impact` is non-destructive** — "Does NOT apply them." Critical for agents: a rename's blast radius is a *proposal*, not a transaction.
6. **Curated workflows via `persisted_query`** — a second registry on top of the same MCP handlers, so an op's `tool:` name resolves to the same closure the agent would call directly. Stops agents from reconstructing call shapes turn after turn.
7. **No toolchain required for consumers** — `plugins/yactt/README.md` installs from a GitHub release, SHA256-verifies against published `SHA256SUMS`, ships **SLSA Build Provenance Level 3 attestations** verifiable with `gh attestation verify`.

**Sources**: `cmd/yactt/main.go:registerAllTools`, `internal/mcp/server.go:validateOutputSchema`, `internal/persisted/registry.go`, `plugins/yactt/README.md`.

---

## 8. Languages supported

Wired in code today (from `internal/parser/language.go:All()`):

- **Go** — tree-sitter + gopls
- **TypeScript** — tree-sitter + typescript-language-server (registered against `LangTypeScript` and `LangJavaScript` keys against the same client)
- **JavaScript** — same typescript-language-server

`docs/design.md` lists Python in scope, but the parser module does not yet wire a Python grammar.

**Source**: `internal/parser/language.go` (`Name` constants; `ByName` switch).

---

## 9. Plugin layer

`plugins/yactt/` ships:

- **SessionStart hook** (`scripts/install.sh`) — on first use downloads the platform-matched tarball from `kellenff/yactt/releases/latest`, verifies SHA256 against the published `SHA256SUMS`, installs to `$XDG_HOME/bin/yactt` (or `$HOME/.local/bin/yactt`). TOFU check against `${XDG_DATA_HOME:-~/.local/share}/yactt/known-good` blocks same-version-replacement attacks.
- **`code-explore` skill** (`skills/code-explore/SKILL.md`) — triggers on prompts about exploring, summarizing, or reviewing code; teaches the agent the canonical `tree_overview` → `find_symbol` → `node_get` → `node_source` progression.
- **`.mcp.json`** registers the `yactt` MCP server with `${CLAUDE_PROJECT_DIR}` as the root path.

**Source**: `plugins/yactt/README.md`; `plugins/yactt/.mcp.json`.

---

## 10. CI / release posture

- `.github/workflows/ci.yml` — single CI gate (see `make-check` memory).
- `.github/workflows/release.yml` — release workflow using `actions/attest@v4` for SLSA Build Provenance Level 3 attestations.
- `Makefile` exists at repo root (see memory note: `make check` is the only CI gate).

---

## 11. Memory notes that constrain the writeup

These are auto-recorded facts about yactt that should shape later stages:

- **Phase F slicing status** — Phase F complete (from `phase-f-slicing.md`).
- **YACTT progress** — V1 + V2.x + V3 subgraph + V3 first + V3 method-bodies slice shipped (from `yactt-progress.md`).
- **Tool-layer method ID inconsistency** — resolved 2026-07-04; single-source `id.For` + per-receiver keying for call-edge (from `tool-layer-method-id-inconsistency.md`).
- **Mockery pattern** — mockery v2.53.6, generated mocks, `EXPECT()` pattern (from `mockery-pattern.md`).
- **golangci-lint** pinned v1.42.1 — forces `interface{}` over `any` in linted code.
- **Tree-sitter bindings** — grammar packages export Go bindings at `.../bindings/go`.
- **Tree-overview security** — `AllowedRoots` + symlink skip + `MaxFiles` cap (memory `tree-overview-security.md`, applied in commit 2154894).
- **Tree-overview summary rule** — first line of doc comment, no kind prefix.

---

## 12. Things NOT in the codebase yet (worth flagging in later stages)

- **Python grammar** — listed in `design.md` § "Languages" but not wired in `internal/parser/language.go:All()`.
- **Multi-repo / cross-repo** — design.md scopes MVP to one repo.
- **CodeQL-style semantic search** — not advertised; `find_code` is regex or tree-sitter AST patterns.
- **GraphQL** — design says "internal query fabric" but no `internal/gql` package exists in code today.

---

## Output target

The README at the project root (`/Users/kellen/Projects/yactt/README.md`) does not yet exist. `plugins/yactt/README.md` is a great secondary reference for the user-facing install + skill story but isn't the project README.

---

## Ready for Stage 2

Crystal Ball next — envision what yactt **could** become. The Phase F slicing status, V3 method-bodies slice, and the design doc's roadmap hooks (multi-repo, Python, persisted-query step chaining per the persisted registry TODO) are the raw material.
