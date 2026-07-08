#!/usr/bin/env bash
# Run a single (skill, prompt) test against one harness, score the
# result, write a summary. The driver is selected by HARNESS env var
# (default: claude).
#
# Usage:
#   HARNESS=claude|pi   ./run-test.sh <skill> <prompt-txt-path> <max-turns> [workspace-dir]
#
# Side effects:
#   - Writes a transcript under $YACTT_OUTPUT_DIR/<ts>-<pid>/<harness>/<skill>/
#   - Writes summary.txt with the per-run metrics
#   - Exits 0 if the agent reached for yactt (pass criterion: tool reach OR
#     skill body load — both at parity across harnesses), 1 otherwise

set -uo pipefail

SKILL_NAME="${1:-}"
PROMPT_FILE="${2:-}"
MAX_TURNS="${3:-3}"
WORKSPACE_DIR="${4:-${YACTT_WORKSPACE:-}}"
HARNESS="${HARNESS:-claude}"

if [ -z "$SKILL_NAME" ] || [ -z "$PROMPT_FILE" ]; then
  echo "Usage: HARNESS=claude|pi|junie $0 <skill-name> <prompt-txt-path> [max-turns] [workspace-dir]" >&2
  exit 2
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
. "$SCRIPT_DIR/lib/scorer.sh"

REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
if [ -z "$WORKSPACE_DIR" ]; then
  WORKSPACE_DIR="$REPO_ROOT/tests/fixtures/sample-go"
fi

DRIVER="$SCRIPT_DIR/drivers/$HARNESS.sh"
if [ ! -x "$DRIVER" ]; then
  echo "Driver not found / not executable: $DRIVER" >&2
  exit 2
fi

PROMPT_NAME="$(basename "$PROMPT_FILE" .txt)"
KW_FILE="${PROMPT_FILE%.txt}.kw"

TIMESTAMP="$(date +%s)"
RUN_ID="${TIMESTAMP}-$$"
OUTPUT_DIR="${YACTT_OUTPUT_DIR:-/tmp/yactt-skill-tests}/$RUN_ID/$HARNESS/$SKILL_NAME"
mkdir -p "$OUTPUT_DIR"
cp "$PROMPT_FILE" "$OUTPUT_DIR/prompt.txt"
[ -f "$KW_FILE" ] && cp "$KW_FILE" "$OUTPUT_DIR/prompt.kw"

LOG_FILE="$OUTPUT_DIR/transcript.json"
echo "  run     → $HARNESS / $SKILL_NAME  prompt=$PROMPT_NAME  turns=$MAX_TURNS"

# Invoke the driver. It writes the transcript and prints the path.
LOG_OUT="$("$DRIVER" "$(cat "$PROMPT_FILE")" "$MAX_TURNS" "$WORKSPACE_DIR" "$LOG_FILE" 2>&1)"
DRIVER_EXIT=$?
[ -n "$LOG_OUT" ] && LOG_FILE="$LOG_OUT"

# Compute metrics.
METRICS="$(run_metrics "$LOG_FILE" "$HARNESS" "$KW_FILE")"
read -r SCORE_PCT COST_USD T_IN T_OUT T_CR T_CW T_TOTAL <<<"$METRICS"

# Tool reach: did the agent invoke any yactt MCP tool? For claude, look
# for mcp__plugin_yactt_yactt__* names; for pi, look for tools whose
# name is a yactt tool (after MCP adapter renames). Both are surfaced
# as JSON tool-call entries with "name":"...".
case "$HARNESS" in
  claude)
    TOOLS_REACHED=false
    TOOL_NAMES=$(grep -oE '"name":"mcp__plugin_yactt_yactt__[A-Za-z_]+"' "$LOG_FILE" 2>/dev/null \
      | sed -E 's/.*"name":"mcp__plugin_yactt_yactt__([A-Za-z_]+)"/\1/' | sort -u | tr '\n' ' ')
    ;;
  pi)
    # Pi routes MCP calls through the pi-mcp-adapter. The adapter emits
    # `toolName:"mcp"` and the yactt routing is in args:
    #   { server: "yactt", tool: "tree_overview", args: "{...}" }
    # (the bare tool name, not yactt_<name>). Any "server":"yactt"
    # occurrence is a yactt tool reach; the tool name is args.tool.
    TOOLS_REACHED=false
    TMP_NAMES="$(mktemp -t yactt-names.XXXXXX)"
    grep -oE '"name":"yactt_[a-z_]+"' "$LOG_FILE" 2>/dev/null \
      | sed -E 's/.*"name":"yactt_([a-z_]+)"/\1/' >> "$TMP_NAMES" || true
    grep -oE '"server":"yactt","tool":"[a-z_]+"' "$LOG_FILE" 2>/dev/null \
      | sed -E 's/.*"tool":"([a-z_]+)".*/\1/' >> "$TMP_NAMES" || true
    # Mark reachability on the server-tag alone (some adapter versions
    # don't surface args.tool cleanly).
    if grep -q '"server":"yactt"' "$LOG_FILE" 2>/dev/null; then
      TOOLS_REACHED=true
      [ ! -s "$TMP_NAMES" ] && echo "yactt_mcp" >> "$TMP_NAMES"
    fi
    TOOL_NAMES=$(sort -u "$TMP_NAMES" | tr '\n' ' ' | sed 's/ $//')
    rm -f "$TMP_NAMES"
    ;;
  junie)
    # Junie's MCP server events carry the yactt server tag and the bare
    # tool name. Tool names we recognize live in $YACTT_TOOLS_RE below —
    # an explicit list is more robust than parsing every event shape.
    TOOLS_REACHED=false
    YACTT_TOOLS_RE='tree_overview|node_get|node_source|node_edges|find_symbol|find_code|search|search_code|find_referencing_symbols|edit_impact|get_graph_schema|get_code_snippet|get_architecture|get_symbols_overview|query_graph|detect_changes|list_projects|index_repository|index_status|delete_project|persisted_query'
    TMP_NAMES="$(mktemp -t yactt-names.XXXXXX)"
    grep -oE "\"name\":\"mcp__yactt__(${YACTT_TOOLS_RE})\"" "$LOG_FILE" 2>/dev/null \
      | sed -E "s/.*\"name\":\"mcp__yactt__(${YACTT_TOOLS_RE})\"/\1/" >> "$TMP_NAMES" || true
    grep -oE "\"server\":\"yactt\",\"tool\":\"(${YACTT_TOOLS_RE})\"" "$LOG_FILE" 2>/dev/null \
      | sed -E "s/.*\"tool\":\"(${YACTT_TOOLS_RE})\".*/\1/" >> "$TMP_NAMES" || true
    if grep -q '"server":"yactt"' "$LOG_FILE" 2>/dev/null; then
      TOOLS_REACHED=true
      [ ! -s "$TMP_NAMES" ] && echo "yactt_mcp" >> "$TMP_NAMES"
    fi
    TOOL_NAMES=$(sort -u "$TMP_NAMES" | tr '\n' ' ' | sed 's/ $//')
    rm -f "$TMP_NAMES"
    ;;
esac
if [ -n "${TOOL_NAMES// /}" ]; then TOOLS_REACHED=true; fi
TOOL_COUNT=$(echo "$TOOL_NAMES" | tr ' ' '\n' | grep -c '.' || echo 0)

# Skill body load: per-harness definition.
SKILL_LOADED=false
case "$HARNESS" in
  claude)
    if grep -q '"name":"Skill"' "$LOG_FILE" 2>/dev/null \
       && grep -qE "\"skill\":\"${SKILL_NAME}\"|\"skill\":\"[^\"]*:${SKILL_NAME}\"" "$LOG_FILE" 2>/dev/null; then
      SKILL_LOADED=true
    fi
    ;;
  pi)
    # Pi uses slash commands. The agent doesn't auto-load skills, so this
    # is usually false; we record it for completeness.
    if grep -q "/skill:${SKILL_NAME}\b" "$LOG_FILE" 2>/dev/null; then
      SKILL_LOADED=true
    fi
    ;;
  junie)
    # Junie emits a skill_load / skill_use event when the agent pulls
    # the skill body into context. Either name field is sufficient.
    if grep -qE "\"name\":\"${SKILL_NAME}\"|\"skill\":\"${SKILL_NAME}\"" "$LOG_FILE" 2>/dev/null; then
      SKILL_LOADED=true
    fi
    ;;
esac

# Pass = tool reach OR skill body load (parity with prior harness).
if [ "$TOOLS_REACHED" = "true" ] || [ "$SKILL_LOADED" = "true" ]; then
  PASS=true
else
  PASS=false
fi

# Persist summary.
SUMMARY="$OUTPUT_DIR/summary.txt"
{
  echo "harness=$HARNESS"
  echo "skill=$SKILL_NAME"
  echo "prompt_file=$PROMPT_NAME.txt"
  echo "pass=$PASS"
  echo "score_pct=$SCORE_PCT"
  echo "cost_usd=$COST_USD"
  echo "tokens_in=$T_IN"
  echo "tokens_out=$T_OUT"
  echo "tokens_cache_read=$T_CR"
  echo "tokens_cache_write=$T_CW"
  echo "tokens_total=$T_TOTAL"
  echo "skill_loaded=$SKILL_LOADED"
  echo "tools_reached=$TOOLS_REACHED"
  echo "tool_count=$TOOL_COUNT"
  printf "tool_names=%s\n" "${TOOL_NAMES:-<none>}"
  echo "transcript=$LOG_FILE"
} > "$SUMMARY"

# Pretty per-run line.
RESULT="✅ PASS"
if [ "$PASS" = "false" ]; then RESULT="❌ FAIL"; fi
echo "  result  → $RESULT  score=${SCORE_PCT}%  cost=\$${COST_USD}  tokens=${T_TOTAL}  tools=${TOOL_COUNT}"

[ "$PASS" = "true" ]
