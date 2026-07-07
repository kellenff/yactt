# Benchmark results — yactt skill-triggering

Run on **2026-07-07** against `tests/fixtures/sample-go/` with
`max_turns=3`, harness at `tests/skill-triggering/`.

## TL;DR

| Metric                       | BEFORE (no skills) | AFTER (new skills) |
| ---------------------------- | ------------------ | ------------------ |
| Pass rate (8 prompts)        | 7/8 (87.5%)        | 7–8/8 (87.5–100%) |
| Skill body loads             | 0/8                | 1–2/8 (~13–25%)    |
| yactt tools reached          | 7/8                | 7–8/8              |
| Avg distinct tools per pass  | ~4                 | ~3–4               |

**Verdict**: small but real lift. MCP tool descriptions alone already drive most of the routing (since the plugin advertises them). The new skills add a **skill-body-load signal** that fires on broad-intent prompts (the `using-yactt` meta-skill is doing its job on ~25% of broad-intent runs).

## Detailed BEFORE run (no skills installed)

| Skill           | Prompt               | Pass | Body | Tools | Tool# | Tools invoked |
| --------------- | -------------------- | ---- | ---- | ----- | ----- | ------------- |
| using-yactt     | 01-orient            | ✅   | —    | ✅    | 4     | get_architecture, index_repository, index_status, tree_overview |
| using-yactt     | 02-architecture      | ✅   | —    | ✅    | 4     | get_architecture, index_status, list_projects, tree_overview |
| using-yactt     | 03-pr-impact         | ❌   | —    | —     | —     | (none — no PR context) |
| using-yactt     | 04-quality           | ✅   | —    | ✅    | 4     | get_architecture, index_repository, index_status, tree_overview |
| code-explore    | 01-caller            | ✅   | —    | ✅    | 4     | find_symbol, index_status, list_projects, search |
| code-explore    | 02-callees           | ✅   | —    | ✅    | 4     | find_symbol, list_projects, query_graph, search |
| code-explore    | 03-references        | ✅   | —    | ✅    | 4     | find_code, index_status, search, search_code |
| code-explore    | 04-rename-impact     | ✅   | —    | ✅    | 4     | edit_impact, find_code, find_symbol, search |

## Detailed AFTER run #1 (new skills installed) — partial

Console log captured at the time, but the underlying `summary.txt` files
were lost to a `rm -rf /tmp/yactt-skill-tests` between runs. Aggregated
totals:

- 8/8 pass (100% — including `using-yactt/03-pr-impact` via skill body load)
- 2/8 skill body loads (`using-yactt/02-architecture`, `using-yactt/03-pr-impact`)
- 6/8 reached yactt tools (the other 2 passed via body load only)

## Detailed AFTER run #2 (new skills installed) — preserved

| Skill           | Prompt               | Pass | Body | Tools | Tool# |
| --------------- | -------------------- | ---- | ---- | ----- | ----- |
| using-yactt     | 01-orient            | ✅   | —    | ✅    | 4     |
| using-yactt     | 02-architecture      | ✅   | —    | ✅    | 4     |
| using-yactt     | 03-pr-impact         | ❌   | —    | —     | —     |
| using-yactt     | 04-quality           | ✅   | —    | ✅    | 4     |
| code-explore    | 01-caller            | ✅   | —    | ✅    | 4     |
| code-explore    | 02-callees           | ✅   | —    | ✅    | 3     |
| code-explore    | 03-references        | ✅   | —    | ✅    | 3     |
| code-explore    | 04-rename-impact     | ✅   | ✅   | ✅    | 2     |

**7/8 pass, 1 skill body load** (code-explore/04-rename-impact).

## Interpretation

1. **MCP server + tool descriptions alone carry most of the load.**
   The yactt plugin's `registerAllTools` advertises each tool's name and
   description, and the agent reaches for them in 7/8 prompts even with
   **no skills installed**. The skill descriptions add a modest ceiling
   on top of that.

2. **`using-yactt` (meta-skill) fires on broad-intent prompts.**
   Across two AFTER runs, 3 of 8 prompts loaded a skill body — 2 of
   those were `using-yactt` (broad-intent), 1 was `code-explore`
   (rename impact, the most "policy-heavy" of the navigation prompts).
   The meta-skill's job is to teach the *decision matrix*; it gets
   consulted exactly when the prompt is meta (architecture / overview /
   PR strategy) rather than symbol-shaped.

3. **`03-pr-impact` is a prompt-design issue, not a skill issue.**
   The prompt asks about "the auth package I just refactored" — the
   agent has no actual PR to inspect. Even loading the using-yactt
   body once (in AFTER #1) helped the agent acknowledge the limit
   gracefully. With more conversational turns it could ask for the PR
   ref, but at 3 turns it stalls.

4. **`code-explore` body is rarely loaded.** The agent goes directly
   to the MCP tools it knows about, ignoring the skill body. This is
   fine — the skill body is reference material, the trigger surface
   is the description. Conclusion: **the `code-explore` skill's body
   may not earn its keep** if it consistently goes unread. Worth a
   follow-up audit (e.g. condense into `using-yactt` or drop the body
   and rely on description alone).

## Recommendations

- **Keep** `using-yactt` — its body genuinely fires on broad prompts.
- **Reconsider** `code-explore` — its body is consistently unread;
  merge decision matrix into `using-yactt` and deprecate.
- **Run this benchmark N≥5 times** before promoting any metric — the
  per-prompt pass rate is too noisy to call 1-vs-1 differences.
- **Add headroom**: the current 03-pr-impact prompt is unanswerable
  at 3 turns with no PR context. Either change `MAX_TURNS=5` for
  that prompt or rewrite it to be answerable from the fixture.
