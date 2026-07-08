# yactt (Junie)

Federated code intelligence for Junie — the third harness for the [yactt](https://github.com/kellenff/yactt) MCP server, parallel to the [Claude Code plugin](../plugins/yactt/) and the [Pi extension](../pi-extension/).

## What you get

- **21 MCP tools** — symbol search, call-graph traversal, AST-aware code search, rename-impact (blast radius), architecture / hotspots / dead-code detection, git-diff impact.
- **A `code-explore` skill** — agent-facing guide on how to use the tools (ladder, decision matrix, gotchas). Junie loads `SKILL.md` automatically.
- **Multi-language** — Go, TypeScript, JavaScript, Python. Tree-sitter is the unconditional floor; `gopls`, `typescript-language-server`, and `pyright-langserver` wire up opportunistically for resolved types.

## Install

1. **Install yactt** — fetched, SHA-256 verified, TOFU-recorded:
   ```bash
   bash plugins/yactt/scripts/install.sh
   ```
2. **Symlink the launcher** onto your `PATH` (Junie's MCP config can't substitute a project-dir variable, so the launcher auto-detects by walking up from `cwd`):
   ```bash
   ln -s "$(pwd)/junie-extension/scripts/yactt-launcher.sh" ~/.local/bin/yactt-launcher
   ```
3. **Drop the extension into Junie's extensions directory:**
   ```bash
   ln -s "$(pwd)/junie-extension" ~/.junie/extensions/yactt
   ```
   Project-local layout (`<repo>/.junie/extensions/yactt/`) also works if Junie picks that up.

The launcher walks up from `cwd` looking for `.git` and falls back to `cwd` for non-git workspaces. Setting `YACTT_LAUNCHER_DRY_RUN=1` prints the resolved path instead of exec'ing yactt (used by the test).

## Files

- `extension.json` — Junie metadata (name + description).
- `mcp/.mcp.json` — invokes `yactt-launcher`.
- `scripts/yactt-launcher.sh` — project-root walker + `exec yactt mcp serve <root>`.
- `skills/code-explore/SKILL.md` — usage guide (same content as the Claude skill, prefix swapped).
- `test/launcher.test.sh` — bash smoke test for the walker.

## Trust

Same chain as the other harnesses — `plugins/yactt/scripts/install.sh` fetches the release binary from GitHub, verifies its SHA-256 against `SHA256SUMS`, records TOFU for replay defence. See [`plugins/yactt/README.md`](../plugins/yactt/README.md) for the full story.
