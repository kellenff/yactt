#!/usr/bin/env bash
# Run all skill-triggering tests for the yactt plugin.
#
# Iterates every prompt in tests/skill-triggering/prompts/<skill>/
# and reports which skills actually fired.
#
# Usage:
#   ./run-all.sh                       # default: 3 turns
#   ./run-all.sh 5                     # 5 turns
#   YACTT_WORKSPACE=/path ./run-all.sh  # use a different workspace
#
# Output:
#   /tmp/yactt-skill-tests/<timestamp>/<skill>/<prompt>/claude-output.json
#   plus a SCORES.md with the run summary.

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROMPTS_DIR="$SCRIPT_DIR/prompts"
MAX_TURNS="${1:-3}"
WORKSPACE_DIR="${YACTT_WORKSPACE:-}"

SKILLS=(using-yactt code-explore)

TIMESTAMP="$(date +%s)"
RESULTS_DIR="${YACTT_OUTPUT_DIR:-/tmp/yactt-skill-tests}/$TIMESTAMP"
mkdir -p "$RESULTS_DIR"

echo "=== yactt skill-triggering benchmark ==="
echo "max_turns=$MAX_TURNS workspace=${WORKSPACE_DIR:-default(sample-go)}"
echo

PASSED=0
FAILED=0
SKIPPED=0
declare -a RESULTS

for skill in "${SKILLS[@]}"; do
  skill_dir="$PROMPTS_DIR/$skill"
  if [ ! -d "$skill_dir" ]; then
    echo "  ⚠ SKIP: no prompts dir for $skill"
    SKIPPED=$((SKIPPED + 1))
    continue
  fi

  for prompt_file in "$skill_dir"/*.txt; do
    [ -f "$prompt_file" ] || continue
    name="$(basename "$prompt_file" .txt)"
    if "$SCRIPT_DIR/run-test.sh" "$skill" "$prompt_file" "$MAX_TURNS" "$WORKSPACE_DIR"; then
      PASSED=$((PASSED + 1))
      RESULTS+=("✅ $skill / $name")
    else
      FAILED=$((FAILED + 1))
      RESULTS+=("❌ $skill / $name")
    fi
    echo
  done
done

# Write SCORES.md summary.
SCORES="$RESULTS_DIR/SCORES.md"
{
  echo "# yactt skill-triggering run — $(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  echo
  echo "max_turns: $MAX_TURNS"
  echo "workspace: ${WORKSPACE_DIR:-tests/fixtures/sample-go}"
  echo
  echo "## Results"
  echo
  for r in "${RESULTS[@]}"; do
    echo "- $r"
  done
  echo
  echo "## Totals"
  echo
  echo "- Passed: $PASSED"
  echo "- Failed: $FAILED"
  echo "- Skipped: $SKIPPED"
  echo "- Trigger rate: $(( PASSED * 100 / (PASSED + FAILED + 1) ))%"
} > "$SCORES"

echo "════════════════════════════════════════════"
echo "Total: $PASSED passed, $FAILED failed, $SKIPPED skipped"
echo "Trigger rate: $(( PASSED * 100 / (PASSED + FAILED + 1) ))%"
echo "Full report → $SCORES"

if [ "$FAILED" -gt 0 ]; then
  exit 1
fi
