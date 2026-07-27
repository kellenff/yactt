---
name: code-explore
description: Use when exploring, understanding, summarizing, or reviewing a Go/TypeScript/JavaScript/Python codebase.
---

# Code exploration with yactt

yactt exposes 21 MCP tools (prefix `mcp__plugin_yactt_yactt__*`). Every later step is cheaper with context from the earlier ones — work the ladder top-down.

## Decision matrix (first 30 seconds)

| Question | First call |
|---|---|
| "Where is X defined?" | `find_symbol` (glob over name-path; on miss check `suggestions`) |
| "Who calls X?" / "What does X call?" | `node_edges` with `kinds=["callers"\|"callees"]` (single hop) |
| "Transitive callers / callees" (multi-hop) | `query_graph` with `follow=["callers"]` and `depth=2-5` — one call replaces a loop of `node_edges` |
| "Find anything matching `*Foo*`" | `search(query="Foo")` (BM25) or `find_code(pattern_kind="regex")` |
| "Will renaming X break anything?" | `edit_impact` (does NOT apply — returns impact only) |
| "What changed in this branch?" | `detect_changes` |
| "Dead code / hot spots / import cycles?" | `get_architecture` |
| "What node kinds / edge kinds exist?" | `get_graph_schema` (call first when writing `query_graph` filters) |

## The ladder (cheapest first)

1. **`tree_overview`** — map the repo. Tune `depth` (1–6, default 2), `include_layers` (`summary` / `structure` / `signature`), and `scope` (absolute path under the repo root — narrows the walk to a package or subdir; same convention as `search`'s `scope`).
2. **`get_symbols_overview`** — top-level outline of a specific file when you know the path.
3. **`search`** (BM25 over name + doc-comment) / **`find_symbol`** (glob over name-path) — locate the target. On a `find_symbol` miss, the response includes a `suggestions` field with edit-distance matches — try those.
4. **`node_get`** — pull specific layers of a node (`summary`, `signature`, `body`, `source`, `tokens`) using the stable `id` from steps 1–3.
5. **`node_source`** — lossless source for a node, optionally bounded by line range.
6. **`node_edges`** / **`find_referencing_symbols`** — single-hop trace callers, callees, tests, overrides, imports. **For multi-hop, use `query_graph`** — one call replaces a loop.
7. **`query_graph`** — multi-hop reachability (`depth` 1–5, cost-capped). The right tool for "who transitively depends on X?" or "what does X transitively reach?".
8. **`find_code`** / **`search_code`** — AST-aware (tree-sitter) or regex patterns across files. `search_code` is best for "which functions handle *X*?".
9. **`edit_impact`** — ALWAYS run before any rename. Returns impact, does not apply.

## Layer discipline

- `summary` is cheap, defaults are usually right.
- `body` and `source` are expensive — pull only the nodes you actually need.
- `tokens` is for budget reasoning, not for reading.

## Truncation

List-returning tools emit `truncated:bool` and `totalCount:int` (or `visited:int` for `query_graph`) when the result was capped. **Always check `truncated`** before declaring a query exhausted — if true, raise the `limit` and re-call.

## Don't

- Don't ask for `node_get` without first getting a stable `id` from `tree_overview`, `search`, or `find_symbol`.
- Don't propose renames without `edit_impact` first.
- Don't pull the whole repo at `depth=6` — start shallow and drill.
- Don't Grep when `search` (BM25, name-aware) or `find_code` (AST-aware) will do.
- Don't loop `node_edges` for multi-hop — that's what `query_graph` is for.
- Don't hardcode node/edge kind strings in `query_graph` — call `get_graph_schema` first.

## Gotchas

1. `tree_overview(depth=6)` returns thousands of nodes — start at `depth=2`. Truncates at 16 KiB; reduce depth if you see the warning.
2. `edit_impact` does NOT apply renames — pair with Edit.
3. `search` is BM25 over name + doc-comment, not regex. For pattern matching use `find_code` with `pattern_kind="regex"`.
4. `node_get id` requires a stable id from tree_overview / search / find_symbol first.
5. `find_referencing_symbols` and `node_edges` are **single-hop only**. For transitive (>1 hop) chains use `query_graph` with `follow=["callers"]` and `depth=2-5`.
6. Persisted ops (`persisted_query(id="onboarding")`, `"architecture"`, `"repo-map"`) are one-call shortcuts — start there for a first-look snapshot.
7. `find_symbol` on a misspelled name returns a `suggestions` field with edit-distance matches — use it instead of giving up.
