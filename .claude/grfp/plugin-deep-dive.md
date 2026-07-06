# Deep Dive — yactt Claude Code plugin

> Stage 1 of the GRFP workflow for `plugins/yactt/README.md`.
> Generated 2026-07-05.

**Graph tools available:** Yes (codebase-memory-mcp, same index as the root project).
**Method used**: graph (primary); all six plugin files read via `get_code_snippet`.
**Project ID:** `Users-kellen-Projects-yactt` (single index, plugin files visible as `plugins/yactt/...`).

---

## 1. Identity

| | |
|---|---|
| **Path** | `plugins/yactt/` |
| **Name** | `yactt` (per `.claude-plugin/plugin.json`) |
| **Version** | `0.1.0` |
| **Description** | "Federated code intelligence: walk the tree, choose your layer." |
| **Author** | Kellen Frodelius-Fujimoto |
| **License** | Apache-2.0 |
| **Keywords** | `code-intelligence`, `tree-sitter`, `mcp` |

The plugin ships a curated Claude Code experience around the `yactt` MCP server. The plugin README is one of the project's two READMEs (the other is the root `README.md`); it targets a different audience — Claude Code users who don't want to touch the CLI or MCP wire format.

**Sources**: `plugins/yactt/.claude-plugin/plugin.json`; `plugins/yactt/README.md:1–2`.

---

## 2. Problem solved

Most Claude Code users would never wire `mcp serve` into their `.mcp.json` manually, build from source, or upgrade a binary. The plugin abstracts that into:

1. One `SessionStart` hook that bootstraps the binary.
2. One `.mcp.json` that wires it.
3. One skill (`code-explore`) that teaches the agent how to use it.
4. One README that explains the integrity model (the part that scares people who care about supply-chain risk).

**Source**: `plugins/yactt/hooks/hooks.json` (SessionStart trigger); `plugins/yactt/.mcp.json` (server wiring); `plugins/yactt/skills/code-explore/SKILL.md` (agent teaching).

---

## 3. Audience

- **Primary**: Claude Code users who want `mcp serve` running without reading the root README.
- **Secondary**: Developers who care about supply-chain integrity — the integrity model is the centerpiece, not an afterthought.
- **Tertiary**: Plugin maintainers — `install.sh` is its own document.

**Source**: `plugins/yactt/README.md:21–35` (Integrity model section).

---

## 4. Core components (the 6 files)

### 4.1 `.claude-plugin/plugin.json`

The plugin manifest. Eight lines:

```json
{
  "$schema": "https://anthropic.com/claude-code/plugin.schema.json",
  "name": "yactt",
  "description": "Federated code intelligence: walk the tree, choose your layer.",
  "version": "0.1.0",
  "author": { "name": "Kellen Frodelius-Fujimoto" },
  "license": "Apache-2.0",
  "keywords": ["code-intelligence", "tree-sitter", "mcp"]
}
```

Defines name, description, version, author, license. The `keywords` array is the only metadata that affects discovery.

**Source**: `plugins/yactt/.claude-plugin/plugin.json`.

### 4.2 `.mcp.json`

MCP server wiring:

```json
{
  "mcpServers": {
    "yactt": {
      "type": "stdio",
      "command": "yactt",
      "args": ["mcp", "serve", "${CLAUDE_PROJECT_DIR}"],
      "timeout": 30000
    }
  }
}
```

`${CLAUDE_PROJECT_DIR}` is the plugin-runtime variable that resolves to the user's current project root. The 30-second timeout is generous — tree-sitter parses are cheap; LSP cold-start can take 5+ seconds per file on the first request.

**Source**: `plugins/yactt/.mcp.json`.

### 4.3 `hooks/hooks.json`

Single `SessionStart` hook that runs `install.sh`:

```json
{
  "hooks": {
    "SessionStart": [{
      "matcher": "",
      "hooks": [{
        "type": "command",
        "command": "INSTALL=\"${CLAUDE_PLUGIN_ROOT:-${CLAUDE_PROJECT_DIR}/plugins/yactt}/scripts/install.sh\"; if [ -x \"$INSTALL\" ]; then bash \"$INSTALL\"; else echo \"yactt: bootstrap script not found at $INSTALL\" >&2; fi"
      }]
    }]
  }
}
```

`${CLAUDE_PLUGIN_ROOT}` is the plugin's install path; falls back to `${CLAUDE_PROJECT_DIR}/plugins/yactt` for local marketplace development. Empty `matcher` means every session.

**Source**: `plugins/yactt/hooks/hooks.json`.

### 4.4 `scripts/install.sh` — the heart of the plugin

192 lines of bash, well-commented. The interesting behaviors:

1. **Install path**: `$XDG_HOME/bin/yactt` if `XDG_HOME` is set, otherwise `$HOME/.local/bin/yactt`.
2. **Developer escape hatch**: `${CLAUDE_PROJECT_DIR}/bin/yactt` wins if it exists and is executable. So a developer hacking on yactt can drop a local build into `<repo>/bin/` and the hook short-circuits — no network, no upgrade.
3. **TOFU record**: `${XDG_DATA_HOME:-~/.local/share}/yactt/known-good`. Stores `(version, sha256)` pairs. On every subsequent install, if the version is the same but the SHA256 differs, refuse (catches same-version replay attacks).
4. **GitHub API cache**: `${XDG_CACHE_HOME:-~/.cache}/yactt/latest`, 1-hour TTL. Caps unauthed API at ~24 calls/day.
5. **Semver allowlist**: refuses any non-semver tag returned by the API — protects against a poisoned cache / API response that would otherwise turn into a 404 download attempt.
6. **SHA256SUMS fetched before the binary**: if TOFU refuses, you don't waste a 100+ MB download.
7. **Cross-platform matrix**: Darwin/Linux + arm64/amd64.
8. **Soft PATH warning**: prints a one-line instruction if `$INSTALL_DIR` isn't on PATH, but doesn't fatal — the MCP server fails loudly on startup if `yactt` is unreachable.

The script also has `set -euo pipefail`, treats errors carefully, and uses `trap "rm -rf '${tmpdir}'" EXIT` for clean teardown.

**Source**: `plugins/yactt/scripts/install.sh` (all 192 lines).

### 4.5 `skills/code-explore/SKILL.md`

A 28-line skill. Its frontmatter is the trigger:

> Use when exploring, understanding, summarizing, or reviewing a Go/TypeScript/JavaScript/Python codebase. Walks the tree with YACTT (tree_overview), drills into symbols (find_symbol, node_get), traces references (node_edges, find_referencing_symbols), and assesses rename impact (edit_impact).

The body teaches the canonical 8-step progression:

1. `tree_overview` (always first, tune `depth` 1–6)
2. `get_symbols_overview` for known files
3. `search` / `find_symbol` for name lookups
4. `node_get` with the stable id from steps 1–3
5. `node_source` for lossless source
6. `node_edges` / `find_referencing_symbols` for call/reference graphs
7. `find_code` for AST patterns
8. `edit_impact` (ALWAYS before renames)

The "Layer discipline" subsection teaches the agent to start with `summary` and only escalate to `body` / `source` when needed. The "Don't" subsection is the *negative* rule book — the same parse-don't-validate discipline that drives the wire contract.

**Source**: `plugins/yactt/skills/code-explore/SKILL.md`.

### 4.6 `README.md` — the existing 86-line README

Five sections:

1. **Install** — plugin marketplace add + install command.
2. **Integrity model (read this)** — SLSA L3 attestations + what SHA256SUMS + TOFU catches today and what they don't.
3. **Use** — one example prompt ("Summarize this repo.") + how the agent should respond.
4. **The 10 tools** — table.
5. **Direct CLI** — curl-tarball recipe for non-Claude-Code users.
6. **License** — dual.

Strengths: concise, has the integrity model centerpiece, the 10-tool table matches the root README's table.

Weaknesses: misses the developer escape hatch, doesn't call out the `code-explore` skill (the agent-teaching file), doesn't link to the root README, and the "Use" section is too thin.

**Source**: `plugins/yactt/README.md`.

---

## 5. Architecture

```
SessionStart ──► hooks/hooks.json
                     │
                     ▼
             scripts/install.sh  (192 lines, set -euo pipefail)
                     │
                     ├── check ${CLAUDE_PROJECT_DIR}/bin/yactt  (dev escape hatch)
                     │
                     ├── fetch latest release tag from GitHub API
                     │     (cached in $XDG_CACHE_HOME/yactt/latest, 1h TTL)
                     │
                     ├── fetch SHA256SUMS
                     │
                     ├── TOFU check against
                     │     $XDG_DATA_HOME/yactt/known-good
                     │     (refuse on version-equal + sha-different)
                     │
                     ├── download yactt_<os>_<arch>.tar.gz
                     │
                     ├── verify shasum -a 256
                     │
                     ├── install -m 0755 → $INSTALL_PATH
                     │
                     ├── record (version, sha) to known-good
                     │
                     └── PATH warning (soft)

Once installed:
                 $ yactt mcp serve ${CLAUDE_PROJECT_DIR}
                                │
                                ▼
                       (yactt root binary)
                                │
                                ▼
                       10 MCP tools + persisted_query
                       available to the agent as
                       mcp__plugin_yactt_yactt__*
                                │
                                ▼
                    code-explore skill triggers
                    on "explore / summarize / review" prompts
```

**Source**: composed from the four files above.

---

## 6. Trust model — what the plugin README leans on

The integrity model section is the README's most distinctive feature. It distinguishes three threat models and what defends against each:

| Threat | Defense | Catches? |
|---|---|---|
| Transport corruption / partial download | SHA256 in `SHA256SUMS` | ✓ yes |
| Same-version replay (binary swap at the same version) | TOFU check on `${KNOWN_GOOD_FILE}` | ✓ yes |
| Unknown-version malicious release (latest-wins upgrade) | nothing in the bootstrap | ✗ no — manual `gh attestation verify` required |
| Future: closed-loop verification | `gh attestation verify` in the bootstrap | tracked as follow-up |

**Source**: `plugins/yactt/README.md:21–42`; `scripts/install.sh:14–22`.

---

## 7. What makes this plugin README unique

1. **The integrity model is the centerpiece.** Most plugin READMes assume the user trusts the marketplace. This one explicitly walks the reader through "what catches what" — SLSA L3 + SHA256SUMS + TOFU.
2. **The developer escape hatch.** A developer working on yactt itself can drop a local build into `<repo>/bin/yactt` and the SessionStart hook short-circuits. This isn't surfaced in the current README — it's only in the `install.sh` comment.
3. **The skill does the teaching, not the README.** The 8-step tool progression lives in `code-explore/SKILL.md` frontmatter + body, where the agent actually reads it. The README's "Use" section just shows one example prompt.
4. **Single source of truth for the install path.** `${XDG_HOME}` or `$HOME/.local`, no third fallback, and the script clearly documents the choice. No `mkdir -p /usr/local/bin` arguments.

**Source**: composed from the files above.

---

## 8. Gaps in the current README

These are the candidates for Stage 5 (Pen Wielding):

| Gap | Severity |
|---|---|
| No mention of the developer escape hatch (`bin/yactt` shortcut) | Medium — plugin authors will hit this |
| No link to the root README for the architectural / positioning story | Medium — root README has the empty-quadrant framing |
| No mention of the `code-explore` skill frontmatter (what triggers it) | Low — but the skill is the canonical teaching surface |
| `Use` section is too thin (one example prompt) | Low |
| No "What gets installed / where" section | Low — could be folded into install |
| No "Update / uninstall" story | Low — the bootstrap upgrades on version mismatch; uninstall is `rm $INSTALL_PATH` + `rm $KNOWN_GOOD_FILE` |
| No mention of supported OS/arch matrix | Low — already in `install.sh` comments |

---

## 9. Things NOT in the plugin

- No marketplace manifest beyond `plugin.json` — the marketplace is implicit (the repo IS the marketplace when added via local path or remote URL).
- No release-notes file — changes live in the root `CHANGELOG` (if it exists; the design doc is the source of truth today).
- No CI for the plugin specifically — the root `.github/workflows/ci.yml` and `release.yml` are the only CI.

---

## 10. Comparison with the root README

| Concern | Root README | Plugin README |
|---|---|---|
| Positioning (empty quadrant) | yes | no — pointers expected |
| 10-tool table | yes | yes (slightly different framing) |
| Install paths | 3 (plugin, MCP, CLI) | 1 (plugin) + 1 fallback (CLI) |
| Integrity model | yes (gh attestation one-liner) | **yes, expanded** (TOFU, SHA256SUMS, threat matrix) |
| Architecture diagram | yes (ASCII tree) | no — pointer expected |
| Trust section | yes (4 bullets) | yes (more detailed) |
| SLSA L3 attestation | yes (one-liner) | yes (one-liner + threat matrix) |

The two READMs are *complementary*, not duplicative. The root README is the project's README; the plugin README is the *user-facing install story*.

**Source**: comparison of `README.md` and `plugins/yactt/README.md`.

---

## 11. Ready for Stage 2

Crystal Ball next — what could this plugin become? The visible threads:

- `gh attestation verify` baked into the bootstrap (today: documented but not enforced).
- Upgrade-rollback story (downgrade to a known-good version).
- Per-platform extension (Windows support, additional architectures).
- Multi-plugin marketplace packaging (the plugin could be one of several in a larger marketplace).
- Skill versioning (the `code-explore` skill should track yactt's MCP version).

Proceeding to **Stage 2: Crystal Ball**.