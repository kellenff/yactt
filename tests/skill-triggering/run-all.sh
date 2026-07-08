#!/usr/bin/env bash
# Run the skill-triggering matrix: every (harness × skill × prompt) pair.
#
# Usage:
#   HARNESS=claude|pi|junie|all  ./run-all.sh [max-turns] [skill ...]
#   HARNESS=all ./run-all.sh 3                    # all 8 prompts × 3 harnesses = 24 runs
#   HARNESS=junie ./run-all.sh 3 code-explore     # 4 prompts × 1 harness = 4 runs
#
# Side effects:
#   - Writes transcripts + summaries to $YACTT_OUTPUT_DIR/<ts>/<harness>/<skill>/
#   - Writes SCORES.md with the matrix summary
#   - Exits 0 if at least one run PASSED, 1 otherwise

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROMPTS_DIR="$SCRIPT_DIR/prompts"
MAX_TURNS="${1:-3}"
shift 2>/dev/null || true
SELECTED_SKILLS=("$@")
HARNESS="${HARNESS:-claude}"

# Default to all skills if none specified.
if [ ${#SELECTED_SKILLS[@]} -eq 0 ]; then
  SELECTED_SKILLS=(using-yactt code-explore)
fi

# Harness list.
HARNESSES=("$HARNESS")
if [ "$HARNESS" = "both" ]; then
  HARNESSES=(claude pi)
fi
if [ "$HARNESS" = "all" ]; then
  HARNESSES=(claude pi junie)
fi

TIMESTAMP="$(date -u '+%Y%m%dT%H%M%SZ')"
RESULTS_DIR="${YACTT_OUTPUT_DIR:-/tmp/yactt-skill-tests}/$TIMESTAMP"
mkdir -p "$RESULTS_DIR"

echo "=== yactt skill-triggering matrix ==="
echo "harnesses: ${HARNESSES[*]}"
echo "skills:    ${SELECTED_SKILLS[*]}"
echo "max_turns: $MAX_TURNS"
echo "results:   $RESULTS_DIR"
echo

# Aggregates.
declare -a ROWS
TOTAL_PASS=0
TOTAL_FAIL=0
TOTAL_COST="0"
TOTAL_TOKENS=0

run_one() {
  local harness="$1" skill="$2" prompt_file="$3"
  local prompt_name
  prompt_name="$(basename "$prompt_file" .txt)"
  local out
  out="$(HARNESS="$harness" bash "$SCRIPT_DIR/run-test.sh" "$skill" "$prompt_file" "$MAX_TURNS" 2>&1)" || true
  echo "$out" | tail -1
  echo

  # Pull metrics from the freshest summary for this (harness, skill, prompt).
  local summary
  summary="$(ls -td "$RESULTS_DIR"/[0-9]*/"$harness"/"$skill"/ 2>/dev/null | head -1)/summary.txt"
  if [ ! -f "$summary" ]; then
    summary="$(ls -td /tmp/yactt-skill-tests/*/"$harness"/"$skill"/summary.txt 2>/dev/null | head -1)"
  fi
  if [ ! -f "$summary" ]; then
    return
  fi
  local pass score cost t_total
  pass=$(awk -F= '/^pass=/{print $2}' "$summary")
  score=$(awk -F= '/^score_pct=/{print $2}' "$summary")
  cost=$(awk -F= '/^cost_usd=/{print $2}' "$summary")
  t_total=$(awk -F= '/^tokens_total=/{print $2}' "$summary")
  ROWS+=("$harness|$skill|$prompt_name|$pass|$score|$cost|$t_total")
  if [ "$pass" = "true" ]; then
    TOTAL_PASS=$((TOTAL_PASS + 1))
  else
    TOTAL_FAIL=$((TOTAL_FAIL + 1))
  fi
  TOTAL_COST=$(awk "BEGIN { printf \"%.4f\", $TOTAL_COST + $cost }")
  TOTAL_TOKENS=$((TOTAL_TOKENS + t_total))
}

for h in "${HARNESSES[@]}"; do
  for skill in "${SELECTED_SKILLS[@]}"; do
    skill_dir="$PROMPTS_DIR/$skill"
    [ -d "$skill_dir" ] || { echo "  ⚠ SKIP: no prompts for $skill"; continue; }
    for prompt_file in "$skill_dir"/*.txt; do
      [ -f "$prompt_file" ] || continue
      run_one "$h" "$skill" "$prompt_file"
    done
  done
done

# Write SCORES.md matrix.
SCORES="$RESULTS_DIR/SCORES.md"
{
  echo "# yactt skill-triggering matrix — $(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  echo
  echo "max_turns: $MAX_TURNS"
  echo "harnesses: ${HARNESSES[*]}"
  echo
  echo "## Per-run matrix"
  echo
  printf "%-7s %-14s %-18s %-6s %-7s %-9s %-8s\n" "harness" "skill" "prompt" "pass" "score%" "cost_usd" "tokens"
  echo "------------------------------------------------------------------------"
  for r in "${ROWS[@]}"; do
    IFS='|' read -r h s p pass score cost t <<<"$r"
    [ "$pass" = "true" ] && mark="✅" || mark="❌"
    printf "%-7s %-14s %-18s %-6s %-7s %-9s %-8s\n" "$h" "$s" "$p" "$mark" "$score" "\$$cost" "$t"
  done
  echo
  echo "## Totals"
  echo
  echo "- Total runs: $((TOTAL_PASS + TOTAL_FAIL))"
  echo "- Passed: $TOTAL_PASS"
  echo "- Failed: $TOTAL_FAIL"
  echo "- Pass rate: $(awk "BEGIN { printf \"%.0f\", $TOTAL_PASS * 100.0 / ($TOTAL_PASS + $TOTAL_FAIL) }")%"
  echo "- Total cost: \$$TOTAL_COST USD"
  echo "- Total tokens: $TOTAL_TOKENS"
} > "$SCORES"

echo "════════════════════════════════════════════"
echo "Total: $TOTAL_PASS passed, $TOTAL_FAIL failed"
echo "Cost: \$$TOTAL_COST USD"
echo "Tokens: $TOTAL_TOKENS"
echo "Full report → $SCORES"
