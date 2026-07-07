#!/usr/bin/env bash
# Test skill triggering with a naive prompt.
#
# Usage: ./run-test.sh <skill-name> <prompt-file> [max-turns] [workspace-dir]
#
# Runs `claude -p` against the prompt (which must NOT name the skill
# explicitly) inside the workspace, with the yactt plugin loaded via
# --plugin-dir. Captures the stream-json transcript and inspects it
# for an invocation of the Skill tool targeting <skill-name>.
#
# Exit 0 if triggered, 1 if not. Always writes a transcript + summary.

set -uo pipefail

SKILL_NAME="${1:-}"
PROMPT_FILE="${2:-}"
MAX_TURNS="${3:-3}"
WORKSPACE_DIR="${4:-${YACTT_WORKSPACE:-}}"

if [ -z "$SKILL_NAME" ] || [ -z "$PROMPT_FILE" ]; then
  echo "Usage: $0 <skill-name> <prompt-file> [max-turns] [workspace-dir]" >&2
  exit 2
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
PLUGIN_DIR="$REPO_ROOT/plugins/yactt"

if [ ! -d "$PLUGIN_DIR" ]; then
  echo "Plugin not found at $PLUGIN_DIR" >&2
  exit 2
fi

if [ -z "$WORKSPACE_DIR" ]; then
  # Default: the sample-go fixture.
  WORKSPACE_DIR="$REPO_ROOT/tests/fixtures/sample-go"
fi

if [ ! -d "$WORKSPACE_DIR" ]; then
  echo "Workspace not found at $WORKSPACE_DIR" >&2
  exit 2
fi

TIMESTAMP="$(date +%s)"
RUN_ID="${TIMESTAMP}-$$"
OUTPUT_DIR="${YACTT_OUTPUT_DIR:-/tmp/yactt-skill-tests}/$RUN_ID/$SKILL_NAME"
mkdir -p "$OUTPUT_DIR"
cp "$PROMPT_FILE" "$OUTPUT_DIR/prompt.txt"

PROMPT="$(cat "$PROMPT_FILE")"
LOG_FILE="$OUTPUT_DIR/claude-output.json"

cd "$WORKSPACE_DIR"
echo "  run     → $SKILL_NAME  prompt=$(basename "$PROMPT_FILE")  turns=$MAX_TURNS  ws=$(basename "$WORKSPACE_DIR")"

# Run Claude in pipe mode. A bash watchdog prevents runaway sessions
# without depending on coreutils `timeout` (not on PATH by default on
# macOS). --dangerously-skip-permissions avoids approval prompts that
# would gate the harness in headless runs.
WALL_TIMEOUT=300
claude -p "$PROMPT" \
  --plugin-dir "$PLUGIN_DIR" \
  --dangerously-skip-permissions \
  --max-turns "$MAX_TURNS" \
  --verbose \
  --output-format stream-json \
  >"$LOG_FILE" 2>&1 &
CLAUDE_PID=$!
( sleep "$WALL_TIMEOUT"; kill -9 "$CLAUDE_PID" 2>/dev/null ) &
TIMER_PID=$!
wait "$CLAUDE_PID"
CLAUDE_EXIT=$?
kill "$TIMER_PID" 2>/dev/null || true

# Inspect transcript for both metrics:
#   1. Skill loaded  — Skill tool invoked with this skill's name
#   2. Tools reached — at least one mcp__plugin_yactt_yactt__* tool invoked
#
# "Tools reached" is the primary metric — the skill description's
# job is to wire the agent's attention to yactt's MCP tools, not
# necessarily to require the Skill tool to load the body first.
# A run counts as PASS if EITHER (a) the skill body was explicitly
# loaded, OR (b) at least one yactt tool was invoked. This keeps
# the benchmark honest: the goal is reachability, not ceremony.

SKILL_LOADED=false
TOOLS_REACHED=false
TOOL_COUNT=0

if grep -q '"name":"Skill"' "$LOG_FILE" 2>/dev/null \
   && grep -qE "\"skill\":\"${SKILL_NAME}\"|\"skill\":\"[^\"]*:${SKILL_NAME}\"" "$LOG_FILE" 2>/dev/null; then
  SKILL_LOADED=true
fi

# Capture which yactt MCP tools were called.
INVOKED_TOOLS=$(grep -oE '"name":"mcp__plugin_yactt_yactt__[A-Za-z_]+"' "$LOG_FILE" 2>/dev/null \
  | sed -E 's/.*"name":"mcp__plugin_yactt_yactt__([A-Za-z_]+)"/\1/' | sort -u | tr '\n' ' ' || true)

if [ -n "${INVOKED_TOOLS// /}" ]; then
  TOOLS_REACHED=true
  TOOL_COUNT=$(echo "$INVOKED_TOOLS" | tr ' ' '\n' | grep -c '.' || echo 0)
fi

# Capture which skills WERE invoked (any name) for diagnosis.
INVOKED_SKILLS=$(grep -oE '"skill":"[^"]*"' "$LOG_FILE" 2>/dev/null | sort -u | tr '\n' ' ' || true)

PASS=$SKILL_LOADED  # a skill body load is a clear pass
if [ "$TOOLS_REACHED" = "true" ] && [ "$SKILL_LOADED" = "false" ]; then
  PASS=true        # reaching for tools is also a pass
fi

# Persist summary for the run-all harness.
SUMMARY_FILE="$OUTPUT_DIR/summary.txt"
{
  echo "skill=$SKILL_NAME"
  echo "prompt_file=$(basename "$PROMPT_FILE")"
  echo "workspace=$(basename "$WORKSPACE_DIR")"
  echo "pass=$PASS"
  echo "skill_loaded=$SKILL_LOADED"
  echo "tools_reached=$TOOLS_REACHED"
  echo "tool_count=$TOOL_COUNT"
  echo "invoked_skills=${INVOKED_SKILLS:-<none>}"
  echo "invoked_tools=${INVOKED_TOOLS:-<none>}"
  echo "log_file=$LOG_FILE"
} > "$SUMMARY_FILE"

if [ "$PASS" = "true" ]; then
  if [ "$SKILL_LOADED" = "true" ]; then
    echo "  result  → ✅ PASS (skill body loaded)"
  else
    echo "  result  → ✅ PASS ($TOOL_COUNT yactt tools reached)"
  fi
  exit 0
else
  echo "  result  → ❌ FAIL (no skill load, no yactt tools)"
  exit 1
fi
