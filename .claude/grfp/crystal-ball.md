# Crystal Ball — yactt

> Stage 2 of the GitHub README For Perfectionists workflow.
> Generated 2026-07-06 (refresh — pull from origin brought Issue #11 + search_code onto main).
>
> **Graph tools available:** Yes (1,858 nodes / 7,171 edges, `ready`).
> **Method:** `search_graph` (BM25 over symbol names), `query_graph` (Cypher); Cypher returns 0 rows for `complexity > N` and inbound/outbound-degree queries — `complexity` and `lines` properties are not populated for this project (cross-checked via `query_graph` returning empty strings for those keys). Fell back to file-level heuristics: `rg` for TODOs/FIXMEs/dead-code patterns, `wc -l` for function-count and LOC, `git log` for the commit arc.
> **Counterpart report:** `.claude/grfp/deep-dive.md`

---

## 1. Vision — where yactt sits in the ecosystem

The README's "Why" should anchor on this: **yactt is a Model Context Protocol server that gives AI agents the same fidelity a human reading the source would have, plus the resolved semantics a human can't easily get on demand.**

| Axis | Reference tools | yactt |
|---|---|---|
| Source fidelity (lossless ↔ lossy) | Sourcegraph/SCIP/LSIF (lossy for parsed AST, lossless for source files) | **Lossless** — every tool returns raw bytes via `node_source` and lossless ranges via `node_get` |
| Semantic depth (syntactic ↔ resolved) | CodeQL/Semgrep (resolved but narrow) | **Resolved when LSP available, syntactic floor otherwise** |
| Front-of-house protocol | Project-specific CLIs / HTTP / IDE plugins | **MCP** — `2024-11-05`, built around JSON-RPC 2.0 + `structuredContent` |
| Polyglot scope (Go/TS/JS/Python) | Each tool covers 1–2 languages well | **4 in design intent; 3 wired in code (Go/TS/JS); Python planned** |
| Tool count | 5–20 (typical) | **21 (16 code-intel + 4 registry + 1 persisted_query)** |
| Federation | Single-tool | **Registry at `$XDG_CACHE_HOME/yactt/projects.json` — discover/manage N repos from one binary** |

**The unique angle:** MCP-native + agent-first + single-binary federation. Other tools predate MCP; yactt is built around the JSON-RPC surface, the `OutputSchema` contract enforced at registration, and the pay-only-for-what-you-pull ergonomics that agents need. Two run modes from one binary (`mcp serve <path>` vs `mcp serve`) is something no competitor offers.

**Sources**: `docs/design.md § 1`; `cmd/yactt/main.go:registerAllTools`; `internal/mcp/server.go:validateOutputSchema`; `internal/registry/registry.go`.

---

## 2. The recent arc — what the commits say

A snapshot of `git log --oneline -20` reveals a clean feature-arc:

```
d31694c fix(tool): reject dash-prefixed refs to block git argv injection (detect_changes)
f137e77 feat(tool): detect_changes — git-ref diff impact (Issue #11)         ← just landed
cbcaecf feat(tool): search_code — dedup + rank find_code matches by enclosing symbol
7013333 feat(tool): query_graph — multi-hop graph traversal (Issue #9)
ef02c21 feat(registry): multi-repo MCP tools (Issue #8)
5ee6537 feat(tool): get_graph_schema, get_code_snippet, get_architecture (Issue #7)
f8889aa fix(ci): drop gitleaks, defer secret-scanning to platform
8bc5405 fix(ci): fetch full history for gitleaks PR diff
d7fbc4e fix(ci): gitleaks-action wants version without 'v' prefix
fb4c339 feat(security): umbrella docs + secret-scanning (Issue #4)
b53b4d2 feat(security): AST09 governance / audit mitigations (Issue #3)
1b750ea fix(security): AST05 mitigations
c541135 feat(security): AST02 supply-chain mitigations
```

**Three parallel arcs running in the last 10 commits:**

1. **Tool surface expansion** — 4 new tools in 5 commits (graph_schema, code_snippet, architecture, query_graph, search_code, detect_changes). Each one closes one Issue. *Methodical.*
2. **Security umbrella** — AST02 (supply chain), AST05 (doc-comment/identifier-name injection), AST09 (governance/audit), plus a security doc + secret-scanning. Four commits. *Defends the trust chain before the surface grows further.*
3. **CI plumbing** — secret-scanning, action SHA-pinning, module-cache. Plumbing the pipeline. *Housekeeping on the security arc.*

**What's NOT in the commit arc yet:** Python grammar wiring, multi-repo queries (registry exists but only the directory side; cross-repo graph queries deferred), persisted-query step chaining, CodeQL-style dataflow, "federated" actually paying off in queries (only manifests in indexing/storage, not in cross-repo traversal).

**Implication for the README roadmap:** the *visible* next milestone is the **Python grammar** (it sits at the top of every past roadmap). The *invisible* but more strategically interesting one is **multi-repo cross-graph queries** — that's what "federated" in the tagline is buying.

---

## 3. Dead code, deferred code, and complexity

### 3.1 — Confirmed dead code (confidence via grep)

**Graph Cypher did not return candidates for `inbound = 0` queries** — the property isn't populated for this project. Falling back to direct file inspection:

| Path | File | Line | Notes |
|---|---|---|---|
| `internal/mcp/server.go` | `validateOutputSchema` | 145 | **Not dead** — called at `NewServer`. Panics are appropriate in registration paths. |
| `internal/persisted/registry.go` | `panic(err)` | 72 | **Not dead** — boot-time fail-fast. |
| `internal/id/id.go` | `panic(err)` | 82 | **Not dead** — `id.For` is single-source; failure is unrecoverable. |
| `internal/lsp/internal/stubserver/main.go` | `os.Exit(0)` | 264 | **Not dead** — stubserver is a test fixture binary. |

**No `TODO`/`FIXME`/`XXX`/`HACK` markers anywhere in the codebase** (verified via `rg -n 'TODO\b|FIXME\b|XXX\b|HACK\b' --type=go`). Two stray "temporary" mentions in comments (resolver.go:53, index_repository.go:69) — both are about temporary data, not deferred code. **This is unusually clean for a project this size.**

### 3.2 — Fan-in / fan-out candidates (heuristic, not graph-confirmed)

Cannot confirm via `query_graph` (Cypher complexity/lines properties are empty strings for this project). Falling back to LOC ranking from `wc -l` of non-test Go files in `internal/`:

| File | Non-test funcs | LOC | Verdict |
|---|---|---|---|
| `internal/store/store.go` | 24 | 612 | **The heart.** Walk, parse, cache, LSP coordination. Largest concentrated responsibility. |
| `internal/tool/nodeedges.go` | 22 | **783** | **Largest tool.** Cross-reference resolution. Worth a future refactor. |
| `internal/store/node.go` | 18 | 476 | Symbol extraction. Core. |
| `internal/store/resolver.go` | 17 | 465 | LSP/edge resolution. Core. |
| `internal/architecture` (via tool) | 11 | 559 | Tarjan SCC + dead-code detection. Self-contained. |
| `internal/tool/detectchanges.go` | — | 484 | **New (Issue #11).** Git diff + per-hunk symbol resolution. |
| `internal/tool/querygraph.go` | — | 348 | **New (Issue #9).** Multi-hop BFS. |
| `internal/tool/findcode.go` | — | 368 | AST-aware + regex pattern search. |
| `internal/tool/searchcode.go` | — | **329** | **New.** Dedup + rank find_code matches. |
| `internal/lsp/client.go` | 16 | — | LSP subprocess wrapper. |

**nodeedges.go at 783 LOC is the largest single source of complexity in the tool layer.** It's the workhorse tool (callers/callees/tests/overrides/imports, tier-ranked fallback through SCIP→LSP→tree-sitter). A future refactor candidate — break it into per-edge-kind resolvers.

### 3.3 — Concurrency surface

Concurrency primitives appear in: `internal/store/store.go`, `internal/registry/registry.go`, `internal/lsp/internal/stubserver/main.go`, `internal/persisted/registry.go`, `internal/audit/audit.go`, `internal/lsp/client.go`, `internal/mcp/server.go`, `internal/cache/cache.go`. **Eight files, all backend plumbing.** No concurrency in `internal/tool/` — the tool handlers are pure functions over a `*store.Repo`. *Good separation.*

### 3.4 — Hard-coded secrets / supply chain

`go.mod` requires **one** external dep, pinned to a literal commit hash (`v0.0.0-20240827094217-dd81d9e9be82`). All three grammar bindings (`golang`, `javascript`, `typescript/typescript`) live as subpackages of the same module — same commit pin. The dep surface is the security story. `docs/security.md` documents the trust chain.

---

## 4. Where the ecosystem is moving (opportunities for the README)

### 4.1 — The "federated" promise has not been paid out

The tagline says *federated* code intelligence. The code today:

| Federation | Status |
|---|---|
| Multiple repos indexed | ✅ Shipped (Issue #8) |
| Switch between repos | ✅ Shipped (`list_projects` + `mcp serve <path>`) |
| **Cross-repo graph queries** | ❌ Not yet — each `Repo` is still a single root |
| **Shared SCIP-style workspace index** | ❌ Not yet |
| **`workspace_overview` tool** | ❌ Not yet (designed in `docs/design.md § 9.C`) |

The README should under-promise on "federated" until the cross-repo graph query ships. Or, better — reframe the tagline to lean on what's actually shipped (multi-repo registry + discover-then-drill) and add federation as the explicit next milestone.

### 4.2 — Python is the single biggest audience unlock

`docs/design.md` lists Python in scope; the design doc actually says "4 languages: Go, TS, JS, Python" in §6. But the parser module today wires only Go, TS, JS. The Python grammar is the most-predicted, most-overdue single feature in the public roadmap. Shipping it would:

- Triple the audience (Python's MCP ecosystem is loudest)
- Validate the design's plug-in language model (grammar + optional `python-lsp-server`)
- Make the empty-quadrant claim polyglot-complete for the dominant AI-agent language

**Strong recommendation:** make Python the visible "next" milestone in the README. The design doc has been saying it for the entire project lifetime.

### 4.3 — Persisted-query step chaining is a quiet wedge

`internal/persisted/` ships a registry with one op (`onboarding`). The design doc describes "step chaining" — a persisted op that calls a sequence of tools. This is the missing piece for the **PR review workflow**, the **codebase health check**, the **onboarding tour** — every recurring agent pattern. The infrastructure is 90% there. The README could position persisted_query as a *workflow composition primitive*, not just a registry lookup.

### 4.4 — Consumer-side provenance verification

Today the install hook does SHA256 + TOFU. Tomorrow: `gh attestation verify` runs at SessionStart. That's a hard upgrade — turn a TOFU chain into a verified provenance chain, per release. The infrastructure (SLSA L3 attestations) is already in place; the consumer-side check is the only missing piece. This would make yactt the first MCP server to ship *end-to-end* provenance, install to host.

### 4.5 — What the "detect_changes" tool unlocks for the README story

Issue #11 (`detect_changes`) is the newest tool. It turns the MCP into a **PR review engine**:

```
> detect_changes {"base": "main", "head": "feat/fix-11"}
  → changed files + per-hunk symbol resolution + per-symbol callers/tests/overrides
```

Combined with `find_referencing_symbols` and `edit_impact`, the agent gets a complete **change-impact analysis** in 2-3 calls. The README should showcase this as the "AI-driven code review" use case — it's the most concrete value to a non-MCP-fluent reader.

---

## 5. Audience segments to call out in the README

| Segment | What yactt gives them | The pitch |
|---|---|---|
| **AI agent authors** (building on MCP) | 21 tools over one binary, structured output schemas, dep-free runtime | "The code-intelligence back end for your MCP-native agent" |
| **Claude Code users** | One-command install + `code-explore` skill that teaches the agent the right tool order | "Your agent gets a code model — install in 30 seconds" |
| **Platform / SRE teams** | Read-only MCP surface, `AllowedRoots` constraints, audit log | "Give your agents repo access without giving them repo write access" |
| **Monorepo maintainers** | Multi-repo registry, `index_repository` to prime cache for teammates | "Stop every teammate re-parsing the monorepo at startup" |
| **Polyglot Go/TS/JS shops** | tree-sitter for all three, opportunistic LSP, provenance fallback | "One server, three languages, zero language-server failures" |

The README currently leans on segment 1 + 2 (the agent author and the Claude Code user). Segments 3-5 are **underexplored copy**.

---

## 6. Roadmap candidates (ranked by leverage)

For the "Status & roadmap" section of the README, the highest-leverage items are:

1. **Python grammar** — single biggest audience unlock; design has it for the entire project; grammar binding is just a `go get` away.
2. **Multi-repo cross-graph queries** — pays out the tagline. `query_graph` already supports the multi-hop primitive; extend it across repos.
3. **Persisted-query step chaining** — turns `persisted_query` from a one-op registry into the agent's workflow composition primitive.
4. **`workspace_overview` tool** — the natural complement to `tree_overview` once multi-repo queries exist.
5. **Consumer-side SLSA attestation verification in SessionStart hook** — TOFU → verified provenance. The single most-trust-positive change in the project.
6. **`tree_at(ref)` for git-time-travel** — the "what did this function look like 3 months ago" answer. Low cost; high agent value.
7. **CodeQL-style `dataflow` layer** — out of scope for MVP; a Phase 2 candidate. Mention it as parked.

**For the public README:** items 1, 2, 5 are the most defensible "next" claims. Item 3 is a quiet differentiator. Items 4, 6, 7 belong in a longer-form roadmap doc, not the README.

---

## 7. README positioning bets

Three framings worth testing in Stage 3 (Brain Jam):

**Bet A — "The missing quadrant."**
Lead with the empty-quadrant chart. Frame yactt as the only tool in the lossless + resolved quadrant for polyglot Go/TS/JS. Pro: clear differentiation, defensible. Con: dense for a first-time reader.

**Bet B — "An agent's code model."**
Lead with the worked example: agent enters a repo, makes 5 MCP calls, gets a complete picture. Frame yactt as the agent's mental model of the codebase. Pro: shows value in 30 seconds. Con: requires the reader to care about agents.

**Bet C — "MCP-native code intelligence."**
Lead with the protocol. Frame yactt as the canonical example of a serious MCP server: structured `OutputSchema`, `structuredContent`, curated workflows. Pro: positions the project as a flagship MCP example. Con: technical, may not land for non-MCP readers.

**Most likely right answer:** lead with Bet A (the empty quadrant) for the first 2 paragraphs, then flow into Bet B's worked example, then bet C's protocol details lower down. The reader gets the positioning, the value, and the technical moat in that order.

---

## Method

- **Graph tools used:** `search_graph` (BM25), `query_graph` (Cypher — returned 0 rows for `complexity > N` and inbound/outbound-degree queries; the `complexity` / `lines` properties are empty strings for this project's index, confirmed by `query_graph` returning blank values); `get_graph_schema` (label/edge catalogue).
- **Read fallback:** `rg` for TODO/FIXME/panic/exit patterns, `wc -l` for function-count + LOC ranking, `git log` for the commit arc, `docs/security.md` for trust-chain context, directory listings for the LSP / persisted / cache packages.
- **Caveats:** the graph index for yactt has a sparse `complexity`/`lines` population. To get a fuller dead-code / hotspot view, the next index pass should re-run with the moderate or full mode (it may have indexed in `fast` mode only). All fan-in/fan-out claims here are heuristic from LOC + function count, not graph-confirmed.
