# Brain Jam — yactt plugin

> Stage 3 of the GRFP workflow for `plugins/yactt/README.md`.
> Generated 2026-07-05.

**Mode**: Single-provider fallback (Chorus MCP unavailable — same constraint as the root README workflow).
**Cast lenses**: synth, pragmatist, critic.
**Counterpart reports**: `.claude/grfp/plugin-deep-dive.md`, `.claude/grfp/plugin-crystal-ball.md`.

---

## 1. Seed context (carried over from prior stages)

- **Plugin path**: `plugins/yactt/`
- **Plugin name / version**: `yactt` 0.1.0
- **Plugin description** (from `plugin.json`): "Federated code intelligence: walk the tree, choose your layer."
- **Existing README**: 86 lines. Has install, integrity model (centerpiece), use, the 10 tools table, direct CLI, license.
- **Six files** total: 2 manifests, 1 hook, 1 install script (192 lines), 1 skill (28 lines), 1 README.
- **Trust story**: SHA256SUMS + TOFU + SLSA L3 (manual verification today, not yet automated).
- **Three audiences**: Claude Code users (primary), plugin developers (escape hatch), security-conscious developers (integrity story).

---

## 2. The three lenses

### 2.1 Synth lens — what the plugin README could be

The plugin's signature is the **integrity model**. Lean into it: lead with the trust story, treat the install command as the resolution, treat the 10 tools as a confirmation that the trust is well-placed.

Pull the threat matrix up to depth-1 visibility. A reader who scans for 10 seconds should see "this project takes install safety seriously" before any code block runs. The threat matrix table is doing heavy lifting — make sure it lives in the install section, not buried in a "Trust" footer.

Surface the **`code-explore` skill** explicitly. The skill is the canonical teaching surface for the agent — if the user understands what their agent is going to be told, they trust the install more.

Surface the **developer escape hatch** (`<repo>/bin/yactt`). Plugin maintainers are a small but real audience; one line for them.

**Synth recommendation**: lead with integrity, lift the skill story, surface the escape hatch, point to the root README for architecture.

### 2.2 Pragmatist lens — what works in the wild

A Claude Code plugin README is read at three depths:

1. **30-second scan** (most readers): "Will it install cleanly? Is it safe? Does it auto-do-things?"
2. **2-minute skim**: the threat matrix, the skill story.
3. **5+ minute deep read**: hook mechanics, escape hatch, uninstall story.

Most readers fall into depth 1. The README has one job at depth 1: **show the install command, show the integrity model, show that the project ships a skill**.

Concretely:

- **The install command should appear in the first 15 lines.** No scrolling.
- **The threat matrix should appear in the first 60 lines.** It's the signature.
- **The skill frontmatter should be quoted verbatim** — it's a clean triggering description and lets the reader know what their agent will learn.
- **The 10-tool table**: keep it (already there), but consider trimming repetition with the root README.
- **Update / uninstall is a 3-line note**, not a section. The bootstrap upgrades on version mismatch; uninstall is two `rm`s.
- **Pointer to the root README is one line** in the closing — the architecture and positioning live there.

**Pragmatist recommendation**: depth-1 content in lines 1–60; depth-2 content in lines 60–120; depth-3 content in lines 120+. The README stays < 150 lines.

### 2.3 Critic lens — what will fail

Three things would make this README worse:

1. **Treating "yactt for Claude Code" as the hook.** Claude Code users know what a plugin is; they don't need the genre explained. The hook should be a value prop, not a category name.
2. **Hedging the threat matrix with "we plan to add…".** The matrix is honest about what it catches and what it doesn't. Adding hedges weakens the honesty. Note the gaps once, in one sentence, with a pointer to the roadmap.
3. **Repeating the 10-tool table verbatim from the root README.** Plugin readers don't need the table re-explained; they need the *integration* (prefix `mcp__plugin_yactt_yactt__`) and the skill story.

Two things already in the existing README deserve explicit praise:

- The integrity-model section's structure (What it catches / What it doesn't catch) is the right shape. Don't refactor it.
- The "Use" example prompt ("Summarize this repo.") is a good hook for depth-2 readers. Add two more prompts to round out the section without overstaying.

**Critic recommendation**: shrink the opening, lift the threat matrix up, surface the skill frontmatter, drop the redundant framing, keep the integrity model honest.

---

## 3. Synthesis: the chosen angle

After three lenses, the plugin README's angle is:

> **The Claude Code plugin installs the yactt server with one command, verifies the binary against published checksums and SLSA Level 3 attestations, and teaches the agent the canonical exploration flow via the `code-explore` skill.**

Concretely:

- **Hero**: a one-line tagline ("The yactt plugin for Claude Code.") plus a one-line sub-tag (the value prop).
- **Quickstart**: a copy-pasteable two-line install (marketplace add + plugin install).
- **Integrity model**: the threat matrix, table form, with three rows (threat / defense / catches?).
- **Skill story**: the `code-explore` frontmatter quoted as a blockquote, followed by the 8-step tool progression summary.
- **Developer escape hatch**: one paragraph, below the integrity section.
- **The 10 tools**: a compact version of the table, marked as a subset of the project.
- **Update / uninstall**: three lines.
- **Trust pointers**: dual license + SLSA L3 attestation one-liner.
- **Read more**: the root README, design doc, releases.

### 3.1 What we explicitly chose NOT to do

- Not opening with the plugin.json description ("Federated code-intelligence MCP server + code-explore skill…"). It's the manifest's job.
- Not marketing "in seconds" or "with zero config." The integrity model proves the install is safe; claiming speed elsewhere is filler.
- Not embedding the full `code-explore` skill body in the README. It's quoted where the trigger matters; the canonical teaching surface is the skill file.
- Not duplicating the 10-tool table from the root README. A one-line summary + a link.
- Not declaring victory on the SLSA L3 attestation — the bootstrap doesn't yet invoke `gh attestation verify`. Be honest in the threat matrix.

---

## 4. Tone calibration

| Section | Tone | Why |
|---|---|---|
| Title + tagline | Direct | Get to the install quickly |
| Quickstart | Copy-paste-ready | Zero friction |
| Integrity model | Engineer-honest, table-driven | The signature section |
| Skill story | Quoted frontmatter + brief explanation | The reader's question: "what does this teach the agent?" |
| Escape hatch | Quiet mention | Developer audience, not the primary |
| Update / uninstall | Two `rm`s | Don't over-explain |
| Trust | Pointer to root README | Don't duplicate |

---

## 5. Hook candidates (ranked)

After three lenses:

1. **"yactt for Claude Code. Federated code intelligence with a verifiable install story."** — direct, names both the tool and the trust model.
2. **"Install the yactt MCP server in Claude Code with one command."** — feature-first, slightly generic.
3. **"The yactt Claude Code plugin."** — minimal; underwhelming; no value prop.

**Chosen**: #1. Adds the value prop without overclaiming; the word "verifiable" earns its place by the integrity model that follows.

---

## 6. Structure (proposed)

```
# yactt — Claude Code plugin
*Federated code intelligence with a verifiable install story.*

## Quickstart
[2-line install: marketplace add + plugin install]

## What this plugin does
[2 short paragraphs:
 - installs yactt on first use (no Go toolchain)
 - wires the MCP server to your project
 - ships the `code-explore` skill that teaches the agent the 8-step tool progression]

## The integrity model
[intro sentence]
[threat matrix table]
[one-line: gh attestation verify + a sentence noting the bootstrap doesn't yet run it]

## The `code-explore` skill
[blockquote of the skill's frontmatter — the trigger description]
[brief 8-step progression summary]
[link: skills/code-explore/SKILL.md is the canonical source]

## For developers working on yactt itself
[one paragraph on the escape hatch: drop a build into <repo>/bin/yactt]
[link to install.sh for the full hook behavior]

## Update & uninstall
[3 lines: bootstrap upgrades on version mismatch; uninstall is rm + rm]

## The 10 tools
[1-line summary]
[compact table or link back to root README's full table]

## Trust
[3 short bullets:
 - dual license Apache-2.0 / MIT
 - SLSA Build Provenance Level 3
 - read-only by design (link to root README for details)]

## Read more
[4 pointers: root README, design doc, plugin skill, latest release]
```

**Target length**: 100–140 lines.

---

## 7. What makes this README stand out

1. **Integrity model as depth-1 content.** Most plugin READMes treat trust as a footer or skip it entirely. Lifting the threat matrix into the first 60 lines is the differentiator.
2. **Skill frontmatter quoted as a blockquote.** The triggering description is the *user's first question* ("what will my agent do?"). Quoting the file gives the answer in the reader's voice.
3. **One-line developer escape hatch.** A plugin maintainer reading the README knows exactly what to do.
4. **Honest threat matrix.** "What it doesn't catch" is explicit. Most security posture docs hedge or omit the gap.
5. **Pointer back to the root README** (not duplication). The plugin README stays short; the user can drill into architecture / depth if they want.

---

## 8. Risks and watch-outs for Stage 5 (Pen Wielding)

- **The threat matrix must stay honest.** Don't trim it for brevity; don't add hedges that weaken it.
- **The skill quote must be verbatim.** Editing the frontmatter weakens the trigger.
- **Keep the 10-tool table compact.** One row per tool, no re-explanation.
- **Don't open with the plugin.json description.** It's the manifest's voice, not the README's hook.
- **Don't claim the bootstrap runs `gh attestation verify`.** It doesn't, yet. The integrity model section says so.
- **The root README is the architecture canonical.** The plugin README points, doesn't repeat.

---

## 9. Looking ahead

The Stage 4 (Think Tank) for the plugin should re-use most of the root README's exemplar patterns (ripgrep vulnerability disclosure, ast-grep multi-track install, modelcontextprotocol/servers LLM framing) but doesn't need to re-research them. The plugin README is shorter and narrower; the pattern carry-overs are direct.

The main new consideration: **plugin-specific READMEs on the marketplace.** A future research pass could check how plugin READMs in established Claude Code / Cursor marketplaces structure their trust stories, but this is a small win — the root README's research already covers the patterns that matter.

## Ready for Stage 4

Think Tank next — exemplars for plugin READMEs specifically. Most of the patterns come from the root README's research; the plugin-specific addition will be a quick scan of well-regarded Claude Code plugin READMEs to confirm the threat-matrix pattern isn't redundant.

Proceeding to **Stage 4: Think Tank** on the plugin.