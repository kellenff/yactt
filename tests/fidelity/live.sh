#!/usr/bin/env bash
# live.sh — manual harness scaffold for the fidelity suite.
#
# Drives a Claude Code session against the sample-go fixture and
# captures the tool-call transcript for each task in TASKS.md.
# Not gated in CI; intended for occasional offline evaluation.
#
# Usage:
#   ./tests/fidelity/live.sh                # run all 6 tasks
#   ./tests/fidelity/live.sh 1 3 5          # run a subset
#
# Output: ./tests/fidelity/live/transcripts/<timestamp>/<task>.json
#         plus a SCORES.md template populated with the run metadata.
#
# Before running:
#   1. Install the yactt plugin: /plugin marketplace add kellenff/yactt
#                                  /plugin install yactt@yactt
#   2. Open a Claude Code session whose cwd is this repo's root so
#      ${CLAUDE_PROJECT_DIR} resolves to the sample-go fixture.
#   3. Confirm /plugin shows yactt installed and `mcp__plugin_yactt_yactt__*`
#      tools are listed under /mcp.
#
# Then run this script from another terminal — it will print each task
# prompt one at a time, wait for you to paste the captured transcript
# (or just press Enter to skip), and finally write a SCORES.md stub.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
TRANSCRIPT_DIR="$ROOT/tests/fidelity/live/transcripts/$(date +%Y%m%d-%H%M%S)"
mkdir -p "$TRANSCRIPT_DIR"

TASKS=(
  "1|Code navigation — locate and find a caller|Where is Login defined, and where is it used? Show me one caller."
  "2|Code navigation — call chain|What does Login call? Show me the call chain."
  "3|Repo orientation — top-level structure|What's the top-level structure of this repo? Just file paths and exported names."
  "4|Diff impact — public-surface change|Did the public surface of auth/login.go change since HEAD~1? List the changed symbols."
  "5|Cross-language — Python regex|Show me every function in this repo named parse_*."
  "6|Mixed — multi-step refactor summary|I just refactored the auth package — give me a summary of what the public surface looks like now, and which callers might be affected."
)

echo "==> Live harness scaffold"
echo "    transcripts → $TRANSCRIPT_DIR"
echo

if [[ $# -gt 0 ]]; then
  SELECTED=("$@")
else
  SELECTED=(1 2 3 4 5 6)
fi

for n in "${SELECTED[@]}"; do
  for entry in "${TASKS[@]}"; do
    IFS='|' read -r num title prompt <<<"$entry"
    if [[ "$num" == "$n" ]]; then
      echo "── Task $num: $title ──"
      echo
      echo "PROMPT:"
      echo "  $prompt"
      echo
      echo "→ Paste the JSON transcript (or press Enter to skip):"
      read -r transcript
      if [[ -n "$transcript" ]]; then
        echo "$transcript" > "$TRANSCRIPT_DIR/task-$num.json"
        echo "    saved → $TRANSCRIPT_DIR/task-$num.json"
      else
        echo "    skipped"
      fi
      echo
      break
    fi
  done
done

cat > "$TRANSCRIPT_DIR/SCORES.md" <<EOF
# Live harness run — $(date +%Y-%m-%d %H:%M:%S)

## Tasks executed

$(for n in "${SELECTED[@]}"; do echo "- Task $n"; done)

## Scores

| Task | Title | Solved? | Notes |
|------|-------|---------|-------|
$(for n in "${SELECTED[@]}"; do echo "| $n |  |  |  |"; done)

## How to score

For each task, mark "Solved? = Yes/No" depending on whether the agent's
final answer was correct. "Notes" captures the canonical-flow drift
(e.g. wrong tool chosen, wrong order, hallucinated answer).

A useful rule of thumb: did the agent reach for the canonical flow
documented in TASKS.md, and was the final answer correct?
EOF

echo "==> Done."
echo "    SCORES.md → $TRANSCRIPT_DIR/SCORES.md"