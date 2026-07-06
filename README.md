# yactt

**Federated code intelligence for AI agents — lossless source, resolved semantics, MCP-native.**

*Walk the tree, choose your layer.*

yactt is a Model Context Protocol server that gives an AI agent both the raw bytes of a source file **and** the resolved symbol/call/reference graph that a human couldn't easily get on demand. It targets the empty quadrant of the code-intelligence tradeoff: lossless source plus semantic depth, in polyglot Go, TypeScript, and JavaScript repositories.

> *Yet Another Code Tree Tool.* Self-aware in the GNU / YACC / WINE tradition — the name is tongue-in-cheek; the tool is serious.

---

## Why yactt

The reference tools sit on a strict diagonal in the polyglot zone:

| | Lossless source | Lossy summary |
|---|---|---|
| **Resolved semantics** | *(empty quadrant)* | CodeQL, Semgrep, Sourcegraph/SCIP |
| **Syntactic only** | `cat`, file readers | `grep`, ripgrep |

yactt occupies the empty corner. It uses [tree-sitter](https://tree-sitter.github.io/) as the unconditional syntactic floor and opportunistically attaches [`gopls`](https://pkg.go.dev/golang.org/x/tools/gopls) and [`typescript-language-server`](https://github.com/typescript-language-server/typescript-language-server) for resolved type/call/reference data when those servers are available on `PATH`. Without them, yactt still works — it just stamps `Provenance.Tool = "tree-sitter"` on every answer.

The wire surface is [MCP](https://modelcontextprotocol.org/), not a custom protocol. Every tool declares both an `InputSchema` and an `OutputSchema` (the `OutputSchema` must declare `type:"object"` — enforced at registration time), so an agent gets structured results it can branch on without parsing prose.

---

## Install

Three install paths, organized by who you are.

### For Claude Code users

The bundled plugin installs on first use. The `SessionStart` hook downloads the matched binary from the latest GitHub release, verifies its SHA256 against the published `SHA256SUMS`, and installs to `$XDG_HOME/bin/yactt`. No Go toolchain required.

```bash
/plugin marketplace add kellenff/yactt
/plugin install yactt@yactt
```

Requires `jq` (standard on macOS/Linux developer machines; `brew install jq` / `apt install jq` otherwise).

See [plugins/yactt/README.md](plugins/yactt/README.md) for the full install story, including the **SLSA Build Provenance Level 3** attestations verifiable with `gh attestation verify`.

### For AI agent authors

Any MCP-capable client. Wire the server into your `.mcp.json`:

```json
{
  "mcpServers": {
    "yactt": {
      "type": "stdio",
      "command": "yactt",
      "args": ["mcp", "serve", "/path/to/repo"]
    }
  }
}
```

The protocol version is `2024-11-05`. After `initialize` + `notifications/initialized`, call `tools/list` to enumerate the registered tools, then `tools/call` per request.

### For shell pipelines

Download a binary tarball, or build from source:

```bash
# macOS arm64 example
curl -fsSL https://github.com/kellenff/yactt/releases/latest/download/yactt_darwin_arm64.tar.gz \
  | tar -xz -C /usr/local/bin yactt_darwin_arm64 \
  && mv /usr/local/bin/yactt_darwin_arm64 /usr/local/bin/yactt

# then
yactt overview /path/to/repo        # tree dump as JSON
yactt mcp serve [/path/to/repo]     # MCP server on stdio
```

The CLI is intentionally thin — `help`, `version`, `overview`, `mcp serve`. Anything with logic lives under `internal/`.

---

## How it works

```
┌──────────────────────────────────────────────────────────┐
│  MCP client (agent)                                      │
└─────────────────────────┬────────────────────────────────┘
                          │ JSON-RPC 2.0 over stdio
                          ▼
┌──────────────────────────────────────────────────────────┐
│  yactt CLI  (cmd/yactt/main.go)                          │
│  ─ loads repo, wires 19 tools, runs server               │
└─────────────────────────┬────────────────────────────────┘
                          │
        ┌─────────────────┼─────────────────┐
        ▼                 ▼                 ▼
┌───────────────┐ ┌──────────────┐ ┌───────────────────┐
│ internal/mcp  │ │ internal/tool│ │ internal/persisted│
│ JSON-RPC      │ │ 19 handlers  │ │ curated-workflow  │
│ server        │ │ + schemas    │ │ registry (Phase 1.5)│
└───────┬───────┘ └──────┬───────┘ └─────────┬─────────┘
        └─────────────────┼─────────────────┘
                          ▼
                ┌───────────────────┐
                │ internal/store    │  per-repo orchestration
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

`store.Load(root)` walks the repo, parses every supported file with tree-sitter, extracts symbols, populates an in-memory file index and a per-repo disk cache (`$XDG_CACHE_HOME/yactt/<sha256(root)[:16]>`), and opportunistically starts `gopls` / `typescript-language-server` against the workspace. Failed language servers fall through to tree-sitter with the provenance marker.

The hard safety valves:

- `MaxFiles` cap (default 50 000) aborts the walk on overflow; partial repo still returned.
- LSP startup bounded at 15 s overall; per-file workspace warm-up at 5 s.
- Disk cache bounded at 512 MiB by default; override with `YACTT_DISK_CACHE_MAX_BYTES`.

See [docs/design.md](docs/design.md) for the full architectural rationale.

---

## The 19 tools

The codebase is one node graph; the tools are 19 facets of access.

Fourteen tools operate on a single loaded repo:

| Tool | Purpose |
|---|---|
| `tree_overview` | Top of the repo tree, depth-limited. Start here. |
| `node_get` | One or more layers of a node — `summary`, `signature`, `body`, `source`, `tokens`. |
| `node_source` | Lossless source for a node, optionally line-bounded. |
| `node_edges` | Cross-references — `callers`, `callees`, `tests`, `overrides`, `imports`. |
| `search` | Find symbols by name or doc-comment matching. |
| `find_symbol` | Locate by qualified name path with glob support (e.g. `internal/store/*/Load`). |
| `get_symbols_overview` | Top-level outline of a single file. |
| `find_code` | AST-aware (tree-sitter) or regex pattern search across files. |
| `find_referencing_symbols` | All references to a given symbol. |
| `edit_impact` | Analyse the blast radius of a proposed set of renames. **Does not apply them.** |
| `get_graph_schema` | Canonical `nodeKinds`, `edgeKinds`, `layers`. Use to write graph queries without hardcoding. |
| `get_code_snippet` | Source slice for a symbol by stable `id` OR qualified `name_path`. One call replaces `find_symbol` + `node_source`. |
| `get_architecture` | Structural summary: languages, packages, hotspots, dead-code candidates, import cycles. |
| `query_graph` | Multi-hop traversal — chain edge kinds across hops with `follow`, cap with `depth`/`limit`, filter by `kind`. |

A fifteenth tool, `persisted_query`, runs registered curated workflows by id. Out of the box it ships one op: `onboarding` (a one-shot `tree_overview` at depth 2 — the smallest useful workflow).

Four tools manage the multi-repo registry — they work whether or not the server has a repo loaded:

| Tool | Purpose |
|---|---|
| `list_projects` | Enumerate every indexed repo (sorted by path). |
| `index_repository` | Walk a repo at `path`, write an entry to the registry, prime its disk cache. |
| `index_status` | Registry row + per-repo cache freshness for `path`. `cacheFresh=false` means re-running `index_repository` would write new bytes. |
| `delete_project` | Evict `path` from the registry and remove its per-repo cache directory. Idempotent. |

## Federated code intelligence

`yactt` ships a multi-repo registry at `$XDG_CACHE_HOME/yactt/projects.json` (or `$HOME/.cache/yactt/projects.json` when `XDG_CACHE_HOME` is unset). The four registry tools above operate against that file; the 14 code-intelligence tools stay bound to whatever repo `yactt mcp serve <path>` loaded at startup.

Two run modes:

- `yactt mcp serve <path>` — single-repo mode. Loads `<path>`, exposes all 19 tools (14 code-intel + 4 registry + `persisted_query`).
- `yactt mcp serve` (no path) — registry mode. Exposes the 4 registry tools + `persisted_query`. Use this to discover or manage which repos are indexed before drilling into one.

Indexing is decoupled from serving: an agent in registry mode can call `index_repository` to prime a repo's cache, then a separate `yactt mcp serve <that-path>` can serve it with warm caches and zero re-parse.

Example flow in registry mode:

```text
> list_projects                          # empty
> index_repository {"path": "/code/svc-a"}
> list_projects                          # one entry
> index_status    {"path": "/code/svc-a"} # cacheFresh: true
> delete_project  {"path": "/code/svc-a"} # gone
```

The on-disk cache layout is unchanged from the single-repo flow — `$XDG_CACHE_HOME/yactt/<root-hash>/` still holds per-file parsed entries, so a registry-indexed repo serves identically to one you ran `yactt mcp serve` against directly.

---

## Status & roadmap

Shipped:

- V1, V2.x, V3 subgraph slice, V3 first slice, V3 method-bodies slice
- Phase F (per-repo provenance + edge model)
- Single-source `id.For` + per-receiver keying for call-edges (resolved 2026-07-04)
- SLSA Build Provenance Level 3 attestations on every release

Next:

- Python grammar (tree-sitter wiring, then `python-lsp-server` if available)
- Persisted-query step chaining and parameter forwarding
- Multi-repo / federated query (the "federated" in the tagline awaits)
- Consumer-side provenance verification inside the SessionStart bootstrap (today: SHA256 + TOFU; tomorrow: `gh attestation verify`)

---

## Trust

- **Dual-licensed**: [Apache-2.0](LICENSE-APACHE) or [MIT](LICENSE-MIT).
- **SLSA Build Provenance Level 3**. Verify any release tarball:

  ```bash
  gh attestation verify yactt_darwin_arm64.tar.gz -R kellenff/yactt
  ```

- **Read-only by design**. The MCP surface exposes no write tools. `edit_impact` analyses renames; it does not apply them. `yactt mcp serve` makes no outbound network calls except to spawn `gopls` / `typescript-language-server` as child processes, which themselves may fetch from language registries. No telemetry.
- **Tree-sitter as the unconditional floor.** `yactt mcp serve` works without `gopls` or `typescript-language-server` installed; every answer is then stamped `Provenance.Tool = "tree-sitter"` so an agent can branch on the provenance it trusts.
- **Bounded resources.** `MaxFiles` defaults to 50 000; disk cache defaults to 512 MiB; LSP startup is bounded at 15 s.

---

## Security

yactt reads untrusted source code on every MCP call. Full threat model,
install-hook trust chain, and mitigations for the OWASP Agentic Skills
Top 10 findings:

- **AST02 supply chain** — `go mod verify`, no-`replace` grep, `govulncheck`, SHA256SUMS + SLSA L3 attestation on releases.
- **AST03 over-privileged access** — closed by design: read-only MCP surface, `AllowedRoots` constraint on every path-bearing tool, `MaxFiles` cap (50 000). No tracking issue — the surface is constrained at every egress.
- **AST05 doc-comment / identifier-name injection (mitigated — Issue #2)** — gates prose behind an opt-in `docs` layer and sanitizes identifier names at egress.
- **AST09 governance / audit (mitigated — Issue #3)** — one structured JSON startup line on stderr (binary SHA-256, resolved root, MaxFiles cap, grammars, LSP status) + an opt-in `--audit-log=<file>` line per `tools/call` (tool, paths, output bytes, duration), plus a runtime check against the install hook's TOFU record.

Full threat model, install-hook trust chain, and mitigations live in
[docs/security.md](docs/security.md).

If you point yactt at a repo you do not fully trust, treat the `source`,
`tokens`, and `docs` layers as untrusted data. The structured layers
(`signature`, `body`, `summary`) carry no attacker-authored prose and can
be trusted as descriptions of structure.

### Opt-in audit log

```bash
yactt mcp serve --audit-log=/var/log/yactt/audit.log
```

One JSON line per MCP tool invocation:

```json
{"event":"tool_call","timestamp":"2026-07-05T12:34:56Z","tool":"node_get","input_paths":["/Users/kellen/proj/foo.go"],"output_bytes":1024,"duration_ms":12,"is_error":false}
```

The startup line is always written to stderr (even without `--audit-log`),
so a host can cross-check the running binary's SHA-256 against its
expected release.

---

## Read more

- Architecture deep dive — [docs/design.md](docs/design.md)
- Security & threat model — [docs/security.md](docs/security.md)
- Claude Code plugin story — [plugins/yactt/README.md](plugins/yactt/README.md)
- Latest release — [github.com/kellenff/yactt/releases/latest](https://github.com/kellenff/yactt/releases/latest)
- `code-explore` skill — [plugins/yactt/skills/code-explore/SKILL.md](plugins/yactt/skills/code-explore/SKILL.md)