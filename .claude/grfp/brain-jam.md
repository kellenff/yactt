# Brain Jam — yactt

> Stage 3 of the GitHub README For Perfectionists workflow.
> Generated 2026-07-06.
>
> **Mode:** Multi-model chorus — default cast (claude-synth + pragmatist, both minimax/MiniMax-M3) + critic, 3 rounds, critique enabled.
> **Cast lenses:** synth (positioning strategy, optimistic-but-rigorous), pragmatist (delivery-focused, sharpens prose), critic (unavailable in this run — recorded as `unavailable` in the transcript).
> **Transcript:** `.brainstorm/chorus-20260706T132250.json`
> **Counterpart reports:** `.claude/grfp/deep-dive.md`, `.claude/grfp/crystal-ball.md`.

---

## 1. Seed

Lead with the empty quadrant. The chart is the hook — it positions yactt in the only spot in the tradeoff space that's empty for polyglot Go/TS/JS. Then flow into the 5-call worked example to show value. The stand-out move: a one-line install-trust chain badge that makes SLSA L3 visible at the README's first scroll.

---

## 2. The jam — three rounds, one resolution

### Round 1 — the chart problem

**Pragmatist's pushback:** A 2D tradeoff scatter is brilliant for a blog post or conference talk — not for a README. The reader is a developer scanning for "should I install this?" The chart takes cognitive load before delivering payoff. The defensibility is real; the conversion is weak.

**Inversion proposal:** Lead with the **5-call worked example**. Show an agent asking a question and getting symbol, type, callers, definition site, and provenance in one short transcript. *Then* drop the chart, captioned: "Here's where yactt sits in the landscape." Now the chart explains why the demo was so good, instead of asking the reader to decode a diagram before seeing value.

**On the trust badge:** "SLSA L3 • 21 tools • 1 dep • read-only" — four numbers, scannable, each one a different objection closed. **The move no competitor README makes: pre-answering trust questions in a single horizontal line above the fold.**

**Synth's commit — audience forking:** Three reader types land on a yactt README: (1) the **agent-builder** wiring MCP into Claude/Cursor/custom runtime — wants "does this give my agent accurate code understanding with one `go install`?"; (2) the **platform engineer** — wants "is this safe to pin in prod?"; (3) the **curious developer** — wants "what's interesting here?". The README converts type 1 first, doesn't alienate type 2, and type 3 finds the interesting parts on their own. The chart serves type 2/3. The demo serves type 1. **Type 1 wins the opening.**

**The asymmetry competitors miss:** ripgrep, ctags, scip-cpp, scip/lsif — all lead with the tool or the protocol. **None lead with "here's why you should believe what I'm about to show you."** That inversion is the positioning bet. The trust strip isn't a feature callout; it's a reframe of what a code-intel README is for.

### Round 2 — hardening the demo

**Pragmatist's three concrete edits:**

1. **Demo prompt:** vague question that returns *multi-hit ambiguity*, with the disambiguation as the visible beat. If the demo's first call returns exactly one hit, you're showing search, not resolution.
2. **Receipts:** inline anchors from the strip, full section between tools and install — "What's behind the badge row."
3. **Provenance:** at least one call in the demo carries an LSP provenance stamp, labeled in the caption. Otherwise the quadrant chart is unsubstantiated by the transcript.

**Synth's draft transcript (verbatim, lightly edited):**

> **What an agent actually gets from a codebase**
>
> Agent prompt: *"Where is payment validation handled, and who calls it?"*
>
> ```jsonc
> // call 1 — find by name
> {"tool": "find_symbol", "args": {"name": "validatePayment"}}
> ```
> → 3 hits across 2 source files + 1 test file. Agent picks the validator.go method.
>
> ```jsonc
> // call 2 — definition lookup crosses into the interface
> {"tool": "get_definitions", "args": {"symbol_ref": "validator.go:14"}}
> ```
> → resolves to interface declaration in a third file. **`provenance: "gopls"`** ← LSP-backed, distinct from calls 1/4/5.
>
> ```jsonc
> // call 3 — type signature for the interface
> {"tool": "get_symbols", "args": {"file": "internal/billing/billing.go", "kind": "interface"}}
> ```
> → type signature, fields, embedded types, rendered once.
>
> ```jsonc
> // call 4 — who calls validatePayment in production?
> {"tool": "get_callers", "args": {"symbol_ref": "validator.go:14"}}
> ```
> → 2 production callers, tests filtered.
>
> ```jsonc
> // call 5 — source snippet for human-in-the-loop confirmation
> {"tool": "get_snippet", "args": {"file": "internal/billing/validator.go", "line": 14, "lines": 12}}
> ```
> → 12-line snippet, no test boilerplate.
>
> **Caption:** *call 2 used gopls; calls 1, 3, 4, 5 used tree-sitter with cross-reference resolution against the parsed AST. yactt stamps every response with its provenance so the agent — and you — can tell which path served the answer.*

### Round 3 — one structural critique, then green-light

**Pragmatist's critique:** Call 3 (`get_symbols`) is described in prose; every other call has a concrete JSON block. The visual contract is broken at the one call that proves the agent gets *type shape*. Two fixes:

- **(a)** Render call 3 output as JSON — preserves visual rhythm
- **(b)** Cut call 3 entirely, replace with `find_references` filtered to non-test files — adds a property the demo wasn't showing (**agent-controllable filtering**), and `get_hover` is already in the 16-tool set

**Decision:** **(b)** is the stronger edit. Adds filtering as a demonstrated feature, drops a tool that was already on the "leave out of spotlight" list.

---

## 3. Resolved angle

### 3.1 — One sentence

**yactt is the MCP server that gives an AI agent both the raw bytes of a source file AND the resolved symbol/call/reference graph — lossless + resolved, polyglot Go/TS/JS, with the install-trust chain verifiable end-to-end.**

### 3.2 — The structure (final)

1. **One-line trust strip** — `SLSA L3 · 21 tools · 1 dep · read-only`. Four numbers, four objections closed. Each number is a link to its receipt.
2. **The 5-call worked example** — vague question → multi-hit ambiguity → disambiguation → LSP-backed resolution → filtered callers → source snippet. Provenance stamp mid-transcript. Caption names the LSP path used.
3. **The empty-quadrant chart, captioned small, as a real SVG** — "Here's where yactt sits in the polyglot Go/TS/JS landscape." Render as a Mermaid `quadrantChart` (which GitHub auto-renders to SVG) — **not** as a markdown table. The table format flattens the 2D positioning into a grid; the SVG chart carries the visual punch the positioning depends on. Now the chart *explains* the demo instead of asking the reader to decode a diagram.
4. **Tools table grouped 16 / 4 / 1** — code-intel, registry, persisted_query.
5. **"What's behind the badge row"** — receipts section. Each strip claim has its verifiable artifact:
   - SLSA L3 → the `provenance.intoto.jsonl` attached to the release, with the `gh attestation verify` command
   - 21 tools → output of `mcp tools` against the binary on disk (or list of names)
   - 1 dep → `go.mod` rendered in the section, `require` block visible
   - read-only → the source-tree note that no tool mutates a path outside the per-repo cache
6. **Install** — `go install` line + the two `mcp serve` modes (single-repo, registry-only), one line each.
7. **Status & roadmap** — Python next, multi-repo queries second, persisted-query step chaining third.
8. **Trust & security** — links to `docs/security.md`, mentions the OWASP AST02/05/09 mitigations, closes with the `tree-sitter as the unconditional floor` beat.

### 3.3 — The stand-out move

**The trust strip above the fold, with the receipts section below the install.** Most tool READMEs make trust a footnote. yactt makes it the headline and earns it with receipts.

Concrete: the trust strip isn't a feature callout; it's a **reframe of what a code-intel README is for**. ripgrep, ctags, scip-cpp, scip/lsif, Serena, JetBrains MCP — every competitor README leads with the tool. **yactt leads with the permission slip that lets you believe the tool.** That's the positioning bet no competitor README makes.

### 3.4 — The 5-call demo (final, post-fix-b)

| # | Tool | Purpose | Path |
|---|------|---------|------|
| 1 | `find_symbol` | 3-hit ambiguity surfaced | tree-sitter |
| 2 | `get_definitions` | resolves to interface in 3rd file | **gopls** ← provenance stamp |
| 3 | `find_references` (non-test filtered) | agent-controllable filtering | tree-sitter |
| 4 | `get_callers` (non-test filtered) | filtered production callers | tree-sitter |
| 5 | `get_snippet` (line-bounded) | human-in-the-loop confirmation | tree-sitter |

**Caption under the demo:** *call 2 used gopls; calls 1, 3, 4, 5 used tree-sitter with cross-reference resolution against the parsed AST. yactt stamps every response with its provenance so the agent — and you — can tell which path served the answer.*

### 3.5 — What the chorus critique caught

The synth's draft was shippable. The pragmatist's two hardenings made it *defensible*:

1. **Multi-hit ambiguity is load-bearing.** A single-hit first call sells search, not resolution. The 3-hit pattern is what the quadrant chart is actually promising.
2. **The mid-transcript provenance stamp is the falsifiable claim.** A reader who runs yactt with gopls missing will see all five calls stamped `tree-sitter` and the caption still holds. With the stamp mid-transcript, the caption makes a description of *available* paths, not a promise about a specific call.
3. **Call-3 → find_references is the better fix.** It adds a property (filtering) the demo wasn't yet showing, and lets `get_symbols` stay out of the spotlight (it's a tool-by-tool section, not a demo tool).

---

## 4. Tone

- **Confident, not boastful.** The trust strip earns the right to be confident; the receipts section proves it.
- **Concrete, not abstract.** JSON transcripts, not "the agent can resolve symbols." Specific tool names, specific args, specific outputs.
- **Engineer-to-engineer.** No marketing language. The reader is an agent-builder; they will grep the binary and the source. Don't write prose the source contradicts.
- **Self-aware without being arch.** The "Yet Another Code Tree Tool" backronym appears once, in the opening. The reader gets the joke, moves on. Don't lean on it.

---

## 5. Risks (transcript and section-by-section)

- **The trust strip only lands if the four numbers are individually verifiable.** Each claim needs a one-click receipt. If any claim doesn't survive scrutiny, the strip becomes a liability instead of a permission slip.
- **The 5-call demo uses tool names that need to match the binary's actual `mcp tools` output.** Map synth's draft names (`find_symbol`, `get_definitions`, `get_callers`, `get_snippet`, `get_symbols`) to the real tool surface in `internal/tool/`. Note: the real names are `find_symbol`, `node_get` (for the `get_definitions` beat — `layers=["body"]`), `find_referencing_symbols` (for the `get_callers` beat), `node_source` (for the `get_snippet` beat). `get_symbols` does exist as `get_symbols_overview`. **The pen-wielding stage must reconcile the demo transcript with the real tool names before shipping.**
- **The chart needs to render as a real SVG, not a markdown table.** Mermaid `quadrantChart` block (GitHub auto-renders to SVG inline). Keep the legend short. The current README uses a markdown table — the brain-jam explicitly rejected that format; Pen Wielding must use a `mermaid` fenced code block.
- **The "What an agent actually gets" header is the right register.** Don't soften it to "Example" or "Quickstart." The reader came for the agent's view, not the installer's.

---

## 6. Ready for Stage 4

The angle is locked:

- **Lead:** trust strip
- **Hook:** 5-call worked example
- **Frame:** empty quadrant, Mermaid `quadrantChart` block (renders as SVG inline, not a markdown table)
- **Proof:** receipts section with linked artifacts
- **Close:** install + roadmap + security

Pen Wielding (Stage 5) drafts the README on this skeleton, reconciles the demo transcript to the real tool names, and renders the quadrant chart as a Mermaid `quadrantChart` code block (auto-renders to SVG on GitHub).

---

# Refresh — 2026-07-13 (worktree `plant-camel`)

Re-ran the chorus with current state. The 2026-07-06 angle (trust strip + 5-call demo) is **superseded** by a parser-warm / languages-first lead that better fits the post-PR-54 / V3 / file:// migration state.

**Transcript:** `.brainstorm/chorus-20260713T184229.json`
**Cast:** claude-synth + pragmatist (minimax/MiniMax-M3); critic unavailable again

## What changed in this refresh

| Decision | 2026-07-06 angle | 2026-07-13 angle |
|---|---|---|
| Lead | Trust strip `SLSA L3 · 21 tools · 1 dep · read-only` | **Parser-warm, 6 languages over persistent HTTP** |
| Hook | 5-call worked example (payment validation) | Tools grouped **introspect / traverse / diagnose** with concrete envelope samples |
| Frame | Empty quadrant (lossless × resolved) | Empty quadrant + **Serena contrast** (parser-shaped vs LSP-shaped) |
| Proof | "What's behind the badge row" | **Envelope examples** + **URI normalization rules** |
| Close | Install + roadmap + security | When-to-use-yactt + roadmap + security |
| Tool taxonomy | 16/4/1 (code-intel / registry / persisted) | **introspect / traverse / diagnose** (functional) |

## Round 1 — pragmatist's lead (verbatim summary)

> yactt gives an AI agent a single 21-tool surface over code written in Go, TypeScript, JavaScript, Python, PHP, and Rust. Instead of writing one extractor per language, you get structured symbol/region/RFC-7807-shaped answers over a persistent HTTP connection. One tree-sitter dependency, read-only filesystem access, SLSA-L3 provenance. The wire format is `file://` URIs, so paths your agent already knows just work.

Bet: **languages-first, tools-second**. Language coverage is the moat (anyone can ship 21 tools for one language in a weekend); the tool count is surface area (the thing that scares operators).

## Round 2 — synth's pushback (the persistent-HTTP anchor)

The persistent-HTTP callout is the angle, not the operational footnote. **stdio MCP inherits the lifecycle of the spawning process** — every agent restart, context-window collapse, MCP-client reinit tears down parser state. For a 200k LOC Go monorepo, that's 3-8s of reparse per session resume. HTTP keepalive keeps the parser warm. **Incrementality (reparse only changed files, reuse prior symbol tables) is only possible over a persistent transport** — so persistent HTTP is the prerequisite for the v0.2 roadmap, not a connection-reuse footnote.

**Revised lead (~90 words):**

> yactt is an MCP server that gives an AI agent one parser-warm, URI-addressable view of code in Go, TypeScript, JavaScript, Python, PHP, and Rust. It runs over persistent HTTP, so tree-sitter state survives agent restarts and context-window collapses — a 200k LOC Go monorepo stays parsed across sessions instead of paying 3-8s of reparse latency on every resume. Twenty-one tools, one tree-sitter dependency, read-only filesystem access, SLSA-L3 provenance. Paths are `file://` URIs, so the agent doesn't learn a new addressing dialect.

Three changes worth calling out:

1. **"parser-warm" is the new hero word** — concrete benefit, not protocol detail.
2. **The 200k LOC / 3-8s number** does real work — that's a budget an agent operator can feel.
3. **"Paths are `file://` URIs"** moved to the closing sentence — earns its place as the "by the way, this just works" beat.

## Round 2 — file:// URI normalization (the bug magnet)

Agents that speak URI are picky in different ways. Claude Desktop, Cline, Goose all normalize input differently:

- Claude passes paths from tool output, not normalized URIs → "doesn't start with file://" rejections
- Cline passes relative paths when cwd is the repo root → "relative not allowed" rejections
- Some agents percent-encode, some don't → two symbols for the same file if match logic is sloppy

**Action item for the README:** add an explicit "URI handling" subsection, not a footnote:

> **URI normalization.** yactt accepts `file://` absolute URIs (`file:///abs/path`), bare absolute paths, and paths relative to the workspace root. The server normalizes on input; clients should treat the `file://` form returned in tool responses as canonical. Relative paths without a workspace root set are rejected.

This is the kind of thing that looks like docs pedantry until an agent engineer spends an afternoon debugging "works in Cline, errors in Claude Desktop."

## Round 2 — envelope taxonomy (`introspect / traverse / diagnose`)

The synth's pushback on the original `structure / navigation / diagnostics`:

> `introspect / traverse / diagnose` is materially better. "Navigation" overlaps "structure" — find-references is a *query* against structure, the language doesn't matter once you have the symbol table. The rename makes the data model visible: **introspect** = read the file, return contents-shaped view; **traverse** = walk the graph, return edges; **diagnose** = read the file, return failure-shaped view.

> The graph framing matters because the next tool you ship (almost certainly something like `impact` or `dependents`) fits cleanly into *traverse*. If you call it "navigation" you're locked into IDE vocabulary and the roadmap gets cramped.

The pragmatic envelope shape (honest version):

| Group | Response shape | Notes |
|---|---|---|
| introspect | `{uri, language, symbols: [...], ranges: [...]}` | same envelope per language |
| traverse | `{uri, language, edges: [...], target?: uri}` | same envelope, different payload key |
| diagnose | RFC 7807 `{type, title, detail, source_pointer}` | same envelope across all errors |

"Shared envelope, shape-compatible payloads, RFC 7807 for errors."

## Round 2 — Serena contrast (the trust-earning paragraph)

The honest trade:

| | yactt | Serena (LSP-based) |
|---|---|---|
| Response shape | Parser-shaped (AST-level) | LSP-shaped (IDE semantics) |
| Fidelity | Predictable across all 6 langs | Highest for what LSP covers |
| Cold start | Re-parse (~seconds, one-time) | Index build + per-language server spin-up |
| Cross-language queries | Same envelope, same syntax | Per-language server, per-language quirks |
| Where it loses | No type inference, no hover docs | Wins on Go/Python/Rust where LSP is mature |
| Where it wins | PHP (LSP story is grim), 6-lang consistency, persistent state | Single-language deep work in a well-served language |

> **yactt trades LSP-grade fidelity for cross-language consistency and parser-warm persistence.** If your codebase is single-language and you want hover-docs precision, use Serena. If your codebase is polyglot or you want predictable tool behavior across all of it, use yactt.

That paragraph earns trust because it admits the trade. The reader who picks Serena after reading it still recommends yactt to their polyglot-team friend.

## Resolved angle (this refresh)

### Lead — parser-warm + 6 languages + persistent HTTP

The hero word is **parser-warm**. The mechanism sentence is **persistent HTTP**. The moat is **6 languages**. The tool count (21) is evidence, not headline.

### Structure

1. **The lead** — ~90 words; parser-warm + persistent HTTP + 6 langs + 21 tools + 1 dep + SLSA-L3 + file:// URIs
2. **Quickstart** — install + first `tools/call` showing the `file://` URI round-trip
3. **Tool surface** — `introspect / traverse / diagnose` with one-liner per tool + concrete envelope samples
4. **URI handling** — explicit normalization rules (not a footnote)
5. **When to use yactt (vs. LSP-shaped tools)** — the Serena contrast, named honestly
6. **Trust sidebar** — SLSA-L3 + read-only + audit log (confirms, doesn't convince)
7. **Roadmap** — cross-repo queries, workspace_overview, persisted-query step chaining, consumer-side SLSA verification, tree_at(ref)
8. **Security** — links to docs/security.md + the tree-sitter floor

### What this beat captures that the prior beat missed

- **The persistent-HTTP angle** — it's the prerequisite for the v0.2 incrementality story, not a footnote
- **The parser-warm framing** — concrete budget number (200k LOC, 3-8s reparse)
- **Languages-first ordering** — language coverage is the moat
- **The introspect/traverse/diagnose taxonomy** — the data model becomes visible; `traverse` makes the next roadmap tool (`impact`/`dependents`) obvious
- **The Serena contrast** — the trust-earning paragraph that wasn't in the prior beat

### Risks (this refresh)

- **The lead is longer (~90 words) than the prior ~80-word lead.** Worth it: parser-warm + persistent HTTP + 6 langs in one block is the deal.
- **The "When to use yactt" section needs Serena's cooperation.** If Serena's positioning changes, this section needs updating. Worth a re-read on every release.
- **The 200k LOC / 3-8s reparse number must be defensible.** Check `docs/benchmarks.md` (recently added) for the actual number. If the benchmark says something different, the lead needs to match.
- **The critic was unavailable again.** Both 2026-07-06 and 2026-07-13 runs hit this — single-provider cast fallback. Future jams should retry with the critic enabled.
- **The `introspect/traverse/diagnose` taxonomy is a new naming convention.** Tool descriptions in `register.go` still use functional names (`tree_overview`, `node_get`, `node_edges`). Pen Wielding needs to add the taxonomy as a *grouping* in the README, not as a rename of the tools.

## Source for this refresh

- `chorus --prompt ... --seed ... --max-rounds 2 --critique` (full transcript at `.brainstorm/chorus-20260713T184229.json`)
- 2026-07-06 `brain-jam.md` (foundation; superseded on lead, hook, taxonomy)
- 2026-07-13 `deep-dive.md` (tool names + wire shape)
- 2026-07-13 `crystal-ball.md` (corrected roadmap + audience segments)
