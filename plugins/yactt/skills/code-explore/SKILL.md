---
description: Use when exploring, understanding, summarizing, or reviewing a Go/TypeScript/JavaScript/Python codebase. Walks the tree with YACTT (tree_overview), drills into symbols (find_symbol, node_get), traces references (node_edges, find_referencing_symbols), and assesses rename impact (edit_impact).
---

# Code exploration with YACTT

YACTT exposes 10 MCP tools (prefix `mcp__plugin_yactt_yactt__`). Use them in this order — every later step is cheaper with context from the earlier ones.

1. **`tree_overview`** — map the top of the repo. Always start here. Tune `depth` (1–6, default 2) and `include_layers` (`summary` / `structure` / `signature`).
2. **`get_symbols_overview`** — top-level outline of a specific file when you know the path.
3. **`search`** — find symbols by name or doc-comment; **`find_symbol`** — locate by qualified name path with glob support.
4. **`node_get`** — pull specific layers of a node (`summary`, `signature`, `body`, `source`, `tokens`) using the stable `id` from steps 1–3.
5. **`node_source`** — lossless source for a node, optionally bounded by line range.
6. **`node_edges`** / **`find_referencing_symbols`** — trace callers, callees, tests, overrides, imports.
7. **`find_code`** — AST-aware (tree-sitter) or regex patterns across files.
8. **`edit_impact`** — ALWAYS run before any rename. Returns impact, does not apply.

## Layer discipline

- `summary` is cheap, defaults are usually right.
- `body` and `source` are expensive — pull only the nodes you actually need.
- `tokens` is for budget reasoning, not for reading.

## Don't

- Don't ask for `node_get` without first getting a stable `id` from `tree_overview`, `search`, or `find_symbol`.
- Don't propose renames without `edit_impact` first.
- Don't pull the whole repo at `depth=6` — start shallow and drill.