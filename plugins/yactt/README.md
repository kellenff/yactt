# yactt Claude Code plugin

Federated code-intelligence MCP server + `code-explore` skill for Claude Code.

## Install

Requires Go ≥ 1.26 on PATH — the SessionStart hook runs `go install` if `yactt` isn't already on PATH.

```bash
# Add this repo as a marketplace (local path or remote URL)
/plugin marketplace add /Users/kellen/Projects/yactt
# or, once the repo is on GitHub:
/plugin marketplace add kellenff/yactt

# Install the plugin
/plugin install yactt@yactt
```

That's it. The SessionStart hook builds and installs the `yactt` binary on first use; later sessions are a no-op.

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

Outside Claude Code:

```bash
go install github.com/kellenff/yactt/cmd/yactt@latest
yactt overview .         # tree dump for the current repo
yactt mcp serve [path]   # MCP server over stdio, rooted at path
```

## License

Dual-licensed under [Apache-2.0](../../LICENSE-APACHE) or [MIT](../../LICENSE-MIT).