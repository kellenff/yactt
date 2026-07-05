# yactt Claude Code plugin

Federated code-intelligence MCP server + `code-explore` skill for Claude Code.

## Install

The SessionStart hook bootstraps the `yactt` binary from the latest GitHub Release on first use — no Go toolchain required. The hook fetches the platform-matched tarball, verifies its SHA256 against the published `SHA256SUMS`, and installs to `$XDG_HOME/bin/yactt` (or `$HOME/.local/bin/yactt` when `XDG_HOME` is unset). Subsequent sessions check the version and upgrade whenever a newer release is out.

Requires `jq` (standard on macOS/Linux developer machines; `brew install jq` / `apt install jq` otherwise).

```bash
# Add this repo as a marketplace (local path or remote URL)
/plugin marketplace add /Users/kellen/Projects/yactt
# or, once the repo is on GitHub:
/plugin marketplace add kellenff/yactt

# Install the plugin
/plugin install yactt@yactt
```

That's it. The SessionStart hook downloads and installs the `yactt` binary on first use; later sessions are a no-op when the installed version is current.

If `$XDG_HOME/bin` (or `$HOME/.local/bin`) is not on your `PATH`, the bootstrap prints a one-line instruction for adding it. The MCP server fails loudly on startup if `yactt` can't be reached either way.

## Integrity model (read this)

The bootstrap's `SHA256SUMS` check is **corruption detection**, not supply-chain integrity. The manifest is fetched from the same origin as the binary, so a compromised release endpoint can ship a malicious tarball with a matching hash. What the bootstrap actually catches:

- **Transport corruption / partial downloads** — SHA256 mismatch in SHA256SUMS (corruption-detection).
- **Replay or replace at the same version** — TOFU check against `${XDG_DATA_HOME:-${HOME}/.local/share}/yactt/known-good`. After the first successful install, a same-version release with a different sha256 in `SHA256SUMS` is refused. Closes the silent-update threat from a compromised GitHub release endpoint *for the same version*.
- **Unknown-version upgrades** — a malicious new version is treated as a legitimate upgrade (TOFU doesn't cover version-bumped releases).

What the bootstrap does NOT catch (and why):

- A malicious new release — TOFU is bypassed on every version bump, since the user's stated choice was "latest wins".
- A compromised TLS endpoint — `https://github.com` is the only transport, but `github.com` itself isn't pinned.

For full supply-chain integrity (catching malicious new versions, too), the manifest needs an out-of-band signature. Plan: sign `SHA256SUMS` with `cosign` or `minisign` during release, bundle the public key in `plugins/yactt/`, and verify the signature in the bootstrap before trusting the manifest. Until that's in place, treat the SHA256 check as a corruption guard, not a security boundary.

## Use

After install, Claude has 10 new MCP tools (prefix `mcp__plugin_yactt_yactt__`) and one skill (`code-explore`) that triggers on prompts about exploring, summarizing, or reviewing code.

Example:

> Summarize this repo.

Claude should call `tree_overview` first, then drill in with `find_symbol`, `node_get`, and `node_source` as needed.

## The 10 tools

| Tool | Purpose |
| --- | --- |
| `tree_overview` | Top of the repo tree, depth-limited |
| `node_get` | One or more layers of a node (summary, signature, body, source, tokens) |
| `node_source` | Lossless source, optionally line-bounded |
| `node_edges` | Callers, callees, tests, overrides, imports |
| `search` | Symbols by name or doc-comment |
| `find_symbol` | Locate by qualified name path with glob support |
| `get_symbols_overview` | Top-level outline of a file |
| `find_code` | AST-aware (tree-sitter) or regex patterns |
| `find_referencing_symbols` | All references to a symbol |
| `edit_impact` | Analyze rename impact — does NOT apply |

## Direct CLI

Outside Claude Code, download the latest release directly:

```bash
# macOS arm64:
curl -fsSL https://github.com/kellenff/yactt/releases/latest/download/yactt_darwin_arm64.tar.gz \
  | tar -xz -C /usr/local/bin yactt_darwin_arm64 && mv /usr/local/bin/yactt_darwin_arm64 /usr/local/bin/yactt

# then
yactt overview .         # tree dump for the current repo
yactt mcp serve [path]   # MCP server over stdio, rooted at path
```

See the [latest release](https://github.com/kellenff/yactt/releases/latest) for all platform tarballs.

## License

Dual-licensed under [Apache-2.0](../../LICENSE-APACHE) or [MIT](../../LICENSE-MIT).