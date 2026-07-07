# Fidelity tasks

A harness (Claude Code + the yactt plugin) should be able to solve these six
tasks by reaching for the right MCP tools in the right order. Each task
corresponds to one `TestFidelity_TaskN` in `fidelity_test.go`, which drives
the canonical tool progression end-to-end and asserts the result.

The fixture is `tests/fixtures/sample-go/` — a tiny two-package Go repo
(`auth`, `payments`) with a clear public surface (`Login`, `ValidateToken`,
`SetSession`, `payments.Charge`).

## Tasks

### 1. Code navigation — locate and find a caller

> Where is `Login` defined, and where is it used? Show me one caller.

Canonical flow: `tree_overview` → `find_symbol(name_path="auth.Login")` →
`find_referencing_symbols(symbol="fn:auth.Login")`.

### 2. Code navigation (deeper) — multi-hop call chain

> What does `Login` call? Show me the call chain two hops deep.

Canonical flow: `find_symbol("auth.Login")` → `node_edges(kinds=["callees"])`
→ for each Tier-0 callee (ValidateToken, SetSession, payments.Charge), drive
`node_edges(kinds=["callees"])` again.

### 3. Repo orientation — top-level structure

> What's the top-level structure of this repo? Just file paths and exported
> names.

Canonical flow: `tree_overview(depth=2)`. Asserts every top-level Go file
appears and every exported name is in the summary.

### 4. Diff impact — public-surface change

> Did the public surface of `auth/login.go` change since `HEAD~1`? List the
> changed symbols.

Canonical flow: `detect_changes(base="HEAD~1", scope=["auth/login.go"])`.
Asserts `Login` is in the changed-symbol list (assuming the fixture's git
history has a `HEAD~1` commit that doesn't include the Login function — see
`tests/fidelity/detectchanges_setup_test.go` for the git setup).

### 5. Cross-language — Python regex search

> Show me every function in this repo named `parse_*`.

Canonical flow: write a tiny `parse_module.py` fixture (`tests/fidelity` does
this in `t.TempDir()` rather than committing it), `search_code` with the
regex `^def parse_`. The fixture's Python file is written by the test itself —
no on-disk sample fixture is committed.

### 6. Mixed — multi-step refactor summary

> I just refactored the `auth` package — give me a summary of what the
> public surface looks like now, and which callers might be affected.

Canonical flow: `tree_overview(scope="auth")` → for each exported name
returned by step 1, drive `find_symbol(name)` and
`find_referencing_symbols(symbol)`. The test asserts the chain produces a
non-empty caller set for at least one of the auth exports (proving the
multi-tool chain ran) — `exclude_tests` isn't a parameter of
`find_referencing_symbols` today, so this task tests "the harness reaches
for the right tools in the right order" rather than "test files are
filtered out".

## How the test exercises the canonical flow

`fidelity_test.go` declares each task as a Go slice of `(toolName, argsJSON)`
pairs. The test walks the slice, snapshotting each step's output. After the
last step it asserts the **final** answer matches the expected domain
result; for tasks with intermediate shape checks, the test asserts each step
returned without error and produced output of the right shape (a non-empty
list, a node ID present, etc.).

This shape is deliberately **not** "the model picks the tools" — it's "this
is the flow a competent agent should follow; the test pins that flow's
outputs". A regression in any step (changed schema, wrong tier wired,
removed method) breaks the test regardless of what a live model would do.

## How a live harness run differs

`live.sh` provides a scaffold for the actual harness evaluation: install
the plugin in a Claude Code session, paste the prompts above one by one,
capture the transcript, and (optionally) score it against a gold transcript.
See `live/README.md` for details.