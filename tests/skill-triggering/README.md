# yactt skill-triggering matrix

Measures whether AI agents reach for yactt's MCP tools (and load the
plugin's skills) across **multiple agent harnesses** — claude (Anthropic
Claude Code) and pi (`MiniMax-M3[1m]` via the yactt pi-extension) —
so harness-specific regressions are caught.

Adapted from snowball's [`tests/skill-triggering/`](https://github.com/snowball-dev/snowball)
pattern; restructures the matrix to compare two harnesses side-by-side
on SWE-rebench-style metrics (objective score + cost per task).

## What it measures

For each (harness, skill, prompt) triple, the harness runs the prompt
through that harness, then scores the transcript on three axes:

- **`score_pct`** — SWE-rebench-style: matches the agent's final text
  against a ground-truth `.kw` file (one keyword per line, case-
  insensitive substring). Empty `.kw` = 0% baseline.
- **`cost_usd`** — USD charged per run (parsed from the harness's
  transcript format).
- **`tokens_total`** — `input + output + cache_read + cache_write` —
  the right number to compare against subscription-plan caps.
- **`pass`** — bonus reachability check: skill body loaded OR ≥1
  yactt MCP tool invoked.

A run **passes** if it reaches yactt (skill body or MCP tools); the
`score_pct` measures correctness independently.

## Why a matrix

The first version of this benchmark only ran against claude. It
missed several harness-specific issues that the matrix caught:

1. **MCP adapter isolation** — pi inherits `~/.claude.json` and was
   blocked from yactt because Claude Code has yactt in its
   `disabledMcpServers` list. Fix: `drivers/pi.sh` passes
   `--mcp-config drivers/pi-mcp.json` (yactt only).
2. **Tool-name namespace** — Claude prefixes with
   `mcp__plugin_yactt_yactt__`; pi prefixes with `yactt_` AND routes
   via `args.server/args.tool`. Detection regex is per-driver.
3. **Token vs cost reporting** — Claude reports totals on the
   `result` event; pi reports per-message. The scorer reads the
   right shape per harness.

## Usage

```sh
# Run the matrix (all 8 prompts × both harnesses = 16 runs)
HARNESS=both ./run-all.sh 3

# Single harness
HARNESS=claude ./run-all.sh 3 code-explore
HARNESS=pi ./run-all.sh 3 using-yactt

# Single (skill, prompt) test
HARNESS=pi ./run-test.sh code-explore prompts/code-explore/01-caller.txt 3

# Override pi's provider/model
PI_PROVIDER=anthropic PI_MODEL=claude-sonnet-4-5 HARNESS=pi ./run-all.sh 3

# Before/after (moves skills in/out of the plugin dir)
./compare.sh 3
```

Outputs land in `/tmp/yactt-skill-tests/<timestamp>/` with one
`transcript.json` and `summary.txt` per (harness, skill, prompt), plus
a top-level `SCORES.md` aggregating the run.

## File layout

```
tests/skill-triggering/
├── README.md                    # this file
├── RESULTS.md                   # latest matrix results + interpretation
├── run-test.sh                  # dispatch to driver + score
├── run-all.sh                   # matrix iterator (HARNESS=claude|pi|both)
├── compare.sh                   # before/after by moving skills in/out
├── lib/
│   └── scorer.sh                # shared: transcript text, cost, tokens, kw score
├── drivers/
│   ├── claude.sh                # claude -p invocation
│   ├── pi.sh                    # pi -p invocation
│   └── pi-mcp.json              # isolated MCP config for the pi driver
└── prompts/
    ├── using-yactt/             # broad-intent prompts
    │   ├── 01-orient.{txt,kw}
    │   ├── 02-architecture.{txt,kw}
    │   ├── 03-pr-impact.{txt,kw}
    │   └── 04-quality.{txt,kw}
    └── code-explore/            # symbol-shaped prompts
        ├── 01-caller.{txt,kw}
        ├── 02-callees.{txt,kw}
        ├── 03-references.{txt,kw}
        └── 04-rename-impact.{txt,kw}
```

## Prompts

8 naive prompts total: 4 broad-intent (using-yactt skill) + 4
symbol-shaped (code-explore skill). Each prompt has a sibling `.kw`
file with 2-4 expected keywords that should appear in the agent's
final answer.

Prompts intentionally avoid naming the skill or specific tools — they
phrase the task the way a real user would.

## Caveats

- **Single-tenant** — runs against `tests/fixtures/sample-go/`.
- **Non-deterministic** — tool selection varies turn-to-turn. Run
  N≥3 before claiming any per-prompt delta is real.
- **Harness availability** — requires `claude` and/or `pi` on PATH
  with an active auth session. The harness fails fast if either is
  unavailable.
- **Headless** — uses `--dangerously-skip-permissions` for claude so
  every Bash call doesn't gate on an approval prompt. Pi doesn't need
  it. Run only in trusted workspaces.
- **Cost fidelity** — Claude's USD is real Anthropic billing; pi's
  USD is whatever the configured provider charges (the default
  `MiniMax-M3[1m]` provider is much cheaper than Anthropic). The
  comparison is meaningful but not apples-to-apples on model cost.

## CI

Not gated — this is an offline eval harness, similar to
`tests/fidelity/live.sh`. Wire into CI when the variance is
acceptable and a stable measurement target is identified.