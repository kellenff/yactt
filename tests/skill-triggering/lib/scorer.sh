#!/usr/bin/env bash
# Shared scorer: extracts (a) the agent's final assistant text from a
# transcript, (b) per-prompt ground-truth match score, (c) per-run cost
# in USD and tokens (input / output / cache_read / cache_write).
# Each harness has its own transcript format, so cost + token + final-
# text extraction live in drivers/<harness>.sh.
#
# Usage:
#   . lib/scorer.sh
#   transcript_final_text "$LOG_FILE" "$HARNESS"
#   ground_truth_score "$TEXT" "$KW_FILE"             # matched total pct
#   cost_usd "$LOG_FILE" "$HARNESS"                   # USD
#   tokens "$LOG_FILE" "$HARNESS"                     # "in out cache_r cache_w"
#   run_metrics "$LOG_FILE" "$HARNESS" "$KW_FILE"     # one-line summary
#
# Ground-truth .kw file format:
#   one keyword per line. Comments (#) and blank lines ignored.
#   Matching is case-insensitive substring against the final text.

set -uo pipefail

# transcript_final_text: last assistant text block the user would see.
transcript_final_text() {
  local log="$1" harness="$2"
  case "$harness" in
    claude) _claude_final_text "$log" ;;
    pi)     _pi_final_text "$log" ;;
    junie)  _junie_final_text "$log" ;;
    *) echo "scorer: unknown harness '$harness'" >&2; return 1 ;;
  esac
}

# cost_usd: total USD charged for the run.
cost_usd() {
  local log="$1" harness="$2"
  case "$harness" in
    claude) _claude_cost_usd "$log" ;;
    pi)     _pi_cost_usd "$log" ;;
    junie)  _junie_cost_usd "$log" ;;
    *) echo "0.0000"; return 0 ;;
  esac
}

# tokens: echoes "input output cache_read cache_write" (all integers).
# Sum = total billable tokens for plan-cap comparisons.
tokens() {
  local log="$1" harness="$2"
  case "$harness" in
    claude) _claude_tokens "$log" ;;
    pi)     _pi_tokens "$log" ;;
    junie)  _junie_tokens "$log" ;;
    *) echo "0 0 0 0"; return 0 ;;
  esac
}

# ground_truth_score: echoes "matched total pct" (matched=integer, total=
# integer, pct=float). Empty .kw → "0 0 0.00".
ground_truth_score() {
  local text="$1" kw_file="$2"
  if [ ! -f "$kw_file" ]; then
    echo "0 0 0.00"
    return 0
  fi

  local total=0 matched=0
  while IFS= read -r line; do
    line="${line%%#*}"
    line="${line#"${line%%[![:space:]]*}"}"
    line="${line%"${line##*[![:space:]]}"}"
    [ -z "$line" ] && continue
    total=$((total + 1))
    if echo "$text" | grep -qiF -- "$line"; then
      matched=$((matched + 1))
    fi
  done < "$kw_file"

  local pct=0
  [ "$total" -gt 0 ] && pct=$(awk "BEGIN { printf \"%.2f\", $matched * 100.0 / $total }")
  echo "$matched $total $pct"
}

# run_metrics: single-line summary suitable for SCORES.md / RESULTS.md.
# Format: "score_pct|cost_usd|tokens_in|tokens_out|tokens_cache_r|tokens_cache_w|tokens_total"
run_metrics() {
  local log="$1" harness="$2" kw_file="$3"
  local text score cost t_in t_out t_cr t_cw
  text=$(transcript_final_text "$log" "$harness")
  score=$(ground_truth_score "$text" "$kw_file" | awk '{print $3}')
  cost=$(cost_usd "$log" "$harness")
  read -r t_in t_out t_cr t_cw <<<"$(tokens "$log" "$harness")"
  local total=$(( t_in + t_out + t_cr + t_cw ))
  printf "%.2f %s %d %d %d %d %d\n" "$score" "$cost" "$t_in" "$t_out" "$t_cr" "$t_cw" "$total"
}

# ---------------------------------------------------------------------------
# Harness-specific extractors.

# _claude_final_text: last "text" block of the last assistant message in
# stream-json. Tolerates malformed lines (skip + continue).
_claude_final_text() {
  python3 - "$1" <<'PY' 2>/dev/null || echo ""
import json, sys
last = ""
with open(sys.argv[1]) as f:
    for line in f:
        line = line.strip()
        if not line: continue
        try: ev = json.loads(line)
        except Exception: continue
        if ev.get("type") != "assistant": continue
        for block in (ev.get("message", {}).get("content") or []):
            if isinstance(block, dict) and block.get("type") == "text":
                last = block.get("text", "")
print(last, end="")
PY
}

# _claude_cost_usd: total_cost_usd in the final result line.
_claude_cost_usd() {
  grep -oE '"total_cost_usd":[0-9.]+' "$1" 2>/dev/null | tail -1 \
    | sed -E 's/.*:([0-9.]+)/\1/' \
    || echo "0.0000"
}

# _claude_tokens: Claude Code reports per-event usage as 0 and the
# real totals only in the result line. Read from there.
_claude_tokens() {
  python3 - "$1" <<'PY' 2>/dev/null || echo "0 0 0 0"
import json, sys
t_in=t_out=t_cr=t_cw = 0
with open(sys.argv[1]) as f:
    for line in f:
        line = line.strip()
        if not line: continue
        try: ev = json.loads(line)
        except Exception: continue
        if ev.get("type") != "result": continue
        u = ev.get("usage") or {}
        t_in  = int(u.get("input_tokens") or 0)
        t_out = int(u.get("output_tokens") or 0)
        t_cr  = int(u.get("cache_read_input_tokens") or 0)
        t_cw  = int(u.get("cache_creation_input_tokens") or 0)
        break
print(t_in, t_out, t_cr, t_cw)
PY
}

# _pi_final_text: last assistant text block in NDJSON.
_pi_final_text() {
  python3 - "$1" <<'PY' 2>/dev/null || echo ""
import json, sys
last = ""
with open(sys.argv[1]) as f:
    for line in f:
        line = line.strip()
        if not line: continue
        try: ev = json.loads(line)
        except Exception: continue
        msg = ev.get("message")
        if not isinstance(msg, dict) or msg.get("role") != "assistant": continue
        for block in (msg.get("content") or []):
            if isinstance(block, dict) and block.get("type") == "text":
                last = block.get("text", "")
print(last, end="")
PY
}

# _pi_cost_usd: sum of usage.cost.total across assistant messages.
_pi_cost_usd() {
  python3 - "$1" <<'PY' 2>/dev/null || echo "0.0000"
import json, sys
total = 0.0
with open(sys.argv[1]) as f:
    for line in f:
        line = line.strip()
        if not line: continue
        try: ev = json.loads(line)
        except Exception: continue
        msg = ev.get("message")
        if not isinstance(msg, dict) or msg.get("role") != "assistant": continue
        cost = ((msg.get("usage") or {}).get("cost") or {})
        v = cost.get("total")
        if isinstance(v, (int, float)): total += float(v)
print(f"{total:.4f}")
PY
}

# _pi_tokens: sum usage.input/output/cacheRead/cacheWrite across messages.
_pi_tokens() {
  python3 - "$1" <<'PY' 2>/dev/null || echo "0 0 0 0"
import json, sys
t_in=t_out=t_cr=t_cw = 0
with open(sys.argv[1]) as f:
    for line in f:
        line = line.strip()
        if not line: continue
        try: ev = json.loads(line)
        except Exception: continue
        msg = ev.get("message")
        if not isinstance(msg, dict) or msg.get("role") != "assistant": continue
        u = msg.get("usage") or {}
        t_in  += int(u.get("input") or 0)
        t_out += int(u.get("output") or 0)
        t_cr  += int(u.get("cacheRead") or 0)
        t_cw  += int(u.get("cacheWrite") or 0)
print(t_in, t_out, t_cr, t_cw)
PY
}

# _junie_final_text: the `result` field of the {"type":"result"} event.
# That field carries the agent's final assistant text in stream-json.
_junie_final_text() {
  python3 - "$1" <<'PY' 2>/dev/null || echo ""
import json, sys
last = ""
with open(sys.argv[1]) as f:
    for line in f:
        line = line.strip()
        if not line: continue
        try: ev = json.loads(line)
        except Exception: continue
        if ev.get("type") != "result": continue
        last = ev.get("result", "")
print(last, end="")
PY
}

# _junie_cost_usd: sum of `cost` over the `errorCode[]` array on the
# result event. Each entry is per-model (junie routes across multiple
# models for one task — e.g. a planner + a worker). Returns 0.0000 if
# the harness reports no cost (the default for junie, which bills at
# the IDE level rather than per-token).
_junie_cost_usd() {
  python3 - "$1" <<'PY' 2>/dev/null || echo "0.0000"
import json, sys
total = 0.0
with open(sys.argv[1]) as f:
    for line in f:
        line = line.strip()
        if not line: continue
        try: ev = json.loads(line)
        except Exception: continue
        if ev.get("type") != "result": continue
        for entry in (ev.get("errorCode") or []):
            if not isinstance(entry, dict): continue
            v = entry.get("cost")
            if isinstance(v, (int, float)): total += float(v)
        break
print(f"{total:.4f}")
PY
}

# _junie_tokens: same array, summing inputTokens / outputTokens /
# cacheInputTokens / cacheCreateTokens across all model entries.
_junie_tokens() {
  python3 - "$1" <<'PY' 2>/dev/null || echo "0 0 0 0"
import json, sys
t_in=t_out=t_cr=t_cw = 0
with open(sys.argv[1]) as f:
    for line in f:
        line = line.strip()
        if not line: continue
        try: ev = json.loads(line)
        except Exception: continue
        if ev.get("type") != "result": continue
        for entry in (ev.get("errorCode") or []):
            if not isinstance(entry, dict): continue
            # ponytail: junie uses cacheInputTokens/cacheCreateTokens
            # for read/write — different field names than claude/pi.
            t_in  += int(entry.get("inputTokens") or 0)
            t_out += int(entry.get("outputTokens") or 0)
            t_cr  += int(entry.get("cacheInputTokens") or 0)
            t_cw  += int(entry.get("cacheCreateTokens") or 0)
        break
print(t_in, t_out, t_cr, t_cw)
PY
}