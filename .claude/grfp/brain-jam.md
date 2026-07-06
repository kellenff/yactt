# Brain Jam — yactt

> Stage 3 of the GitHub README For Perfectionists workflow.
> Generated 2026-07-05.

**Mode**: Single-provider fallback (Chorus MCP unavailable — see § 1).
**Cast lenses**: synth (optimistic), pragmatist (delivery-focused), critic (jargon-averse).
**Counterpart reports**: `.claude/grfp/deep-dive.md`, `.claude/grfp/crystal-ball.md`.

---

## 1. Fallback note

`mcp__chorus__chorus` is not available in this session. Per the brain-jam skill's documented fallback ("Falls back to a single-provider cast when only one API key is set"), this stage runs as a **single provider applying three analytical lenses sequentially** — synth, pragmatist, critic — instead of N independent voices. The result is still a coherent positioning document, but it lacks the genuine cross-model adversarial pressure.

**Recommendation**: re-run this stage with Chorus available for the sharpest positioning. The single-provider output is shippable; the chorus version is dialed-in.

---

## 2. Seed context (carried over from prior stages)

- **Name**: YACTT — *Yet Another Code Tree Tool*. Self-aware GNU/YACC/WINE-tradition tone per design doc.
- **Tagline (current)**: "Federated code intelligence for AI agents — walk the tree, choose your layer."
- **Positioning (from crystal-ball)**: First serious code-intelligence MCP server to ship to production. Sits in the "empty quadrant" (lossless + deep) in Go/TS/JS, Python planned.
- **What's shipped**: V1, V2.x, V3 subgraph slice, V3 first slice, V3 method-bodies slice, Phase F complete. Single-source `id.For` + per-receiver keying resolved 2026-07-04.
- **Surface**: 10 MCP tools + 1 `persisted_query` tool; one CLI binary (`yactt overview`, `yactt mcp serve`, `yactt version`, `yactt help`); bundled Claude Code plugin.
- **Trust story**: dual-licensed (Apache-2.0 / MIT); SLSA Build Provenance Level 3 attestations; SHA256 + TOFU bootstrap.

---

## 3. The three lenses

### 3.1 Synth lens — what the README could be

Open the README with the empty quadrant. It's a strong narrative move — anchors the reader's mental model in *why this exists*, not *what it does*. Follow with the layered model (tree-sitter floor → LSP depth) as the architectural signature. Show the install story as three parallel tracks: **for Claude Code users** (the plugin), **for AI agent authors** (the MCP server), **for shell pipelines** (the overview command).

Lean on the diagram from `docs/design.md` (already a Mermaid chart). The 10-tool table is the centerpiece of the README's middle — it's the surface area; everything else is plumbing.

End with status (what shipped, what's next) and trust (license + SLSA). The tagline earns its place at the top *and* as a callout in the about section.

**Synth recommendation**: hero = quadrant diagram + tagline. Middle = architecture diagram + 10-tool table. Footer = status + SLSA.

### 3.2 Pragmatist lens — what works in the wild

A README is read at three depths:

1. **First scan** (10 seconds): hook, value prop, can-I-install-it-now.
2. **Skim** (1 minute): the 10 tools, the architecture, the install story.
3. **Deep read** (5+ minutes): design doc, plugin README, the source itself.

Most readers fall into depth 1 and 2. Depth 3 readers don't need the README to teach — they need it to point.

Concretely:

- **Install instructions must work on copy-paste** — no "go to release page" buried in prose. Three code blocks, three audiences, in that order.
- **The 10-tool table needs one-line descriptions**, not paragraphs. A reader should be able to scan the table and know which 2–3 tools solve their immediate problem.
- **Status / roadmap should be terse and verifiable** — link to commits or changelog, don't claim milestones in prose.
- **The SLSA L3 attestation belongs in the trust section** — it's a real differentiator for any tool an AI agent is going to load from disk. One line + the `gh attestation verify` one-liner.
- **Pointer to deeper docs**: `docs/design.md` for architecture, `plugins/yactt/README.md` for the Claude Code install story.

**Pragmatist recommendation**: depth-1 content ≤ 8 lines; depth-2 content in a `<details>` block or after a clear horizontal rule; depth-3 content is a "Read more" link, not in the README.

### 3.3 Critic lens — what will fail

Jargon and self-deprecation are the two ways this README goes wrong.

- **"Federated" is jargon.** Most readers won't have the design doc context. They'll pause, and a pausing reader is a leaving reader.
- **"Yet Another" as the headline opener reads as apologetic.** Self-deprecating names like GNU and WINE worked because they were the names people already knew; the README isn't a context where that joke lives well.
- **"Walk the tree, choose your layer" is poetic but vague.** The reader doesn't know what "the tree" or "your layer" means in the first 30 seconds.
- **The 10-tool wall** — if presented without context, it reads as "yet another tool that ships 10 features" rather than "10 facets of one model of a codebase." A short framing sentence above the table saves it.
- **Marketing-speak kills engineer trust.** Avoid "powerful", "robust", "seamlessly", "next-generation", "AI-powered" — all of those are red flags in a Go project's README.
- **Don't bury the L3 attestation.** It's the answer to "why should I trust this binary I'm about to shell out from a SessionStart hook." Front-load the trust signal.

**Critic recommendation**: the *backronym* earns one mention (in the about section, with the GNU/YACC/WINE nod); the *tagline* is the headline, not the backronym. The hero is the position (lossless + resolved for AI agents), not the name.

---

## 4. Synthesis: the chosen angle

After three lenses, the README's angle is:

> **yactt is the MCP server that gives AI agents lossless source and resolved semantics over a polyglot (Go/TS/JS, Python planned) codebase, with three install paths and a tamper-evident binary.**

Concretely:

- **Hero**: one-line tagline ("Federated code intelligence for AI agents — lossless source, resolved semantics, MCP-native.") + three install paths as parallel code blocks (plugin / MCP / CLI).
- **Positioning**: a small "Why" paragraph + the empty-quadrant chart. 4 lines of prose, then the chart. No essay.
- **Architecture**: a short paragraph explaining the layered model (tree-sitter = floor, LSP = depth, MCP = surface) + the ASCII or Mermaid diagram from the deep-dive report. One paragraph, one diagram.
- **The 10 tools**: a table with one-line descriptions. Framed by a single sentence ("yactt exposes the codebase as one node graph with 10 facets of access."). The 11th (`persisted_query`) is mentioned below the table as "for curated workflows".
- **Status & roadmap**: a short list. What's shipped (linkable), what's next.
- **Trust**: license + SLSA L3 attestation + `gh attestation verify` one-liner. Plus a note that `edit_impact` does not apply changes (a trust signal in its own right).
- **Footer**: pointer to `docs/design.md` for deep architecture, `plugins/yactt/README.md` for the Claude Code install story, and the GitHub releases page for binaries.

### 4.1 What we explicitly chose NOT to do

- Not opening with the backronym. The name lives in the title; the GNU/YACC/WINE nod lives in a one-line aside in the about section.
- Not using "federated" in the headline. It stays as a paragraph word — readers who reach the architecture section get it.
- Not listing 10 tools without framing. The framing sentence above the table is non-negotiable.
- Not embedding the full status report in the README. Pointer to CHANGELOG / `yactt-progress` memory.
- Not advocating for AI agents in vague terms. The trust story is concrete (SLSA L3, parse-don't-validate, read-only surface).

---

## 5. Tone calibration

| Section | Tone | Why |
|---|---|---|
| Title + tagline | Direct, slightly literary | Earns the read; signal seriousness |
| Why (positioning) | Engineer-honest | No fluff; let the chart do the work |
| Install paths | Copy-paste-ready | Zero friction |
| Architecture | Compact prose + diagram | One paragraph, not an essay |
| Tools table | Scan-friendly, no jargon in descriptions | Reader's #2 stop |
| Status / roadmap | Terse, verifiable | "Shipped X, next: Y" |
| Trust | Concrete + short | Show, don't tell |

---

## 6. Hook candidates (ranked)

After the lens work, the top three hooks are:

1. **"Federated code intelligence for AI agents — lossless source, resolved semantics, MCP-native."** — wins on clarity, balanced.
2. **"Walk the tree, choose your layer."** — wins on poetry, loses on cold-read clarity.
3. **"The code-intelligence MCP server your AI agent actually wants."** — wins on personality, loses on the "yet another" backronym being absent.

**Chosen**: #1 for the headline. #2 as a sub-headline (italic, smaller) under the title block. #3 rejected — too punchy, doesn't survive depth-2 skim.

---

## 7. Structure (proposed)

```
# yactt
*Federated code intelligence for AI agents — lossless source, resolved semantics, MCP-native.*

*Walk the tree, choose your layer.*

## Why yactt
[2 short paragraphs]
[mermaid: the empty quadrant chart]

## Install
### For Claude Code users
[code block: plugin marketplace add]
### For AI agent authors
[code block: stdio MCP server config]
### For shell pipelines
[code block: brew install or curl tarball + `yactt overview .`]

## How it works
[1 paragraph: tree-sitter floor + LSP depth + MCP surface]
[mermaid or ASCII: the architecture]

## The 10 tools
[framing sentence]
[table: tool | purpose]
[1 line: persisted_query adds curated workflows]

## Status & roadmap
[short bullet list with links]

## Trust
- Dual-licensed Apache-2.0 / MIT
- SLSA Build Provenance Level 3 — verify with one command
- read-only by design (`edit_impact` does not apply changes)
- tree-sitter as the unconditional floor (works without gopls / ts-language-server)

## Read more
- Architecture deep dive — docs/design.md
- Claude Code plugin story — plugins/yactt/README.md
- Latest release — github.com/kellenff/yactt/releases/latest
```

---

## 8. What makes this README stand out

1. **The empty-quadrant chart in the Why section** — most code-intelligence READMes open with feature lists. A quadrant chart is a visual differentiator and immediately earns the "lossless + deep" claim.
2. **Three parallel install paths** — most tools have one. The "for Claude Code / for AI agents / for shell" trio matches the actual audience surface.
3. **The 10-tool table framed as "10 facets of one graph"** — most tools present their toolset as a wall of features. Framing it as facets of one model makes the surface area feel coherent, not chaotic.
4. **The trust section that names the SLSA L3 attestation concretely** — most READMes gesture at "secure" without specifics. The `gh attestation verify` one-liner is concrete, verifiable, and rare.
5. **The "Yet Another" backronym as a one-line aside, not a headline** — preserves the project's self-aware tone without making the joke carry the read.

---

## 9. Risks and watch-outs for Stage 5 (Pen Wielding)

- **Don't let the diagram dominate.** The mermaid chart is strong; an oversized chart pushes the prose down.
- **Don't over-explain the layered model.** One paragraph + diagram. The design doc carries the depth.
- **Keep the install code blocks copy-pasteable.** No "consult your distribution's package manager" hedging.
- **Avoid first-person plural ("we")** — Go project READMes that succeed tend to use third-person or imperative; avoid the corporate-blog tone.
- **The plugin README stays canonical for plugin install.** Don't duplicate the install-with-SLSA-verify story in the root README — link to it.

---

## Ready for Stage 4

Think Tank next — research exemplar READMEs (golangci-lint, ripgrep, fd, ast-grep, gopls itself) to pressure-test the chosen structure. The crystal-ball + deep-dive + brain-jam outputs together give the research a tight seed: *what does a Go project README look like when it's aimed at AI-agent authors instead of human developers, and the binary ships with SLSA L3?*

Proceeding to **Stage 4: Think Tank**?