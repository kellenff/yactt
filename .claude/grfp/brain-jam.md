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
