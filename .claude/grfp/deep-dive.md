# Deep Dive — yactt

> Stage 1 of the GitHub README For Perfectionists workflow.
> Generated 2026-07-06 (refresh — pull from origin brought Issue #11 + search_code onto main).
>
> **Graph tools available:** Yes (codebase-memory-mcp, 1,858 nodes / 7,171 edges, status `ready`).
> **Methods used:** `get_architecture`, `search_graph`, `query_graph`, `get_graph_schema` (primary); `Read` / `wc -l` / `ls` for the README, design doc, registry, list_projects, go.mod, and per-package LOC counts.
> **Project ID:** `Users-kellen-Projects-yactt`

---

## 1. Identity

| | |
|---|---|
| **Name** | yactt (YACTT) |
| **Backronym** | *Yet Another Code Tree Tool* — self-aware in the GNU / YACC / WINE tradition |
| **Tagline** | *Federated code intelligence for AI agents — lossless source, resolved semantics, MCP-native.* |
| **Module path** | `github.com/kellenff/yactt` |
| **License** | Dual: Apache-2.0 / MIT |
| **CLI binary** | `yactt` |
| **MCP server name** | `yactt` |
| **MCP protocol** | `2024-11-05` |
| **Go version** | 1.26.4 |
| **External deps** | ONE — `github.com/smacker/go-tree-sitter` |
| **Audience** | AI agents and the teams that wire them up. The user isn't a developer reading a codebase; it's a model exploring one. |

**Sources**: `README.md`; `cmd/yactt/main.go:Package main doc`; `docs/design.md § Project identity`; `go.mod`.

---

## 2. The problem — the empty quadrant

`docs/design.md § 1` calls it the **"empty quadrant"**. Every existing code intelligence tool sits on a strict tradeoff diagonal once you restrict to polyglot Go/TS/JS:

| | Lossless source | Lossy summary |
|---|---|---|
| **Resolved semantics** | *(empty quadrant — yactt's target)* | CodeQL, Semgrep, Sourcegraph/SCIP/LSIF |
| **Syntactic only** | `cat`, file readers | `grep`, ripgrep |

Roslyn breaks the diagonal in C#/.NET — no equivalent exists for Go/TS/JS/Python. yactt fills it by **federation, not replacement**: glue tree-sitter + opportunistic LSP under one identity, expose them through one tree-shaped view, let the consumer choose fidelity per query.

**Sources**: `README.md § Why yactt`; `docs/design.md § 1`.

---

## 3. The architecture — tree of nodes, multiple layers

Each node has a **stable identity** (e.g. `fn:auth.login.Handler.HandleCallback`), a **kind** (`repo | package | file | function | method | class | module | …`), optional **layers**, optional **edges**. Each layer is owned by exactly one tool:

| Layer | Owner | Notes |
|---|---|---|
| `summary` | summarizer | deterministic, no LLM |
| `signature` | LSP | typed, hover-derived |
| `body` | LSP / SCIP | typed + cross-file resolved |
| `source` | tree-sitter | lossless, comments + whitespace preserved |
| `tokens` | tree-sitter | full CST, every node + range |
| `edges` | LSP / SCIP / tree-sitter | callers / callees / tests (tier-ranked fallback) |

### Tier-ranked fallback (mandatory, not optional)

```
Tier 0 = SCIP (if available)
Tier 1 = LSP
Tier 2 = tree-sitter
```

`Provenance.Tool` is stamped on every answer so the agent can branch on what it trusts.

**Sources**: `docs/design.md § 2.1, § 5.4, § 5.5`; `README.md § How it works`.

---

## 4. The 21 MCP tools

### 4.1 — 16 code-intelligence tools (single-repo mode)

| Tool | LOC | Purpose |
|---|---|---|
| `tree_overview` | 243 | Top of repo tree, depth-limited. Start here. |
| `node_get` | 94 | One or more layers of a node (`summary`, `signature`, `body`, `source`, `tokens`). |
| `node_source` | 112 | Lossless source for a node, optionally line-bounded. |
| `node_edges` | **783** | Cross-refs — `callers`, `callees`, `tests`, `overrides`, `imports`. |
| `search` | 141 | Find symbols by name or doc-comment matching. |
| `find_symbol` | 170 | Locate by qualified name path with glob support. |
| `get_symbols_overview` | 119 | Top-level outline of a single file. |
| `find_code` | 368 | AST-aware (tree-sitter) or regex pattern search. |
| `search_code` | **329** | `find_code` matches collapsed into containing functions, deduped by symbol, ranked structurally. |
| `find_referencing_symbols` | 168 | All references to a given symbol. |
| `edit_impact` | 139 | Blast radius of proposed renames (**does NOT apply**). |
| `get_graph_schema` | 116 | Canonical `nodeKinds`, `edgeKinds`, `layers`. |
| `get_code_snippet` | 230 | Source slice for symbol by id OR name_path. |
| `get_architecture` | **559** | Langs, packages, hotspots, dead-code candidates, import cycles (Tarjan SCC). |
| `query_graph` | 348 | Multi-hop traversal — chain edge kinds with `follow`, cap with `depth`/`limit`. |
| `detect_changes` | **484** | Impact of a `git diff` between two refs (or `since` → HEAD); changed files + per-file hunks + enclosing function + callers/tests/overrides per affected symbol. |

### 4.2 — 4 registry tools (work in both modes)

| Tool | LOC | Purpose |
|---|---|---|
| `list_projects` | 69 | Enumerate every indexed repo. |
| `index_repository` | 187 | Walk a repo at `path`, write registry entry, prime cache. |
| `index_status` | 179 | Registry row + per-repo cache freshness. |
| `delete_project` | 105 | Evict `path` from registry; idempotent. |

### 4.3 — 1 persisted_query tool

Runs registered curated workflows by id. Ships `onboarding` out of the box — a one-shot `tree_overview` at depth 2.

Every tool declares both an `InputSchema` and an `OutputSchema` — `OutputSchema` must declare `type:"object"`, enforced at registration time. Agents get structured results they can branch on without parsing prose.

**Sources**: `cmd/yactt/main.go:registerAllTools`; `internal/tool/*.go`; `internal/mcp/server.go:validateOutputSchema`.

---

## 5. Architecture

```
┌──────────────────────────────────────────────────────────┐
│  MCP client (agent)                                      │
└─────────────────────────┬────────────────────────────────┘
                          │ JSON-RPC 2.0 over stdio (2024-11-05)
                          ▼
┌──────────────────────────────────────────────────────────┐
│  yactt CLI  (cmd/yactt/main.go, 475 LOC)                 │
│  ─ loads repo, wires 21 tools, runs server               │
└─────────────────────────┬────────────────────────────────┘
                          │
        ┌─────────────────┼─────────────────┐
        ▼                 ▼                 ▼
┌───────────────┐ ┌──────────────┐ ┌───────────────────┐
│ internal/mcp  │ │ internal/tool│ │ internal/persisted│
│ JSON-RPC      │ │ 21 handlers  │ │ curated-workflow  │
│ server        │ │ + schemas    │ │ registry          │
└───────┬───────┘ └──────┬───────┘ └─────────┬─────────┘
        └─────────────────┼─────────────────┘
                          ▼
                ┌───────────────────┐
                │ internal/store    │  per-repo orchestration (612 LOC)
                │   (Repo)          │
                └─┬──────┬──────┬───┘
                  │      │      │
                  ▼      ▼      ▼
            ┌─────┐ ┌─────┐ ┌─────────┐
            │parser│ │cache│ │   lsp   │
            │tree-│ │mem+ │ │ gopls + │
            │sitter│ │disk │ │ts-lang  │
            └─────┘ └─────┘ └─────────┘
```

### Package roles

| Package | Role |
|---|---|
| `cmd/yactt` | CLI; thin dispatcher. Loads repo via `store.Load`, wires `tool/*` handlers into an `mcp.Server`. |
| `internal/mcp` | `Server` struct, JSON-RPC 2.0 over stdio. Owns `ToolDef` registry; validates `OutputSchema` is `type:"object"` at registration time. |
| `internal/store` | Per-repo orchestration. Owns the file index, symbol index, parsed-file cache, and an LSP subgraph keyed by `parser.Name`. `Repo` is "intentionally narrow — a value object, not a service." |
| `internal/registry` | Multi-repo book-keeping (Issue #8). `projects.json` at `$XDG_CACHE_HOME/yactt/`. Mutex-serialised; corrupt file renamed to `.corrupt-<unix-seconds>`. |
| `internal/parser` | Tree-sitter boundary: `Language` interface + concrete `Go`, `TypeScript`, `JavaScript` drivers. |
| `internal/source` | `File` struct holding bytes + AST root, plus per-language load helpers. |
| `internal/lsp` | LSP client wrapper. Speaks to gopls (Go) and typescript-language-server (TS/JS — one client serves both keys). Opportunistic startup at `Load` time. |
| `internal/cache` | Two-tier: in-memory `*Cache` + opt-in disk `*DiskCache`. Disk-cache path is `$XDG_CACHE_HOME/yactt/<sha256(root)[:16]>`. Cap default: 512 MiB. |
| `internal/tool` | 21 tool handlers + their JSON-Schema inputs/outputs + `helpers.go` for shared provenance stamping. |
| `internal/id` | Single-source `id.For` node identity (resolved 2026-07-04). |
| `internal/domain` | Cross-cutting types (`Provenance`) + identifier-name sanitization (AST05 mitigation). |
| `internal/audit` | Structured startup line on stderr + opt-in `--audit-log=<file>` per `tools/call`. |
| `internal/contract` | Shared types + cross-package contract tests (wire-shape enforcement). |
| `internal/persisted` | Curated-workflow registry; one MCP tool call per op. |
| `internal/search` | Search infrastructure backing `search` + `search_code`. |
| `internal/summarizer` | Deterministic, no-LLM summarization. |

### Loading model — `store.Load(root, opts...)`

1. Resolve `root` to absolute path; reject if not a directory.
2. Walk, skipping `.git`, `vendor`, `node_modules`, and any hidden dir.
3. `parser.Detect(path)` routes to the right grammar; unknown extensions skipped silently.
4. Disk-cache hit (mtime match) → skip re-parse. Miss → `source.LoadFile` + `parser.ExtractSymbols` + best-effort `diskCache.Put`.
5. `WithMaxFiles` cap (default 50,000) aborts with `ErrMaxFilesExceeded` on overflow; partial repo still returned.
6. `rebuildIndex()` to expose the cross-file symbol index.
7. **Opportunistic LSP startup** (15 s overall): gopls for Go, typescript-language-server for TS+JS. Failures leave the slot nil; materializers fall through to tree-sitter with `Provenance.Tool = "tree-sitter"`.
8. **Warm the language servers**: open every parsed file (5 s per-file, 30 s overall) so the first agent request doesn't pay parse latency.

### Two run modes from one binary

- `yactt mcp serve <path>` — single-repo mode. Loads `<path>`, exposes all 21 tools (16 code-intel + 4 registry + `persisted_query`).
- `yactt mcp serve` (no path) — registry mode. Skips repo load; exposes only the 4 registry tools + `persisted_query`. Agents discover/manage which repos are indexed before drilling into one.

Indexing is decoupled from serving: an agent in registry mode can call `index_repository` to prime a repo's cache, then a separate `yactt mcp serve <that-path>` can serve it with warm caches and zero re-parse.

**Sources**: `cmd/yactt/main.go`; `internal/store/store.go`; `internal/registry/registry.go`; `README.md § How it works`.

---

## 6. The hard safety valves

| Constraint | Default | Override |
|---|---|---|
| `MaxFiles` (walk cap) | 50,000 | — |
| Disk cache | 512 MiB | `YACTT_DISK_CACHE_MAX_BYTES` |
| LSP startup budget | 15 s | — |
| Per-file LSP warm-up | 5 s | — |
| `AllowedRoots` | required on every path-bearing tool | — |

Partial repo is still returned on `MaxFiles` overflow. Failed LSP servers fall through to tree-sitter with `Provenance.Tool = "tree-sitter"` — yactt never fails because the language server is missing.

**Sources**: `README.md § How it works`; memory `tree-overview-security`.

---

## 7. Shipped state (current — post-Issue #11 merge)

**[memory: yactt-progress] + [graph: recent commits]**

- V1, V2.x, V3 subgraph slice, V3 first slice, V3 method-bodies slice
- Phase F (per-repo provenance + edge model)
- Single-source `id.For` + per-receiver keying for call-edges (resolved 2026-07-04)
- **Issue #8** — multi-repo registry + 4 MCP tools
- **Issue #9** — `query_graph` multi-hop traversal
- **Issue #11** — `detect_changes` git-ref diff impact (just landed)
- `search_code` — dedup + rank `find_code` matches by enclosing symbol
- `get_architecture` — Tarjan SCC import cycles + dead-code candidates + hotspots
- `wire_shape_test.go` — 382 LOC of contract tests pinning the JSON schemas
- SLSA Build Provenance Level 3 on every release (`gh attestation verify`)

### Headline LOC

```
internal/tool/         ~13 tools, ~9.6k LOC incl. tests
internal/registry/     666 LOC
internal/store/        3.2k LOC
cmd/yactt/             666 LOC
total                  ~14.3k LOC (incl. tests)
```

---

## 8. What makes yactt unique

1. **Sits in the empty quadrant** — lossless source + semantic depth, polyglot. No equivalent in the Go/TS/JS world.
2. **Federation, not replacement** — each layer owned by exactly one tool; tier-ranked fallback; provenance stamped on every answer.
3. **MCP-native wire surface** — every tool declares `InputSchema` + `OutputSchema` (`type:"object"` enforced at registration).
4. **Two run modes from one binary** — `serve <path>` (single-repo) vs `serve` (registry-only). Indexing decoupled from serving.
5. **One external dependency** — `go-tree-sitter`. Everything else is in-tree. The dep surface IS the security story.
6. **SLSA Build Provenance Level 3** on every release; `gh attestation verify` per tarball.
7. **Provenance is mandatory** — `Provenance.Tool = "tree-sitter"` vs `"gopls"` lets the agent trust one layer over another.
8. **Read-only by design** — no write tools. `edit_impact` analyzes renames; never applies them.
9. **Self-aware naming** — "Yet Another Code Tree Tool"; tongue-in-cheek acronym on a serious tool.
10. **Per-receiver keying for call-edges** — single-source `id.For` resolved 2026-07-04; eliminates method-vs-function ambiguity.

**Sources**: `README.md`; `cmd/yactt/main.go:registerAllTools`; `internal/mcp/server.go:validateOutputSchema`; `docs/design.md`.

---

## 9. Memory notes that constrain the writeup

These are auto-recorded facts that should shape later stages:

- **Phase F slicing status** — Phase F complete.
- **YACTT progress** — V1 + V2.x + V3 subgraph + V3 first + V3 method-bodies slice shipped.
- **Tool-layer method ID inconsistency** — resolved 2026-07-04; single-source `id.For` + per-receiver keying for call-edges.
- **Mockery pattern** — mockery v2.53.6; generated mocks with `EXPECT()` pattern; linter caveat.
- **Make check** — the single CI gate.
- **golangci-lint version** — v1.42.1 pins `interface{}` over `any`.
- **Tree-sitter binding imports** — grammar packages export Go bindings at `.../bindings/go`.
- **Invalidate test pitfalls** — macOS fsnotify `OpWrite`/`OpCreate` quirks + debug drainer race.
- **Smacker predicate quirks** — predicates go inside parens; `FilterPredicates` rejects via empty `Captures`; `NextMatch` reports empty captures when predicates present.
- **Issue #8 registry package** — `internal/registry` + 4 MCP tools; `yactt mcp serve` with no path = registry mode; cache priming gotcha for `index_repository`.

---

## 10. Things NOT in the codebase yet (worth flagging in later stages)

- **Python grammar** — listed in `design.md § Languages` but not yet wired in `internal/parser/language.go:All()`.
- **Multi-repo / cross-repo queries** — registry is single-process; cross-repo query deferred to Phase 2.
- **CodeQL-style dataflow** — not advertised; `find_code` is regex or tree-sitter AST patterns.
- **GraphQL internal fabric** — design doc says "internal query fabric" but no `internal/gql` package exists in code today.
- **Persisted-query step chaining** — mentioned as next on roadmap.
- **Consumer-side SLSA attestation verification** — today: SHA256 + TOFU; tomorrow: `gh attestation verify`.

---

## 11. Key questions for Stage 2 (Crystal Ball)

- What's the single thing yactt does that no one else does, framed as a *future* promise?
- Which deferred feature, if shipped next, would double the audience?
- Where does "federated" in the tagline actually pay off — and what does paying it off unlock?
- What's the natural Phase-2 story (Python + multi-repo query + verified provenance)?
- Does the README's current emphasis on tree-sitter-as-floor still hold post-Issue #11, where detect_changes ships a git-backed feature?

---

## Method summary

- **Graph:** `get_architecture` (4 aspects), `search_graph` (BM25: "main entry point", "tool registration"), `query_graph` (Cypher over `Package`/`File` — empty results; schema used instead), `get_graph_schema` (full label/edge catalogue).
- **Read fallback:** `README.md`, `cmd/yactt/main.go` (head 130 lines), `internal/registry/registry.go` (head 80 lines), `internal/tool/list_projects.go`, `docs/design.md` (full 1200 lines), `go.mod`, `wc -l` of every package file, directory listings.
- **Caveats:** Cypher query against `Package` label returned 0 rows; fell back to filename heuristics + Read. Graph likely uses `Folder` for package-level nodes.