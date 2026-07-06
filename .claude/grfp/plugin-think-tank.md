# Think Tank — yactt plugin

> Stage 4 of the GRFP workflow for `plugins/yactt/README.md`.
> Generated 2026-07-05.

**Mode**: Web research via WebFetch (WebSearch unavailable — same constraint as the root README workflow).
**Counterpart reports**: `.claude/grfp/plugin-deep-dive.md`, `.claude/grfp/plugin-crystal-ball.md`, `.claude/grfp/plugin-brain-jam.md`, `.claude/grfp/think-tank.md` (root README research).

---

## 1. Methodology + fallbacks

- `WebSearch` was unavailable this session (400 errors throughout).
- `WebFetch` worked against raw GitHub URLs — three plugin-adjacent exemplars fetched and parsed.
- The root README's Think Tank (5 exemplars: ripgrep, ast-grep, fd, golangci-lint, ruff, mcp/servers) carries over directly — most patterns apply unchanged.
- **Plugin-specific research addition**: filesystem MCP server (specific instance), mise (curl|sh install pattern), sigstore/cosign (binary verification).

Projects sampled:

| Project | Why | Verdict |
|---|---|---|
| **modelcontextprotocol/servers/src/filesystem** | Specific MCP server in the official registry; closest cousin | Adopt: explicit "directory access control" + annotation hints |
| **mise** (jdx/mise) | Self-bootstrapping dev-tool installer | Adopt: `curl | sh` as canonical install; **avoid** for trust patterns |
| **sigstore/cosign** | Binary verification + signing toolchain | Adopt: signing-by-digest, threat matrix honesty |

---

## 2. Pattern matrix (plugin additions to the root README's research)

| Pattern | filesystem-mcp-server | mise | cosign | yactt's pick |
|---|---|---|---|---|
| Auto-execute on first use | `npx -y @…` (downloads + runs) | `curl \| sh` | `go install` | Already chosen — SessionStart hook |
| Security/trust section | yes (allowed dirs) | **no** (no verification, no SHA) | yes (keyless signing, KMS, transparency log) | **yes — threat matrix** (centerpiece) |
| Sandboxing boundary | yes (allowed-directories) | n/a | n/a | implicit (MCP server has read-only tools) |
| MCP tool annotation hints | yes (readOnly/idempotent/destructive) | n/a | n/a | **no — yactt's tools are uniformly read-only; one explicit bullet beats 11 hint rows** |
| "Sign by digest, not tag" | n/a | n/a | yes | **already aligned** — yactt's TOFU records `(version, sha256)` not version alone |
| Air-gapped verification | n/a | n/a | yes | **out of scope** but mentioned in crystal-ball § 6 as a future |
| Public release-attestation verification | n/a | n/a | yes (`cosign verify`) | **deferred** — bootstrap doesn't yet run `gh attestation verify` |
| Honest "what doesn't catch" disclosure | no | n/a | yes (Caveats + Known race conditions) | **yes — threat matrix row "unknown-version malicious release → manual verification only"** |
| Self-referential verification (the tool verifies itself) | n/a | n/a | yes (cosign signs its own releases) | **aspirational** — yactt's bootstrap could call `gh attestation verify` to verify itself |
| Update cadence / release notes | no | no | yes ("Release Cadence" section) | **deferred** — the bootstrap upgrades on version mismatch; release cadence note belongs in the root README's Status section |

---

## 3. Anti-patterns (what NOT to copy)

| Pattern | Why it's attractive | Why it would hurt the plugin README |
|---|---|---|
| **280-line comprehensive README** (filesystem-mcp) | thorough | Wrong audience — plugin readers want install + integrity in < 60 lines; depth-3 readers drill into the root README |
| **`curl \| sh` with no verification** (mise) | "Just works" | Defeats the entire integrity-model value prop. yactt's existence in plugin form is *because* bootstrap-without-verification is wrong. The threat matrix exists to distinguish yactt from mise. |
| **Auto-execute without warning** (filesystem-mcp's `npx -y`) | convenient | yactt's SessionStart hook is *also* auto-executing — the README must be upfront about it. The threat matrix is exactly that honesty. |
| **Sponsor block** (mise) | appropriate for BDFL projects | premature for yactt |
| **Cargo-cult transparency-log details** (cosign) | thorough for sigstore | overkill — TOFU + SHA256SUMS is the right size for yactt's threat model |

---

## 4. What yactt should steal (and how)

### 4.1 From filesystem-mcp-server — Sandboxing boundary as a one-liner

filesystem-mcp reads Allowed-directories at startup and refuses everything else. yactt's analog is the "read-only by design" claim: every MCP tool is read-only or analysis-only; `edit_impact` does not apply changes.

The current root README has this as a trust bullet; the plugin README should echo it in a one-line form, not a section. Phase: lift the existing claim, don't add new structure.

### 4.2 From sigstore/cosign — "Sign by digest" framing

Cosign strongly recommends signing by `@sha256:digest` rather than by tag. yactt's TOFU records `(version, sha256)` — the digest is the ground truth, the tag is the human-readable name. The threat matrix can be sharpened by leaning on this framing:

> "Same-version replay defense: TOFU records the binary's **digest**, not just its version. A release at the same tag with a different SHA256 is refused."

One sentence, and it preempts the "but what if a maintainer's account is compromised and ships a new version?" follow-up question.

### 4.3 From sigstore/cosign — "Caveats" / known limitations section

Cosign has an explicit "Caveats" section that lists known limitations including a race condition. yactt's threat matrix already has a "what it doesn't catch" row (unknown-version malicious release). The pattern: own the limitation, point to the mitigation path.

### 4.4 From filesystem-mcp-server — MCP tool annotation hints (NOT applied)

filesystem-mcp annotates each of its 13 tools with `readOnlyHint` / `idempotentHint` / `destructiveHint`. yactt's 10 tools are uniformly read-only or analysis-only — emitting per-tool annotations is overkill. The right move is one explicit bullet in the trust section.

### 4.5 From mise — `curl | sh` shape (NOT the verification posture)

The `curl mise.run | sh` shape is the canonical self-bootstrapping install. yactt's equivalent is the SessionStart hook running `install.sh`. The shape transfers; the verification posture does not. Mention this in a sentence: "If you prefer `curl` over the plugin marketplace, see the [Direct CLI] link."

---

## 5. Composition for the plugin README

Pulling the additions together with the brain-jam's structure:

| Section | Lines | Sources |
|---|---|---|
| Title + tagline | 3 | brain-jam § 5 hook |
| **Quickstart** | ~10 | ast-grep (multi-track install) — 2-line copy-paste |
| **What this plugin does** | ~12 | deep-dive § 4 inventory |
| **The integrity model** | ~25 | cosign (caveats + sign-by-digest framing); yactt's existing threat matrix preserved |
| **The `code-explore` skill** | ~12 | filesystem-mcp's "no warning" anti-pattern correction; SKILL.md frontmatter quoted |
| **For developers working on yactt itself** | ~8 | deep-dive § 7.2 (escape hatch) |
| **Update & uninstall** | ~6 | mise shape (without the verification gap) |
| **The 10 tools** | ~12 | compact; link to root README |
| **Trust + Read more** | ~10 | pointer, not duplication |

**Target length**: 100–120 lines.

---

## 6. Things that did NOT make the cut

These came up in the research but were rejected:

| Idea | Source | Why rejected |
|---|---|---|
| Per-tool read-only/idempotent/destructive annotation table | filesystem-mcp | yactt's tools are uniformly read-only; one bullet beats 11 rows |
| `cosign verify`-equivalent command in the bootstrap | cosign | Deferred — `gh attestation verify` is the chosen path (root README's roadmap) |
| Air-gapped verification story | cosign | Out of scope for MVP; flagged in crystal-ball § 6 |
| Sponsor block | mise | No sponsors |
| Cute install tip at the top | mise | Wrong tone for a security-focused plugin |

---

## 7. Open question for Stage 5 (Pen Wielding)

The brain-jam chose to lift the threat matrix into depth-1 content (first 60 lines). The crystal-ball noted the threat model's biggest gap is "unknown-version malicious release → manual verification only."

**Should the threat matrix call out the gap explicitly, or leave it as an implicit "what it doesn't catch" line?**

- Calling it out: stronger honesty, slightly longer matrix.
- Implicit: shorter matrix, risk of reader missing the gap.

**Recommendation for Stage 5**: call it out, in one row. The cosign "Caveats" pattern supports this.

---

## 8. Stage 4 confidence + coverage

- 3 plugin-adjacent exemplars fetched (filesystem-mcp, mise, cosign). All three successful.
- Patterns reused: most of the root README's research transfers unchanged.
- Additions: sign-by-digest framing, explicit threat-model caveat row, ecosystem-level context (yactt sits between mise's "no verification" anti-pattern and cosign's "sigstore-grade verification" aspirational).
- Brain-jam's structure survives intact; Stage 5 (Pen Wielding) should compose the README without redoing the structure work.

## Ready for Stage 5

Pen Wielding next — write the plugin README. Brain-jam's structure + Stage 4's additions (sign-by-digest framing, explicit threat-model caveat row) are the inputs.

Proceeding to **Stage 5: Pen Wielding** on the plugin.