---
name: using-yactt
description: Use when working with Go, TypeScript, JavaScript, or Python source code
---

# When to reach for yactt

yactt exposes 21 MCP tools (`mcp__plugin_yactt_yactt__*`) built on tree-sitter with opportunistic LSP (gopls /
typescript-language-server / pyright-langserver). Reach for them when the question is **symbol-shaped** ("what calls X")
rather than byte-shaped ("show me line 42").

If a `codebase-memory`-style graph or a snowball workflow is also present, yactt complements them — yactt is the typed,
AST-aware floor; graph tools are the cross-service ceiling.

## Decision matrix (first 30 seconds)

| Question                                     | Use                                                            | Don't use                      |
|----------------------------------------------|----------------------------------------------------------------|--------------------------------|
| "Where is X defined?"                        | `find_symbol(name_path="X")` (check `suggestions` on miss)     | Grep                           |
| "Who calls X?" (single hop)                  | `node_edges(id=..., kinds=["callers"])`                        | Grep                           |
| "What does X call?" (single hop)             | `node_edges(id=..., kinds=["callees"])`                        | Read whole file                |
| "Who transitively depends on X?" (multi-hop) | `query_graph(from=..., follow=["callers"], depth=2-5)`         | loop of `node_edges`           |
| "Find anything matching `*Foo*`"             | `search(query="Foo")`                                          | Grep                           |
| "What does this repo look like?"             | `tree_overview(depth=2)` + `get_architecture()`                | `ls -R`                        |
| "Will renaming X break anything?"            | `edit_impact(renames=[{id:..., new_name:...}])`                | (no equivalent in plain tools) |
| "What changed in this branch?"               | `detect_changes()`                                             | `git diff` + manual reading    |
| "Dead code / hot spots / cycles?"            | `get_architecture()` (returns all three)                       | manual triage                  |
| "One-shot curated snapshot?"                 | `persisted_query(id="onboarding"\|"architecture"\|"repo-map")` | reconstruct the call           |
| "What kinds can I filter `query_graph` by?"  | `get_graph_schema()` (call first when writing filters)         | hardcoding strings             |

## The 8-step ladder (cheapest first)

1. **`tree_overview`** — map the repo. `depth=2` is the default; don't start at 6.
2. **`get_symbols_overview`** — top-level outline of a specific file when you know the path.
3. **`search`** (BM25 over name + doc) / **`find_symbol`** (glob over name-path) — locate the target. On `find_symbol`
   miss, the response includes a `suggestions` field — try those.
4. **`node_get`** — pull `summary` first, then `body` / `source` only if needed.
5. **`node_source`** — lossless source, optionally line-bounded.
6. **`node_edges`** / **`find_referencing_symbols`** — single-hop callers, callees, tests, overrides, imports. **For
   multi-hop, use `query_graph`.**
7. **`query_graph`** — multi-hop reachability; depth 1–5 with cost caps. The right tool for transitive caller/callee
   questions.
8. **`find_code`** — AST-aware (tree-sitter) or regex across files.
9. **`edit_impact`** — ALWAYS run before any rename. Returns impact, does not apply.

## Truncation

List-returning tools emit `truncated:bool` and `totalCount:int` (or `visited:int` for `query_graph`) when the result was
capped. **Always check `truncated`** before declaring a query exhausted — if true, raise `limit` and re-call. The cap
signals matter for agent loops: a silent cap means the agent can't tell whether it has the full answer.

## Layer-cost discipline

- `summary` is cheap — usually the right default.
- `body` and `source` are expensive — pull only the nodes you actually need.
- `tokens` is for budget reasoning, not for reading.

## When NOT to reach for yactt

- **Prose** — READMEs, CHANGELOGs, long comments. Use Read.
- **Single short file** you already know the path of. Read is fine; yactt is graph-indexed, not text-indexed.
- **Non-Go / TS / JS / Python** — the tree-sitter floor still indexes, but LSP resolution is best-effort.
- **Binary files** — never.
- **You just need one regex result** — Grep is fine if the pattern is line-shaped, not symbol-shaped.

## Gotchas

1. `tree_overview(depth=6)` returns thousands of nodes — start shallow, drill.
2. `node_get` needs a stable `id` (from tree_overview / search / find_symbol). Don't call without one.
3. `edit_impact` does NOT apply renames — it returns the impact set only. Pair with Edit.
4. `search` is BM25, not regex. For regex, use `find_code` with `pattern_kind="regex"`.
5. The persisted_query MVP runs ONE tool call per op — no step-chaining yet (planned).
6. MCP-side names are unprefixed (`tree_overview`), but in the tool surface they may appear as
   `mcp__plugin_yactt_yactt__tree_overview`.
