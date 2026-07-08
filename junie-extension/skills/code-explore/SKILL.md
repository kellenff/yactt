---
description: Use when exploring, understanding, summarizing, or reviewing a Go/TypeScript/JavaScript/Python codebase. Triggers on: explore the codebase, navigate code, understand the architecture, who calls this function, what does X call, find callers, find callees, look up a symbol, find symbol definition, where is X used, trace the call chain, dead code, unused functions, high fan-out, refactor candidates, code quality audit, code hotspots, rename impact, blast radius, what will break, AST-aware search, call graph, review my changes, PR review impact. Walks the tree with YACTT (tree_overview), drills into symbols (find_symbol, node_get), traces references (node_edges, find_referencing_symbols), and assesses rename impact (edit_impact).
---

# Code exploration with yactt

yactt exposes 21 MCP tools (prefix `mcp__yactt__*`). Every later step is cheaper with context from the earlier ones — work the ladder top-down.

## Decision matrix (first 30 seconds)

| Question | First call |
|---|---|
| "Where is X defined?" | `find_symbol` (glob over name-path) |
| "Who calls X?" / "What does X call?" | `node_edges` with `kinds=["callers"\|"callees"]` |
| "Find anything matching `*Foo*`" | `search(query="Foo")` (BM25) or `find_code(pattern_kind="regex")` |
| "Will renaming X break anything?" | `edit_impact` (does NOT apply — returns impact only) |
| "What changed in this branch?" | `detect_changes` |
| "Dead code / hot spots / import cycles?" | `get_architecture` |

## The ladder (cheapest first)

1. **`tree_overview`** — map the repo. Tune `depth` (1–6, default 2) and `include_layers` (`summary` / `structure` / `signature`).
2. **`get_symbols_overview`** — top-level outline of a specific file when you know the path.
3. **`search`** (BM25 over name + doc-comment) / **`find_symbol`** (glob over name-path) — locate the target.
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
- Don't Grep when `search` (BM25, name-aware) or `find_code` (AST-aware) will do.

## Gotchas

1. `tree_overview(depth=6)` returns thousands of nodes — start at `depth=2`.
2. `edit_impact` does NOT apply renames — pair with Edit.
3. `search` is BM25 over name + doc-comment, not regex. For pattern matching use `find_code` with `pattern_kind="regex"`.
4. `node_get id` requires a stable id from tree_overview / search / find_symbol first.
5. `find_referencing_symbols` with `kinds=["mentions"]` is broad; default to `["calls"]` for call chains.
6. Persisted ops (`persisted_query(id="onboarding")`, `"architecture"`, `"repo-map"`) are one-call shortcuts — start there for a first-look snapshot.
