# yactt — Claude Code plugin

**Federated code intelligence with a verifiable install story.**

The yactt plugin installs the [`yactt`](../..) MCP server on first use, wires it into Claude Code, and ships a `code-explore` skill that teaches the agent the canonical exploration flow. No Go toolchain required.

---

## Quickstart

### Claude Code

```bash
/plugin marketplace add kellenff/yactt
/plugin install yactt@yactt
```

That's it. The `SessionStart` hook downloads the matched binary from the latest GitHub release on first use; later sessions are a no-op when the installed version is current. Requires `jq` (`brew install jq` / `apt install jq` otherwise).

### Pi agent

```bash
pi install git:github.com/kellenff/yactt
```

The Pi extension (`pi-extension/index.js`) reuses this plugin's installer — same TOFU + SHA256 chain. On first session it bootstraps `yactt` if missing, ensures [`pi-mcp-adapter`](https://github.com/nicobailon/pi-mcp-adapter) is installed, and registers the MCP server. Subsequent sessions are no-ops.

---

## Security

The install-hook trust chain (SHA256 + SLSA L3 + TOFU) lives in
[§ The integrity model](#the-integrity-model) below. The full
threat model — grammar pinning, release attestation, LSP
subprocess boundary, doc-comment / identifier-name injection
gates, application-level audit log — is documented in
[docs/security.md](../../docs/security.md).

To report a vulnerability, see [SECURITY.md](../../SECURITY.md).

---

## What this plugin does

1. **Installs `yactt`** to `$XDG_HOME/bin/yactt` (or `$HOME/.local/bin/yactt` when `XDG_HOME` is unset). Both are XDG-conventional and on most shells' `PATH`.
2. **Wires the MCP server** into the Claude Code session at `${CLAUDE_PROJECT_DIR}` via `.mcp.json` (30 s request timeout, generous for first-time LSP cold-start).
3. **Activates the `code-explore` skill**, which triggers on prompts about exploring, summarizing, or reviewing code. The skill teaches the agent the canonical tool progression — see [§ The `code-explore` skill](#the-code-explore-skill).

After install Claude has 10 new MCP tools (prefix `mcp__plugin_yactt_yactt__`) plus the skill.

---

## The integrity model

Every release starting at the next tag ships with **SLSA Build Provenance Level 3** attestations. The bootstrap (`scripts/install.sh`) is the front-line defense; the attestations are the back-line.

| Threat | Defense | Catches? |
|---|---|---|
| Transport corruption / partial download | SHA256 in published `SHA256SUMS` | **Yes** |
| Same-version replay (binary swapped, hash swapped) | TOFU on `${XDG_DATA_HOME:-~/.local/share}/yactt/known-good` records `(version, sha256)`; same version + different sha is refused | **Yes** |
| Malicious new release at a higher version (latest-wins upgrade) | None automated | **No** — verify manually until § Future |
| TOFU file tampering | Lives in user-owned `$XDG_DATA_HOME`; tampering requires local root | **Yes (inherited)** |

Verify any release tarball manually with one command:

```bash
gh attestation verify yactt_darwin_arm64.tar.gz -R kellenff/yactt
```

The bootstrap itself doesn't yet run `gh attestation verify` — that's a tracked follow-up (see [§ Future](#future)). Until then, manual verification before upgrading to a release you don't recognize is the right habit.

> **Why "sign by digest"?** The TOFU record stores the binary's `sha256`, not just its version. A release at the same tag with a different digest is refused — that's the only way to catch "compromised GitHub release at the same version."

### Runtime TOFU check (yactt side)

At every `mcp serve` startup, yactt reads the TOFU record your hook wrote and compares its running binary's SHA-256 against the recorded hash at the same version. A mismatch writes a `WARNING:` line on stderr:

```
WARNING: yactt binary SHA-256 does not match the install hook's TOFU record
  recorded: 4a3b...
  actual:   1f2c...
  version:  v0.1.0
  likely a replay or compromised release — refusing to trust the install
```

A version mismatch (legitimate upgrade) is silent; missing TOFU (dev install) is silent; `dev` builds skip the check entirely. See [`docs/security.md`](../../docs/security.md) §6 (AST09 / Issue #3) for the full threat model.

### Optional per-tool audit log

```bash
yactt mcp serve --audit-log=/var/log/yactt/audit.log
```

Writes one JSON line per `tools/call`. The startup line (binary SHA-256, resolved root, MaxFiles cap, grammars, LSP status) always lands on stderr regardless of this flag — the host can cross-check the binary without `audit-log` being on.

---

## The `code-explore` skill

The skill's frontmatter (verbatim, from [`skills/code-explore/SKILL.md`](skills/code-explore/SKILL.md)):

> Use when exploring, understanding, summarizing, or reviewing a Go/TypeScript/JavaScript/Python codebase. Walks the tree with YACTT (`tree_overview`), drills into symbols (`find_symbol`, `node_get`), traces references (`node_edges`, `find_referencing_symbols`), and assesses rename impact (`edit_impact`).

The skill teaches the agent the 8-step canonical progression:

1. **`tree_overview`** — map the top of the repo. Start here.
2. **`get_symbols_overview`** — top-level outline of a specific file.
3. **`search`** / **`find_symbol`** — name- or doc-comment-based lookups.
4. **`node_get`** — pull layers of a node by its stable id.
5. **`node_source`** — lossless source, optionally line-bounded.
6. **`node_edges`** / **`find_referencing_symbols`** — callers, callees, tests, overrides, imports.
7. **`find_code`** — AST-aware or regex patterns.
8. **`edit_impact`** — always before any rename. Returns impact, does not apply.

---

## For developers working on yactt itself

If you're hacking on `yactt` and want the plugin to use your local build instead of the latest release, drop the binary into `<repo>/bin/yactt` and make it executable:

```bash
go build -o bin/yactt ./cmd/yactt
chmod +x bin/yactt
```

The SessionStart hook checks `${CLAUDE_PROJECT_DIR}/bin/yactt` first and short-circuits the network bootstrap when present. No upgrade prompts, no API calls, no install side effects. The full hook behavior lives in [`scripts/install.sh`](scripts/install.sh).

---

## Update & uninstall

The bootstrap upgrades `yactt` automatically when a newer release ships; the next SessionStart picks it up.

To uninstall:

```bash
rm "$(command -v yactt)"                                          # the binary
rm "${XDG_DATA_HOME:-$HOME/.local/share}/yactt/known-good"        # the TOFU record
```

Optional: clear the GitHub API cache with `rm "${XDG_CACHE_HOME:-$HOME/.cache}/yactt/latest"`.

---

## The 11 tools

`tree_overview`, `node_get`, `node_source`, `node_edges`, `search`, `find_symbol`, `get_symbols_overview`, `find_code`, `search_code`, `find_referencing_symbols`, `edit_impact` — and one bonus: `persisted_query` for curated workflows.

See the [project README](../../README.md#the-20-tools) for the full table with one-line descriptions and the framing ("20 facets of one node graph"). The plugin's tools are a subset of that set; the prefix `mcp__plugin_yactt_yactt__` is the only difference.

> `edit_impact` does not apply renames — it only analyses the blast radius. Every tool is read-only or analysis-only.

---

## Trust

- **Dual-licensed** under [Apache-2.0](../../LICENSE-APACHE) or [MIT](../../LICENSE-MIT).
- **SLSA Build Provenance Level 3** on every release; manual `gh attestation verify` supported.
- **Read-only by design** — no MCP tool writes to your repo. `edit_impact` is analysis-only. `yactt mcp serve` makes no outbound network calls except to spawn `gopls` / `typescript-language-server` as child processes.

For the full trust posture, including tree-sitter-as-floor and bounded resources, see the [project README's Trust section](../../README.md#trust).

---

## Future

Tracked on the plugin:

- `gh attestation verify` invoked inside the bootstrap (today: manual only).
- Windows support (today: bootstrap only handles Darwin / Linux).
- Additional architectures beyond `arm64` / `amd64`.
- Upgrade rollback (today: bootstrap always upgrades to `latest`).

---

## Read more

- Project README — [../../README.md](../../README.md)
- Architecture deep dive — [../../docs/design.md](../../docs/design.md)
- `code-explore` skill — [skills/code-explore/SKILL.md](skills/code-explore/SKILL.md)
- Latest release — [github.com/kellenff/yactt/releases/latest](https://github.com/kellenff/yactt/releases/latest)