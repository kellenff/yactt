#!/usr/bin/env bash
# Pi driver: invokes `pi -p` against a prompt with the yactt MCP server
# auto-registered (via ~/.pi/agent/mcp.json), writes an NDJSON transcript,
# prints the path.
#
# Usage:  drivers/pi.sh <prompt-text> <max-turns> <workspace-dir> <transcript-path>
#
# Side effects:
#   - Writes the transcript to <transcript-path>
#   - Sets a 300s bash-based watchdog
#   - Uses --no-session so each prompt is ephemeral
#   - Provider/model override via PI_PROVIDER / PI_MODEL env vars
#     (default: minimax / MiniMax-M3[1m] to mirror Claude Code's model)
#   - Scopes the tool surface to yactt + read-only built-ins via
#     --exclude-tools. Pi loads 8+ MCP servers by default (argdown,
#     context7, idea, serena, mcp-adapter, yactt, ...) and the agent
#     explores them all — that's noise for a yactt benchmark.
#   - --thinking minimal keeps output tokens (which include thinking)
#     comparable to claude's much-shorter assistant events.

set -uo pipefail

PROMPT="$1"
MAX_TURNS="$2"
WORKSPACE_DIR="$3"
LOG_FILE="$4"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

PI_PROVIDER="${PI_PROVIDER:-minimax}"
PI_MODEL="${PI_MODEL:-MiniMax-M3[1m]}"
PI_EXCLUDE_TOOLS="${PI_EXCLUDE_TOOLS:-argdown,context7,idea,serena}"

cd "$WORKSPACE_DIR"

WALL_TIMEOUT=300
pi -p "$PROMPT" \
  --mode json \
  --no-session \
  --provider "$PI_PROVIDER" \
  --model "$PI_MODEL" \
  --mcp-config "$SCRIPT_DIR/pi-mcp.json" \
  --exclude-tools "$PI_EXCLUDE_TOOLS" \
  --thinking minimal \
  >"$LOG_FILE" 2>&1 &
PI_PID=$!
( sleep "$WALL_TIMEOUT"; kill -9 "$PI_PID" 2>/dev/null ) &
TIMER_PID=$!
wait "$PI_PID"
EXIT_CODE=$?
kill "$TIMER_PID" 2>/dev/null || true

echo "$LOG_FILE"
exit $EXIT_CODE