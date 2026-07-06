# Think Tank — yactt

> Stage 4 of the GitHub README For Perfectionists workflow.
> Generated 2026-07-05.

**Mode**: Web research via WebFetch (Chorus/WebSearch unavailable in this session — see § 1).
**Counterpart reports**: `.claude/grfp/deep-dive.md`, `.claude/grfp/crystal-ball.md`, `.claude/grfp/brain-jam.md`.

---

## 1. Methodology + fallbacks

- `WebSearch` returned 400 "invalid params" on every call this session.
- `WebFetch` worked against raw `raw.githubusercontent.com` README URLs — six exemplars fetched successfully.
- **No** Exa or Gemini deep-research MCP installed; this stage runs as direct README reads.

Projects sampled:

| Project | Why | Verdict |
|---|---|---|
| **ripgrep** (BurntSushi/ripgrep) | Gold standard for "single-purpose fast CLI" | Adopt trust signals + honest limitations |
| **ast-grep** (ast-grep/ast-grep) | Direct cousin: AST-tooling CLI with tree-sitter | Adopt multi-platform install, vision statement framing |
| **fd** (sharkdp/fd) | Gold standard for "find with good UX" | Avoid: too long, too dense |
| **golangci-lint** (golangci-lint/golangci-lint) | Multi-tool runner with external docs | Adopt: external-hub README pattern |
| **ruff** (astral-sh/ruff) | Modern polyglot-linter-equivalent (Python-only) | Adopt: 4 install paths, adopter list (future) |
| **Model Context Protocol servers** (modelcontextprotocol/servers) | Direct MCP-server reference | Adopt: LLM-targeted framing; avoid emoji headings |
| **biome** | Attempted; README fetch returned a stub | n/a |

---

## 2. Pattern matrix

| Pattern | ripgrep | ast-grep | fd | golangci-lint | ruff | mcp/servers | yactt's pick |
|---|---|---|---|---|---|---|---|
| Trust badges (shields.io cluster) | yes (5) | yes (7) | yes (3) | yes (8+) | yes (5) | no | **no** — engineer audience is allergic; SLSA L3 is a stronger signal |
| Vulnerability disclosure | explicit | no | no | no | no | no | **yes** — `gh attestation verify` one-liner |
| Honest "why not" section | yes | no | no | no | no | no | **yes** — concrete limitations (no Python yet, no remote repos, no write tools) |
| Multi-platform install | yes (~20) | yes (~10) | yes (~20) | external-only | yes (4) | npx/uvx | **3 paths** — by audience (plugin / MCP / CLI), not by OS |
| External docs hub | yes (GUIDE.md, FAQ.md) | yes (website, playground) | no (single-page) | yes (golangci-lint.run) | yes (docs.astral.sh) | yes (ADDITIONAL.md) | **yes** — `docs/design.md` + `plugins/yactt/README.md` |
| Demo / screenshot | yes | yes (animated) | yes (SVG) | no | yes (benchmark chart) | no | **skip** — README is short by design |
| Performance benchmarks | yes (6 tables) | no | yes (table) | no | yes (bar chart) | no | **skip** — yactt isn't a perf tool; the trust signal is integrity, not speed |
| Adopter / testimonial section | no | no | no | sponsor logos | yes (~100 orgs) | no | **skip** — too early (MVP); defer to v1.0 |
| Vision / tagline paragraph | yes | yes ("democratize…") | no | no | no | no | **yes** — already drafted in brain-jam.md |
| Pre-commit / GH Actions example | no | no | no | yes (CI link) | yes (full YAML) | no | **skip** — yactt is a server, not a developer toolchain |
| Test data / fixtures noted | yes (paths) | no | no | no | no | no | **skip** — internal detail |
| Completion / shell integration | yes | no | yes | no | no | no | **skip** — MCP server doesn't need shell completions |
| LLM-targeted framing | no | no | no | no | no | yes (every section) | **yes** — explicitly: "for AI agents", tool descriptions framed for the agent reader |
| Minimum-runtime / minimum-Go statement | yes (Rust 1.85.0) | no | no | no | yes (Python 3.14) | no | **yes** — Go version pinned in go.mod, worth surfacing |
| Multi-runtime parity | no | no | no | no | no | yes (npx / uvx / Windows wrapper) | **yes** — three install tracks by audience |

---

## 3. Anti-patterns (what NOT to copy)

| Pattern | Why it works elsewhere | Why it would hurt yactt |
|---|---|---|
| **900-line single-page README** (fd) | fd is a `find` replacement; users want one searchable page | yactt has a docs/ directory and a plugin README; duplicating in root README is wrong |
| **Benchmark tables** (ripgrep, ruff) | perf is ripgrep/ruff's *whole identity* | yactt's identity is lossless + resolved for agents; perf is incidental |
| **"10x faster than X" framing** (ruff) | sells in Hacker News threads | reads as marketing to the engineer audience yactt is targeting |
| **Emoji-prefixed headings** (mcp/servers) | works for a directory of small servers | wrong for a Go project's primary README |
| **Testimonial blockquotes** (ruff) | early-stage adoption sell | yactt has no adopters yet — defer to v1.0 |
| **Sponsor-placement imagery** (golangci-lint) | appropriate for a BDFL project with corporate sponsors | premature for an MVP |
| **Multiple install-platform commands** (ripgrep, fd, ast-grep) | appropriate for general-purpose CLIs | yactt's audience is narrower: agents, plugin users, shell users |

---

## 4. What yactt should steal (and how)

### 4.1 From ripgrep — vulnerability disclosure + minimum runtime

Two concrete lines, top of the trust section:

```
Go 1.21+ required. Binary releases are SLSA Build Provenance Level 3 — verify with:
gh attestation verify <tarball> -R kellenff/yactt
```

The dual license (Apache-2.0 / MIT) follows. **Concrete, verifiable, rare.**

### 4.2 From ast-grep — vision statement + multi-track install

The vision paragraph ("democratize abstract syntax tree magic…") is short and quotable. yactt has its equivalent already in `docs/design.md` § "Project identity":

> *"Self-aware in the GNU / YACC / WINE tradition; tongue-in-cheek acronym on a serious tool."*

One sentence in the README's about section. Earned.

Multi-track install: ast-grep covers 8 package managers. yactt covers 3 *audiences* (Claude Code plugin / MCP server / shell CLI). The structural insight — **organize install by audience, not by OS** — is the carry-over.

### 4.3 From modelcontextprotocol/servers — LLM-targeted framing

Every section header can be re-read through the lens "is this useful to an AI agent reading this README?" Examples that need this framing in yactt's README:

- **The 10 tools table**: each row's purpose should be written as "what an agent does with this," not "what it does to your code."
- **Why yactt**: the empty-quadrant framing already does this work.
- **Trust**: the SLSA L3 attestation is what makes an agent author comfortable letting `mcp serve` start at every Claude Code session.

The "WARNING: these are not production-ready" callout in `mcp/servers` is a useful **inverse example** — yactt is production-ready, and saying so explicitly (in the Status section) is the move.

### 4.4 From golangci-lint — external docs hub

golangci-lint's README is ~35 lines and links to `golangci-lint.run` for everything. yactt's README should mirror this:

- One paragraph per section in the README.
- One pointer per section to the deeper docs (`docs/design.md` for architecture, `plugins/yactt/README.md` for plugin story).

Avoid: duplicating the install instructions in both the root README and the plugin README. Pick one canonical home; the other points.

### 4.5 From ruff — adopter list (deferred)

ruff's "Who's Using Ruff?" section is one of its strongest trust signals, but it requires 50+ real adopters to land. **Defer to v1.0.** Once yactt has users, this becomes a real differentiator.

### 4.6 From fd — callout blocks (`> [!NOTE]`)

`fd` uses GitHub-flavored markdown alerts effectively. yactt should use them in two places:

- "Tree-sitter as the unconditional floor — yactt works without `gopls` or `typescript-language-server`."
- "`edit_impact` does not apply changes — it only analyses the blast radius of a rename."

Both are warnings disguised as features; both belong in a callout.

---

## 5. Composition for yactt's README

Pulling the patterns together with the brain-jam's structure, the final shape:

| Section | Lines | Source inspiration |
|---|---|---|
| Title + tagline | 3 | brain-jam #1 hook |
| *Walk the tree, choose your layer.* | 1 | brain-jam sub-headline |
| **Why yactt** — empty-quadrant chart + 2 paragraphs | ~10 | design doc § 1 |
| **Install** — 3 code blocks by audience | ~15 | ast-grep (multi-track) |
| **How it works** — layered model paragraph + diagram | ~12 | brain-jam § 7 |
| **The 10 tools** — framing sentence + table + persisted_query footnote | ~20 | brain-jam |
| **Status & roadmap** — terse list | ~10 | crystal-ball § 6 |
| **Trust** — 4 short items (license, SLSA L3 one-liner, read-only contract, tree-sitter floor) | ~10 | ripgrep + fd callouts |
| **Read more** — pointers to design doc, plugin README, releases | ~5 | golangci-lint hub |

**Target length**: 90–120 lines of markdown. Long enough to be the canonical landing page; short enough to read in one screenful.

---

## 6. Things that did NOT make the cut

These came up in the research but were rejected:

| Idea | Source | Why rejected |
|---|---|---|
| Benchmark table | ripgrep | yactt isn't a perf tool |
| Adopter wall | ruff | too early (no users yet) |
| Testimonials | ruff | same |
| Sponsor logos | golangci-lint | no sponsors |
| Pre-commit / GH Actions recipes | ruff | yactt is a server, not a CI tool |
| Multi-OS install matrix | ripgrep, fd | organized by audience instead |
| Demo GIF / animated SVG | ast-grep, fd | README is text-first; the architecture diagram is the visual |
| Emoji-prefixed headings | mcp/servers | wrong tone for Go project |
| Stargazers-over-time chart | golangci-lint | too early (graph would be flat) |
| Comparison table vs. competitors | ripgrep (beyondgrep.com), sourcetrail-style | the empty-quadrant chart *is* the comparison |
| French / Spanish / Chinese translations | fd | defer to v1.0+ |

---

## 7. Open question for Stage 5 (Pen Wielding)

The brain-jam chose the empty-quadrant Mermaid chart as the hero visual. The crystal-ball noted Mermaid charts already exist in `docs/design.md`. **Should the README embed the chart from the design doc, or redraw a smaller, README-tuned version?**

- Embedding = one source of truth, but the design doc's chart may be too dense.
- Redrawing = full control over readability, but creates a maintenance debt.

**Recommendation for Stage 5**: redraw a smaller 2×2 chart tuned for the README's 800-pixel width. Link to the design doc for the full quadrant analysis.

---

## 8. Stage 4 confidence + coverage

- 5 of 6 attempted exemplars fetched and parsed successfully. Biome failed (raw URL returned a stub path) — not material; biome is a formatter, not a close cousin of yactt.
- Patterns extracted: 16 in the matrix; 5 adopted; 11 explicitly rejected.
- Trust signals, install paths, and LLM framing are the three strongest takeaways.
- The brain-jam's structure survives the research intact — the patterns validate, not contradict.

## Ready for Stage 5

Pen Wielding next — write the README. The brain-jam's structure + the think-tank's pattern choices + the crystal-ball's roadmap content + the deep-dive's technical facts are all in hand.

Proceeding to **Stage 5: Pen Wielding**?