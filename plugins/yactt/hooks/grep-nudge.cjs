#!/usr/bin/env node
//
// yactt: PreToolUse:Grep nudge.
//
// Reads a Claude Code PreToolUse payload from stdin (JSON with
// `tool_name` + `tool_input`). If the Grep pattern looks symbol-shaped
// (camelCase, ::  scope, function-call parens, mixed snake-then-Cap),
// prints a soft one-line reminder to prefer yactt's typed search.
//
// Always exits 0. Never blocks. Cheap: no I/O, no graph query, just
// regex on the pattern string itself.
//
const fs = require("fs");

let raw = "";
try {
  raw = fs.readFileSync(0, "utf8"); // stdin
} catch (_) {
  process.exit(0); // no payload → no nudge
}

let payload;
try {
  payload = JSON.parse(raw);
} catch (_) {
  process.exit(0);
}

if (payload.tool_name !== "Grep") process.exit(0);

const pattern =
  (payload.tool_input && typeof payload.tool_input.pattern === "string")
    ? payload.tool_input.pattern
    : "";

if (!pattern) process.exit(0);

// Telltales of a symbol-shaped (not regex-replaceable) query.
// All cheap string checks; no graph lookup.
const looksLikeSymbol =
  // Go method: "Foo.Bar" or "FooServer.Baz"
  /[A-Z][a-zA-Z0-9]*\.[A-Z][a-zA-Z0-9]*/.test(pattern) ||
  // Go/JS/TS class-qualified: pkg::Func, pkg.Func, pkg.Func.SubFunc
  /::/.test(pattern) ||
  // Function call shape: "Func()" or "Foo.Bar("
  /[A-Za-z_][A-Za-z0-9_]*\(/.test(pattern) &&
    !/\.\*|\(\?|\(\?:/.test(pattern) ||
  // snake_case followed by Capital — likely Go identifier fragment
  /[a-z]+_[a-z]+[A-Z]/.test(pattern) ||
  // Plain CamelCase identifier ≥ 8 chars (FooHandler, MyService, ...)
  /^[A-Z][a-z]+(?:[A-Z][a-z]+){1,}/.test(pattern);

if (looksLikeSymbol) {
  // systemMessage routes through Claude Code's UI as a soft hint.
  console.log(JSON.stringify({
    systemMessage:
      "yactt: pattern looks symbol-shaped — prefer " +
      "mcp__plugin_yactt_yactt__search (BM25) or " +
      "search_code / find_code (AST-aware). " +
      "Use Grep only for line-shaped patterns."
  }));
}
process.exit(0);
