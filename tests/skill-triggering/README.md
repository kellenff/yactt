# yactt skill-triggering benchmark

Measures whether Claude Code reaches for yactt's MCP tools (and loads the
plugin's skills) when given naive prompts that should trigger them. Adapted
from snowball's [`tests/skill-triggering/`](https://github.com/snowball-dev/snowball)
pattern.

## What it measures

For each (skill, prompt) pair, the harness runs `claude -p` against the
`tests/fixtures/sample-go/` workspace with the yactt plugin loaded, then
inspects the stream-json transcript for:

- **Skill body loaded** — the `Skill` tool was invoked with the matching
  skill name. Strongest signal: the agent chose to read the skill's body
  for guidance.
- **Tools reached** — at least one `mcp__plugin_yactt_yactt__*` tool was
  invoked. Weaker but more common signal: the agent acted on the MCP tool
  description alone.

A run counts as **PASS** if either signal fires. The harness reports both
individually so the two signals are distinguishable in the summary.

## Why this matters

The yactt plugin's `description` frontmatter in each `SKILL.md` is the
trigger surface — Claude Code matches the user's prompt against it to
decide which skill to load. The denser the description's trigger phrases,
the better the routing. The benchmark verifies that:

1. Broad-intent prompts ("show me the architecture") load `using-yactt`.
2. Symbol-shaped prompts ("who calls Login") drive the agent to yactt's
   MCP tools instead of falling back to Read/Grep.

## Usage

```sh
# Single run with the new skills in place
./run-all.sh                          # default 3 turns
./run-all.sh 5                        # 5 turns
YACTT_WORKSPACE=/path/to/repo ./run-all.sh

# Single prompt test
./run-test.sh code-explore prompts/code-explore/01-caller.txt

# Before/after comparison (moves skills in/out of the plugin dir)
./compare.sh 3
```

Outputs land in `/tmp/yactt-skill-tests/<timestamp>/` with one
`claude-output.json` and `summary.txt` per run, plus a top-level
`SCORES.md` aggregating the suite.

## Prompts

8 naive prompts, 4 per skill:

- **`using-yactt/`** — broad intent (orientation, architecture, PR impact,
  quality audit). Should fire the meta-skill.
- **`code-explore/`** — symbol-shaped (locate, callers, references,
  rename impact). Should drive the agent to the MCP tools.

Each prompt intentionally avoids naming the skill or specific tools — it
phrases the task the way a real user would.

## Caveats

- **Single-tenant**: runs against the local `tests/fixtures/sample-go/`
  fixture. Extend `prompts/<skill>/` for new repos.
- **Non-deterministic**: tool selection varies turn-to-turn. Treat a
  single run as a sample, not a measurement; aggregate across runs.
- **MCP availability**: requires `claude` on PATH and an active auth
  session. The harness fails fast if `claude` is unavailable.
- **Headless**: uses `--dangerously-skip-permissions` so every Bash call
  doesn't gate on an approval prompt. Run only in trusted workspaces.

## CI

Not gated — this is an offline eval harness, similar to
`tests/fidelity/live.sh`. Wire into CI when the variance is acceptable
and a stable measurement target is identified.