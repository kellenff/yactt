# Benchmarks

yactt has two benchmark layers:

1. **Performance benchmarks** — `func Benchmark*` functions in `*_bench_test.go`
   files. Measure hot-path latency so regressions surface in `go test -bench`.

2. **Fidelity / tool-use benchmarks** — `tests/fidelity/`. Measure whether a
   real harness (Claude Code + the plugin) can reach for the right tools in
   the right order to complete representative code-intelligence tasks.

Both layers reuse existing infrastructure: `internal/store/repofixture/`,
`tests/fixtures/sample-go/`, the `tool.*Handler` signatures, and the
`tests/acceptance/` pattern.

## Performance benchmarks

### How to run

```bash
# All benchmarks, default 1s per benchmark.
go test -bench=. -benchmem ./...

# A specific package.
go test -bench=. -benchmem ./internal/parser/...

# A specific benchmark.
go test -bench='^BenchmarkToolNodeGet' -benchmem ./internal/tool/...

# HTTP MCP transport (small + medium project sizes).
go test -bench='^BenchmarkHTTP' -benchmem ./internal/mcp/transport/http/...

# External Go HTTP client path (real TCP listener).
go test -bench='^BenchmarkHTTPClient' -benchmem ./internal/mcp/transport/http/...

# Large real-world fixture only (fastify/fastify; clones once into
# tests/fixtures/.cache/, then offline-safe).
go test -bench='ToolsCall/large' -benchmem ./internal/mcp/transport/http/...

# Heavy benchmarks (LoadFixture, ReparseOneFile) benefit from longer benchtime.
go test -bench='^BenchmarkLoadFixture$' -benchtime=10x ./internal/store/...
```

### Reading the output

```
goos: darwin
goarch: arm64
pkg: github.com/kellenff/yactt/internal/parser
cpu: Apple M3 Max
BenchmarkParseGolang-14    	   15230	    78543 ns/op	    32896 B/op	     212 allocs/op
PASS
```

- `ns/op` — wall-clock time per iteration. Lower is better.
- `B/op` — bytes allocated per iteration. Watch for unexpected allocations
  when adding caches or new code paths.
- `allocs/op` — allocation count per iteration. A new allocs/op count is a
  hint that something now allocates per-call where it didn't before.

### Where to look when a benchmark regresses

| Benchmark | Package | What regressed |
|---|---|---|
| `BenchmarkParseGolang` / `BenchmarkParsePython` / `BenchmarkParseTypeScript` | `internal/parser/` | Tree-sitter grammar, lang-detection |
| `BenchmarkDetectLanguage` | `internal/parser/` | Extension→Language mapping |
| `BenchmarkFor_Kinds` | `internal/id/` | ID encoding |
| `BenchmarkJoinDotted` | `internal/id/` | Dotted-segment joiner |
| `BenchmarkLRU_*` | `internal/cache/` | In-memory cache |
| `BenchmarkDiskCache_*` | `internal/cache/` | Disk-backed cache |
| `BenchmarkSearch` | `internal/search/` | BM25/regex search |
| `BenchmarkLoadFixture` / `BenchmarkReloadInvalidate` | `internal/store/` | Repo walk + Tier-0 rebuild |
| `BenchmarkLocateSymbol` | `internal/store/` | By-ID lookup |
| `BenchmarkEdgesByCallee` / `BenchmarkEdgesByCaller` / `BenchmarkImportsIn` | `internal/store/` | Persisted index accessors |
| `BenchmarkDispatchToolsCall` / `BenchmarkDispatchToolsList` / `BenchmarkRegisterTool` | `internal/mcp/` | JSON-RPC dispatch + tool registry |
| `BenchmarkHTTP_Initialize` / `BenchmarkHTTP_ToolsList` / `BenchmarkHTTP_ToolsCall` | `internal/mcp/transport/http/` | MCP over Streamable HTTP via `httptest` (`serve-http`); `ToolsCall` sweeps small (repofixture), medium (genfixture 100-file), and large (fastify/fastify @ pinned SHA) |
| `BenchmarkHTTPClient_Initialize` / `BenchmarkHTTPClient_ToolsList` / `BenchmarkHTTPClient_ToolsCall` | `internal/mcp/transport/http/` | Same MCP surface through a dedicated `*http.Client` against a real loopback TCP listener (agent-harness shaped); same size matrix including large/fastify |
| `BenchmarkToolTreeOverview` | `internal/tool/` | `tree_overview` handler |
| `BenchmarkToolFindCode` / `BenchmarkToolFindSymbol` / `BenchmarkToolNodeEdges` / `BenchmarkToolFindReferencingSymbols` | `internal/tool/` | The navigation tools |
| `BenchmarkToolNodeGet_signature` / `_body` / `_source` | `internal/tool/` | Layer materialisation; signature < body < source in cost |

### Caveats

- **LSP availability affects results.** `BenchmarkTool*` benches hit
  Tier-1 LSP paths when `gopls`/`typescript-language-server`/`pyright-langserver`
  are on PATH; otherwise they fall back to tree-sitter-only Tier 2. The
  bench reports the cost in *the current environment* — useful as a
  regression detector, less useful as an absolute number.
- **`BenchmarkLoadFixture` rebuilds the fixture per iteration.** That
  makes it expensive — run with `-benchtime=10x` to keep wall time sane.
- **Benchmark files are `_bench_test.go`, not `_test.go`.** `go test ./...`
  (the CI gate) picks them up for `go vet` but not for execution; the
  `Benchmark*` only runs under `-bench`.

## Fidelity / tool-use benchmarks

### How to run (scripted)

The scripted layer lives in `tests/fidelity/` and runs as part of
`go test ./tests/fidelity/...`. Each `TestFidelity_TaskN` drives the
canonical tool progression an agent is expected to take and asserts
both intermediate-step shapes and the final domain result.

```bash
# All fidelity tests.
go test ./tests/fidelity/...

# One task.
go test -v -run='TestFidelity_Task3' ./tests/fidelity/...
```

The six tasks and their canonical flows are documented in
[`tests/fidelity/TASKS.md`](../tests/fidelity/TASKS.md).

### What the tests actually pin

A regression in any step (changed schema, wrong tier wired, removed
method) breaks the test, regardless of what a live model would do. The
per-step assertions catch issues like:

- "Tier 1 broke for `node_edges(kinds=['callees'])` but the final answer
  still happens to look right because the fallback ran" — currently
  invisible to `tests/acceptance/`.
- "`id.For` output changed shape and `find_referencing_symbols` now
  rejects the ID at the boundary" — caught at step 3 of every navigation
  task.

### How to run (live harness)

For an actual agent evaluation, install the plugin in Claude Code
and use `tests/fidelity/live.sh`:

```bash
# All 6 tasks.
./tests/fidelity/live.sh

# Subset.
./tests/fidelity/live.sh 1 3 5
```

The script captures raw tool-call transcripts to
`tests/fidelity/live/transcripts/<timestamp>/` and generates a
`SCORES.md` template. See `tests/fidelity/live/README.md` for the
full workflow.

The live layer is **manual, not a CI gate** — it requires a real
Claude Code session. The scripted layer covers the regression axis
cheaply; the live layer covers "is a real agent actually able to use
this plugin?"

## What is NOT benchmarked (and why)

- **Production code paths under concurrent load.** A single bench
  loop is sequential. If you suspect a contention bug, write a
  `t.Parallel()` test or a stress harness — both are out of scope for
  these benches.
- **Real repos (except the pinned large fixture).** Most benches use
  inline fixtures (repofixture, sample-go, or t.TempDir). The HTTP MCP
  benches also include a **large** size: `fastify/fastify` at a pinned
  SHA via `tests/fixtures/realrepo`, cached under
  `tests/fixtures/.cache/` (gitignored). First run needs network +
  `git`; later runs are offline. To bench against your own repo, copy
  a benchmark file and swap the fixture loader.
- **Tool-layer ID consistency regressions.** Each tool handler
  round-trips args through `json.RawMessage` literals, so a wire-
  schema rename surfaces as a parse error — not a silent miss. The
  `internal/contract/contract_test.go` suite enforces this more
  rigorously at the unit level.