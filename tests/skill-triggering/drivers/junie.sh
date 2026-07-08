#!/usr/bin/env bash
# Junie driver: invokes the Junie CLI non-interactively against a prompt
# with the yactt Junie extension loaded, writes an NDJSON transcript
# (`type`-tagged events), prints the path.
#
# Usage:  drivers/junie.sh <prompt-text> <max-turns> <workspace-dir> <transcript-path>
#
# Side effects:
#   - Writes the transcript to <transcript-path>
#   - Sets a 300s bash-based watchdog
#   - Wraps the workspace in a per-run temp dir and stages
#     `.junie/mcp/mcp.json` so the MCP server is discovered under
#     project scope (the documented discovery path)
#   - Adds `junie-extension/scripts` to PATH so the `yactt-launcher`
#     command in the MCP config resolves; `yactt` itself must already
#     be on PATH (run `plugins/yactt/scripts/install.sh` once)
#   - Accepts max-turns for parity with the other drivers but does NOT
#     pass it to junie — Junie's CLI has no --max-turns flag, so runs
#     proceed to natural completion
#
# Known limitation (first-run finding):
#   Junie's default agent prefers `bash`/`ls`/`find` over MCP tool
#   invocation even when MCP servers are registered. The benchmark
#   measures real-world reachability; low junie reach is a measurement
#   outcome, not a bug. See RESULTS.md for the actual pass rate.
#
# Schema observed (`{type, ...payload}` events):
#   {type:"session", timestamp, sessionId}
#   {type:"step",    timestamp, name, details?, output?}
#   {type:"result",  timestamp, result, changes, errorCode:[{
#        model, calls, cost, inputTokens, outputTokens,
#        cacheInputTokens, cacheCreateTokens }]}

set -uo pipefail

PROMPT="$1"
MAX_TURNS="$2"   # accepted but unused — Junie CLI lacks --max-turns
WORKSPACE_DIR="$3"
LOG_FILE="$4"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
EXT_DIR="$REPO_ROOT/junie-extension"

# ponytail: wrap the workspace in a temp dir per run. We stage
# `.junie/mcp/mcp.json` (project-scope MCP discovery — the documented
# path) without polluting the user's checkout, and symlink the rest so
# large repos don't get copied.
WRAP_DIR="$(mktemp -d)"
cleanup() { rm -rf "$WRAP_DIR" 2>/dev/null || true; }
trap cleanup EXIT

if [ -d "$WORKSPACE_DIR" ]; then
	shopt -s dotglob 2>/dev/null || true
	for f in "$WORKSPACE_DIR"/*; do
		[ -e "$f" ] || continue
		ln -s "$f" "$WRAP_DIR/$(basename "$f")"
	done
fi
mkdir -p "$WRAP_DIR/.junie/mcp"
cp "$EXT_DIR/mcp/.mcp.json" "$WRAP_DIR/.junie/mcp/mcp.json"

# ponytail: launcher must be on PATH for `command: yactt-launcher`
# in mcp/.mcp.json. Scope the PATH bump to this run only.
export PATH="$EXT_DIR/scripts:$PATH"

cd "$WRAP_DIR"

WALL_TIMEOUT=300
junie --task="$PROMPT" \
	--output-format=json-stream \
	--json-output-file="$LOG_FILE" \
	--project="$WRAP_DIR" \
	--skip-update-check \
	>/dev/null 2>&1 &
JUNIE_PID=$!
( sleep "$WALL_TIMEOUT"; kill -9 "$JUNIE_PID" 2>/dev/null ) &
TIMER_PID=$!
wait "$JUNIE_PID"
EXIT_CODE=$?
kill "$TIMER_PID" 2>/dev/null || true

echo "$LOG_FILE"
exit $EXIT_CODE
