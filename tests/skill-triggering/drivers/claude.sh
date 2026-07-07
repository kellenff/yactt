#!/usr/bin/env bash
# Claude driver: invokes `claude -p` against a prompt with the yactt
# plugin loaded, writes a stream-json transcript, prints the path.
#
# Usage:  drivers/claude.sh <prompt-text> <max-turns> <workspace-dir> <transcript-path>
#
# Side effects:
#   - Writes the transcript to <transcript-path>
#   - Sets a 300s bash-based watchdog (no coreutils `timeout` on macOS)
#   - Uses --dangerously-skip-permissions so the run is unattended
#   - The harness script itself decides whether to launch this in background

set -uo pipefail

PROMPT="$1"
MAX_TURNS="$2"
WORKSPACE_DIR="$3"
LOG_FILE="$4"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
PLUGIN_DIR="$REPO_ROOT/plugins/yactt"

cd "$WORKSPACE_DIR"

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
EXIT_CODE=$?
kill "$TIMER_PID" 2>/dev/null || true

echo "$LOG_FILE"
exit $EXIT_CODE