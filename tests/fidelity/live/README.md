# Live harness runner

This directory holds transcripts from manual harness runs of the six
fidelity tasks documented in `../TASKS.md`. It's a scaffold for
occasional offline evaluation; nothing in here is gated in CI.

## Workflow

1. **Install the plugin** in your Claude Code session:
   ```
   /plugin marketplace add kellenff/yactt
   /plugin install yactt@yactt
   ```

2. **Open a Claude Code session** in this repo's root. The plugin's
   `.mcp.json` registers the server with `${CLAUDE_PROJECT_DIR}` rooted
   at the cwd, so the sample-go fixture at
   `tests/fixtures/sample-go/` is the default repo.

3. **Run the scaffold**:
   ```
   ./tests/fidelity/live.sh                # all 6 tasks
   ./tests/fidelity/live.sh 1 3 5          # subset
   ```
   The script prints each prompt, pauses for you to paste the
   transcript, and writes the captured transcripts to
   `transcripts/<timestamp>/task-N.json`.

4. **Score the run** by editing the generated `SCORES.md`. For each
   task, mark Solved = Yes/No and capture any deviation from the
   canonical flow documented in `../TASKS.md`.

## Transcript format

Each `task-N.json` is the raw tool-call transcript pasted from Claude
Code's `/context` or transcript export. The format is intentionally
loose — the scaffold doesn't enforce a schema, because harness
transcripts vary across Claude Code versions and capture modes.

A future iteration could parse the transcript into a structured
sequence of `{tool, args, result}` triples and compare it against a
gold `golden/task-N.json`. That work is a follow-up; this scaffold is
the minimum scaffolding that captures the data we'd need.

## When to run this

The live harness is **manual, not a CI gate**. Run it when:

- you've changed a tool's wire schema (you want to confirm a real agent
  still picks the right args after the rename)
- you've added a new tool and want to confirm a real agent actually
  reaches for it
- you've changed the canonical-flow guidance in `../TASKS.md` and want
  to confirm a real agent's natural progression still converges on the
  same shape

It's not worth running for every commit — the scripted tests in
`../fidelity_test.go` cover the regression axis cheaply.

## Why this is a separate folder

`tests/fidelity/transcripts/` is gitignored — these are local artifacts.
The git-tracked layer above is just this README and `../TASKS.md`.
Gold transcripts, when they exist, should live in
`tests/fidelity/live/golden/task-N.json` and be committed alongside
TASKS.md so reviewers can diff the live run against the expected.