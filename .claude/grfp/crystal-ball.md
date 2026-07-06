# Crystal Ball — yactt

> Stage 2 of the GitHub README For Perfectionists workflow.
> Generated 2026-07-05.

**Graph tools available:** Yes (codebase-memory-mcp, 1432 nodes / 5158 edges).
**Method:** graph-primary; `trace_path` / `get_code_snippet` / `search_graph` with `max_degree` filter for dead-code candidates; Cypher queries on `complexity > N` and `is_test = false` returned empty (likely strict null-handling in the graph query layer — cross-checked via `search_graph` plus per-candidate snippet reads).
**Project ID:** `Users-kellen-Projects-yactt`
**Counterpart report:** `.claude/grfp/deep-dive.md`

---

## 1. Vision: where yactt sits in the ecosystem

The README's "Why" should anchor on this: **yactt is a Model Context Protocol server that gives AI agents the same fidelity a human reading the source would have, plus the resolved semantics a human can't easily get on demand.** Position-wise:

| Axis | Reference tools | yactt |
|---|---|---|
| Source fidelity (lossless ↔ lossy) | Sourcegraph/SCIP/LSIF (lossy for parsed AST, lossless for source files) | **Lossless** — every tool returns the raw bytes via `node_source` and the lossless range via `node_get` |
| Semantic depth (syntactic ↔ resolved) | CodeQL/Semgrep (resolved but narrow) | **Resolved when an LSP server is available, syntactic floor otherwise** |
| Front-of-house protocol | Project-specific CLIs / HTTP / IDE plugins | **MCP** (one of the first serious code-intelligence MCP servers shipping to production) |
| Polyglot scope (Go/TS/JS/Python) | Each tool covers 1–2 languages well; the four-language gap is the empty quadrant | **All four in design intent; Go/TS/JS in code today** |

The unique angle is the **MCP-native and agent-first design**. Other tools predate MCP; yactt is built around the JSON-RPC 2.0 surface, the `structuredContent` contract, and the 5-minute-max / pay-only-for-what-you-pull ergonomics that agents need. Curated workflows via `persisted_query` make the agent's first turn cheap.

**Source**: `docs/design.md` § 1; `cmd/yactt/main.go:registerAllTools`; `internal/mcp/server.go:validateOutputSchema`.

---

## 2. Audience segments (underexplored in current docs)

The current documentation surfaces two: AI agents and Claude Code users. There are at least four more worth mentioning:

1. **AI agents via stdio MCP** (primary) — any MCP-capable client (Claude Desktop, Cursor, Continue.dev, custom).
2. **Claude Code users** — via the bundled plugin (`plugins/yactt/`). The install story is the SLSA-signed release tarball.
3. **Programmatic users** — `yactt overview <path>` for shell pipelines or watch scripts; the JSON tree is intentionally machine-friendly.
4. **CI / batch pipelines** — same `mcp serve` mode can be driven by any JSON-RPC client (the protocol is open).
5. **Reverse direction — "for LSP server authors"** — the project ships a `stubserver` (in `internal/lsp/internal/stubserver/`) that fakes an LSP server for tests. That's a bonus for anyone testing their own LSP client in Go, though it's positioned as an internal test fixture.

**Source**: observed through `cmd/yactt/main.go`, `internal/lsp/internal/stubserver/main.go:1–8` (well-documented test fixture).

---

## 3. Dead-code analysis

### Findings list

| Symbol | File | Apparent dead? | Verified status | Confidence |
|---|---|---|---|---|
| `ByteRangeFromRows` | `internal/store/edges.go` | yes (in_degree=0) | Used in `internal/tool/nodeedges.go` — graph CALLS edge missing across package | **Low confidence** (graph gap) |
| `WalkExpr` | `internal/store/edges.go` | yes | Used in tests + `nodeedges.go` for iterating an AST | **Low confidence** (graph gap) |
| `PackagePath` | `internal/store/store.go` | yes | Public single-source-of-truth; called by import resolver | **Low confidence** (graph gap) |
| `WithDiskCache` | `internal/store/store.go` | yes | Used in `cmd/yactt/main.go:loadOptsWithDiskCache` | **Low confidence** (graph gap — option-constructor wrappers) |
| `WithDiskCacheMaxBytes` | `internal/store/store.go` | yes | Same as above | **Low confidence** |
| `WithMaxFiles` | `internal/store/store.go` | yes | Documented as a public option; used in tests (≥6 unit tests per count) | **Low confidence** |
| `NewSession` (test fixture) | `tests/fixtures/sample-go/auth/session.go` | yes | Test fixture data — deliberately a leaf | **High confidence it's a fixture**, zero confidence "should be removed" |

**Interpretation**: The graph's CALLS edges miss indirect callsites where a function is passed as an argument (`func(...)...` wrappers, struct-field literals, slice-element constructors like `[]store.LoadOption{store.WithDiskCache(dir)}`). The empty `trace_path` results for these confirm the gap rather than missing callers. **No genuine dead code surfaced.**

### Caveat
The Cypher filter `MATCH (f:Function) WHERE f.is_test = false AND f.in_degree = 0 RETURN f.qualified_name` returned **zero** candidates, which is consistent with the finding above: no production-only function is genuinely orphaned; the suspects are all reachable via paths the CALLS edge extractor misses.

**Recommendation for the README**: skip "dead code" as a topic entirely — it's not a story.

---

## 4. Complexity hotspots

### Production code (high complexity)

| Function | File | Complexity | Notes |
|---|---|---|---|
| `Repo.Load` | `internal/store/store.go:136–371` | (large) | **The** orchestration function — 612 lines combining walk + parse + extract + cache + LSP startup + warm-up. Splitting it further would ease reading and testing. |
| `Repo.Close` | `internal/store/store.go` | (low) | Iterates the `lsp` map; not high complexity per se but warrants the write-lock contract noted in its docstring. |
| The 10 tool handlers in `internal/tool/*.go` | various | ≤5 each | The "parse, don't validate" architecture keeps each handler small. |

### Test fixtures (very high complexity, intentionally)

| Function | File | Complexity | Notes |
|---|---|---|---|
| `internal/lsp/internal/stubserver.main.main` | `internal/lsp/internal/stubserver/main.go:68–287` | **29** | 220-line single function with 13 flags for fixture behavior. **By design** — every flag is a documented test scenario (failure, sleep, prose hover, no-shutdown, hang-forever, etc.). Not a refactor target. |

### Call-graph hot spots (high fan-in — most-called)

| Function | Fan-in | Why |
|---|---|---|
| `internal/source.source.LoadFile` | **45** | Every file in every loaded repo flows through here; this is the parse chokepoint |
| `internal/store.repofixture.repofixture.New` | **39** | All repo tests go through the fixture builder |
| `internal/parser.symbols.ExtractSymbols` | **26** | One parse = one symbols extraction, called once per file |
| `internal/id.id.Parse` / `internal/id.id.Function` | **19–21** | Stable ID generation is on the hot path |
| `internal/cache.disk.NewDiskCache` | **15** | Cache construction per repo |
| `internal/cache.cache.NewSized` | **14** | In-memory cache constructor |
| `internal/persisted.registry.NewRegistry` | **12** | Registry constructor |
| `internal/parser.language.Detect` | **11** | Per-file language detection |

### Take-away for the README

`store.Load` is the function most worth flagging in a "How it works" diagram. It's the entropy sink: parsing, caching, indexing, LSP startup, workspace warm-up, all in one place, and the place where any future "I/O-threaded" or "incremental reload" work would land.

**Source**: `internal/store/store.go:136`; `internal/lsp/internal/stubserver/main.go:68`.

---

## 5. TODO / FIXME / deprecated scan

`search_code` for `TODO|FIXME|XXX|HACK` and `deprecated` against `*.go` returned **zero matches**. The codebase looks clean of conventional debt markers.

The only multi-step workflow scope note ("MVP scope: one tool call per op. Each Op records the tool name and a static args map; the runner dispatches the wrapped call and returns its result. Step chaining and parameter forwarding are deliberate follow-ups…") is in a package-level doc comment on `internal/persisted/registry.go`, not a marker comment. **Worth surfacing in the README's roadmap.**

**Source**: `internal/persisted/registry.go:8–14`.

---

## 6. Roadmap candidates (visible in code or design today)

Items the project is *already pointed at* — no speculation, just carried-over TODOs and un-wired-up surfaces.

| Candidate | Location | Maturity |
|---|---|---|
| **Python grammar** | `docs/design.md` § "Languages" lists Python; `internal/parser/language.go:All()` does not wire a Python `Language` | Design ready; code absent |
| **Persisted-query step chaining & template syntax** | `internal/persisted/registry.go:8–14` (package doc) | Explicit follow-up called out |
| **Multi-repo / federated query** | `docs/design.md` tags MVP as single-repo; the project name "Federated" awaits | Future |
| **GraphQL internal query fabric** | `docs/design.md` says MCP is the surface, GraphQL the internal query fabric — no `internal/gql` package exists yet | Future |
| **`gh attestation verify` in the bootstrap** | `plugins/yactt/README.md` notes "the bootstrap itself still only does corruption-detection + TOFU today — see `install.sh`'s comments" | Tracked |
| **Consumer-side provenance verification** | Same plugin README section ("follow-up work") | Tracked |
| **Per-account locks if cache contention hits** | Implicit (per-repo subdir already exists); no observed pressure | Watch |
| **MCP-2025 spec upgrade** | Current server hard-codes protocol `2024-11-05`; newer drafts exist | Watch |

### Already shipped (worth mentioning in README's "Status" line)

- V1 + V2.x + **V3 subgraph slice + V3 first slice + V3 method-bodies slice** (per `mem:yactt-progress`)
- Phase F complete (per `mem:phase-f-slicing`)
- Single-source-of-truth `id.For` + per-receiver keying (per `mem:tool-layer-method-id-inconsistency`, resolved 2026-07-04)
- SLSA Build Provenance Level 3 (per `plugins/yactt/README.md`)

---

## 7. Where to play in the README

Given the deep-dive findings + this crystal-ball view, the README's standout angles should be:

1. **The headline**: "Federated code intelligence for AI agents — lossless source, resolved semantics, MCP-native." Avoid jargon dump.
2. **The install paths** (three of them, by audience):
   - Claude Code users → install the bundled plugin (no Go needed).
   - AI agent authors → `yactt mcp serve` over stdio.
   - Shell users → `yactt overview <path>`.
3. **The 10 tools table** (a quick reference card).
4. **How it works** — short paragraph pointing at the architecture diagram. Stay shallow; the design doc is the deep dive.
5. **The autonomy corner**: tree-sitter as floor, LSP as depth, parse-don't-validate as the wire contract.
6. **Status & roadmap snippet** — what's shipped, what's next.
7. **Trust**: dual license + SLSA L3 attestations + `gh attestation verify` one-liner.

---

## 8. Trust / risk flags worth a README mention

- **Permissions**: The MCP server has read-only file access by design (no write tools registered; `edit_impact` is analysis-only). Worth stating.
- **Network**: `mcp serve` makes no outbound calls except to spawn `gopls` / `typescript-language-server` as child processes, which themselves may fetch packages from language registries. No telemetry.
- **Resource caps**: `MaxFiles` defaults to 50 000; `LSP startup` is bounded at 15 s; per-file workspace warm-up at 5 s. These are safety valves and should be in a "Limits" subsection.
- **gopls / ts-server dependency**: Not bundled. If absent, the load logs a one-line warning and everything still works via tree-sitter with `Provenance.Tool = "tree-sitter"`.

**Sources**: `internal/store/store.go:24–34`, `:267–278`, `:281–313`; `plugins/yactt/README.md`.

---

## Ready for Stage 3

Brain Jam next — collaborate with the model on tone, voice, marketing angle. The crystal-ball view + deep-dive should give the chorus a stable foundation: **what** yactt is, **who** it serves, **how** it's built, **what's next**.

Proceeding to **Stage 3: Brain Jam**?