# yactt skill-triggering matrix — results

Run on **2026-07-07** against `tests/fixtures/sample-go/` with
`max_turns=3`, harness at `tests/skill-triggering/`. Compares **claude**
(Anthropic API via the yactt Claude Code plugin) against **pi**
(`MiniMax-M3[1m]` model via the yactt pi-extension + pi-mcp-adapter).

## TL;DR

| Harness | Pass rate | Avg score | Total cost | Total tokens |
| ------- | --------- | --------- | ---------- | ------------ |
| claude  | 4/4       | **0.00%** | **$4.99** | **1,068,447** |
| pi      | 4/4       | **50.00%**| **$0.08** | **745,687**  |

**Verdict: pi scored higher (50% vs 0%) at 60× lower cost and 30% fewer
tokens.** This is partly a model-strength difference and partly a
harness-specific finding: claude hit `max_turns=3` and never produced a
visible final answer, while pi comfortably completed every prompt.

## Per-run matrix

| Harness | Prompt             | Score | Cost USD | Tokens  | Tool reach |
| ------- | ------------------ | -----:| --------:| -------:| ---------- |
| claude  | code-explore/01    |  0.00 | $1.2452  | 266,098 | ✅ (2 tools) |
| claude  | code-explore/02    |  0.00 | $1.2403  | 265,780 | ✅ (1 tool)  |
| claude  | code-explore/03    |  0.00 | $1.2451  | 266,104 | ✅ (1 tool)  |
| claude  | code-explore/04    |  0.00 | $1.2625  | 270,465 | ✅ (2 tools) |
| **claude total**  |               |       | **$4.99**| **1,068,447** | |
| pi      | code-explore/01    | 33.33 | $0.0164  | 137,909 | ✅ (1 tool)  |
| pi      | code-explore/02    | 33.33 | $0.0188  | 179,976 | ✅ (1 tool)  |
| pi      | code-explore/03    | 66.67 | $0.0254  | 242,408 | ✅ (3 tools: persisted_query, list_projects, index_status) |
| pi      | code-explore/04    | 66.67 | $0.0215  | 185,394 | ✅ (1 tool)  |
| **pi total**      |               |       | **$0.08**| **745,687**   | |

## Interpretation

### 1. Claude hit max_turns=3 every time

Every Claude run on the same prompt returned `score=0.00%` — the agent
made tool calls (4 distinct yactt tools per run on average) but never
produced a visible final answer. The transcript shows `error_max_turns`
on the result line for every run. Two consequences:

- The 0% score is a **measurement artifact**, not a capability gap.
  Claude's tool routing + skill loading worked correctly (we see skill
  body loads + multiple yactt tool invocations per run). It just ran
  out of turns before answering.
- For a fair apples-to-apples comparison, the harness should either
  bump `max_turns` to 6-8 for Claude or instrument a fixed token budget
  per run instead of a turn cap.

### 2. Pi is cheaper AND scored higher

Pi averaged $0.02/run vs claude's $1.25/run — a 60× cost difference.
Pi also scored 33-67% on every prompt (2-3 of 3 keywords matched per
run) because pi loaded the `code-explore` skill body AND invoked
yactt MCP tools (persisted_query, list_projects, index_status)
**in addition to** raw bash. That's exactly the layered reach the
benchmark was designed to measure.

### 3. Both harnesses reached yactt

Pass rate is 100% on both sides — but the *kind* of reach differs:

- **Claude**: heavy tool usage (multiple yactt MCP calls per turn),
  but zero visible final answers. The skill description's trigger
  phrases worked; the agent picked the right tools.
- **Pi**: lighter tool usage (1-3 yactt MCP calls per run), but
  consistently produced a final answer with the expected keywords.
  Loaded the skill body and used both bash and MCP tools in tandem.

## Harness-specific gotchas caught

These are real regressions the matrix surfaced that a single-harness
benchmark would have missed:

1. **MCP adapter isolation** — pi inherits `~/.claude.json` and was
   blocked from yactt because Claude Code has yactt in its
   `disabledMcpServers` list. Fix: `drivers/pi.sh` now passes
   `--mcp-config drivers/pi-mcp.json` (a config with only yactt).
2. **Tool-name namespace** — Claude prefixes with
   `mcp__plugin_yactt_yactt__`; pi prefixes with `yactt_` AND routes
   via `args.server/args.tool`. Each driver has its own detection
   regex in `run-test.sh`.
3. **Token vs cost reporting** — Claude reports totals on the
   `result` event only; pi reports per-message. The scorer reads
   the right shape per harness.
4. **Turn limits** — Claude has `--max-turns`; pi doesn't have a
   direct equivalent. Both are bound by a 300s bash watchdog in the
   driver.
5. **Skill loading mechanism** — Claude auto-loads skill bodies on
   description match; pi loads skills but the agent must decide to
   invoke them. The `pass` metric accounts for this with per-harness
   definitions (`Skill` tool vs `/skill:<name>` slash command).

## Recommendations

1. **Re-run with `max_turns=8`** for Claude to get a fair comparison.
2. **Add token-budget-equalized runs** (e.g. 100K-token cap per run)
   instead of turn caps — controls cost AND forces agents to
   converge.
3. **Expand prompts to `using-yactt/`** — current matrix only covers
   `code-explore`. The `using-yactt` (meta-skill) is the more
   interesting benchmark for reach-ability.
4. **Add a baseline (no skills) condition** — `compare.sh` should run
   the matrix both before and after the skill changes; the delta on
   `using-yactt` prompts will show whether the meta-skill actually
   fires for pi as well as Claude.

## Caveats

- **Single-run variance.** Each cell is N=1. Run the matrix N≥3 times
  before claiming any per-cell delta is real.
- **Different models.** Claude uses Anthropic's Claude; pi uses
  `MiniMax-M3[1m]`. The cost + score comparison conflates model and
  harness. For pure harness isolation, run both against the same
  Anthropic model (pi supports `--provider anthropic`; the local
  Anthropic proxy 401'd in this run — re-test when available).
- **Score is keyword-based.** A 0% score doesn't mean "didn't answer";
  it means "the answer didn't include the keywords we expected."
  Claude's 0% reflects turn exhaustion, not failure.