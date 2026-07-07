#!/usr/bin/env bash
# Before/after comparison for the new yactt skills.
#
# 1. Run the benchmark with the new skills installed (the current state)
# 2. Move the new skills out of the plugin dir (simulates "before")
# 3. Run the benchmark again
# 4. Restore the skills
# 5. Print a side-by-side table with the metric breakdown
#
# Usage: ./compare.sh [max-turns]

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
PLUGIN_SKILLS="$REPO_ROOT/plugins/yactt/skills"
TOP_SKILLS="$REPO_ROOT/skills"
BACKUP_PARENT="$(mktemp -d)"
MAX_TURNS="${1:-3}"

echo "=== yactt skill-triggering: before/after ==="
echo

declare -a SAVED=()
for src in \
    "$PLUGIN_SKILLS/using-yactt" \
    "$PLUGIN_SKILLS/code-explore" \
    "$TOP_SKILLS/using-yactt" \
    "$TOP_SKILLS/code-explore"
do
  if [ -e "$src" ]; then
    dst="$BACKUP_PARENT/$(basename "$src")"
    SAVED+=("$src")
  fi
done

move_out() {
  for src in "${SAVED[@]}"; do
    dst="$BACKUP_PARENT/$(basename "$src")"
    mkdir -p "$(dirname "$dst")"
    mv "$src" "$dst"
  done
}

restore() {
  for src in "${SAVED[@]}"; do
    dst="$BACKUP_PARENT/$(basename "$src")"
    if [ -e "$dst" ]; then
      mkdir -p "$(dirname "$src")"
      rm -rf "$src"
      mv "$dst" "$src"
    fi
  done
}

trap restore EXIT

run_suite() {
  local label="$1"
  local log="/tmp/yactt-bench-${label}.log"
  rm -rf /tmp/yactt-skill-tests
  echo "── $label ──"
  bash "$SCRIPT_DIR/run-all.sh" "$MAX_TURNS" 2>&1 | tee "$log"
  echo
}

# 1. AFTER
run_suite "AFTER (new skills installed)"

# 2. BEFORE
move_out
echo "(skills moved to $BACKUP_PARENT — restoring on exit)"
run_suite "BEFORE (no skills)"

# 3. Restore (trap)
restore
trap - EXIT
rm -rf "$BACKUP_PARENT" 2>/dev/null || true

# 4. Aggregate from summary.txt files (richer than run-all's tally).
after_dir="$(ls -dt /tmp/yactt-skill-tests/*/ 2>/dev/null | sed -n '2p')"  # second-newest (BEFORE was last)
before_dir="$(ls -dt /tmp/yactt-skill-tests/*/ 2>/dev/null | sed -n '1p')" # newest (BEFORE)

# Re-discover via /tmp/yactt-skill-tests SCORES.md instead — the dirs are
# both there but the ORDER of tee's output to /tmp/yactt-bench-{after,before}.log
# is the cleanest source of truth.
agg() {
  local log="$1"
  awk -F'[: ]+' '
    /skill=/{skill=$2}
    /prompt_file=/{prompt=$2}
    /pass=/{pass=$2}
    /skill_loaded=/{body=$2}
    /tools_reached=/{tools=$2}
    /tool_count=/{count=$2}
    /^log_file=/{print skill, prompt, pass, body, tools, count}
  ' "$log"
}

echo
echo "═══════════════════════════════════════════════════════════════════"
printf "%-12s %-26s %-5s %-6s %-8s %s\n" "skill" "prompt" "pass" "body" "tools" "tool#"
echo "───────────────────────────────────────────────────────────────────"

# We need to extract summaries from both runs. The simplest path: parse
# each individual summary.txt from the run output dirs.
print_breakdown() {
  local label="$1"
  local run_root="/tmp/yactt-skill-tests"
  # Find the run dir that matches this label — easier: just show every
  # summary.txt sorted by skill/prompt name.
  for f in $(find "$run_root" -name summary.txt | sort); do
    awk -v label="$label" '
      /^skill=/{gsub("skill=",""); skill=$0}
      /^prompt_file=/{gsub("prompt_file=",""); prompt=$0}
      /^pass=/{gsub("pass=",""); pass=$0}
      /^skill_loaded=/{gsub("skill_loaded=",""); body=$0}
      /^tools_reached=/{gsub("tools_reached=",""); tools=$0}
      /^tool_count=/{gsub("tool_count=",""); count=$0}
      /^log_file=/{printf "%-12s %-26s %-5s %-6s %-8s %s\n", skill, prompt, pass, body, tools, count}
    ' "$f" | sed "s/^/${label}  /"
  done
}

# Find the two most recent timestamps and use them as AFTER (older) and BEFORE (newer).
recent_dirs=$(ls -dt /tmp/yactt-skill-tests/*/ 2>/dev/null | head -2)
after_id=$(echo "$recent_dirs" | sed -n '2p' | xargs basename)
before_id=$(echo "$recent_dirs" | sed -n '1p' | xargs basename)

print_for() {
  local label="$1" ts="$2"
  find "/tmp/yactt-skill-tests/$ts" -name summary.txt 2>/dev/null \
    | sort \
    | xargs -I{} awk -v label="$label" '
        /^skill=/{gsub("skill=",""); skill=$0}
        /^prompt_file=/{gsub("prompt_file=",""); prompt=$0}
        /^pass=/{gsub("pass=",""); pass=$0}
        /^skill_loaded=/{gsub("skill_loaded=",""); body=$0}
        /^tools_reached=/{gsub("tools_reached=",""); tools=$0}
        /^tool_count=/{gsub("tool_count=",""); count=$0}
        /^log_file=/{printf "%-4s %-12s %-26s %-5s %-6s %-8s %s\n", label, skill, prompt, pass, body, tools, count}
      ' {}
}

print_for "AFTER"  "$after_id"
print_for "BEFORE" "$before_id"

echo "═══════════════════════════════════════════════════════════════════"
echo
echo "Columns:"
echo "  body  = Skill tool invoked with this skill's name (skill body loaded)"
echo "  tools = at least one mcp__plugin_yactt_yactt__* tool was invoked"
echo "  tool# = number of distinct yactt tools invoked"
echo "  pass  = body OR tools (the harness PASS criterion)"