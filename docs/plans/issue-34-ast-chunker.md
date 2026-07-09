# Plan: AST-aware chunker for vector-store ingest (issue #34)

> **Status:** proposed (pre-implementation). Scope derived from the issue body
> and an audit of the current parser/store/entity layer.

## 1. Goal

Ship a thin chunker that emits **one chunk per function/method/class/module**
using yactt's existing tree-sitter boundaries — not character-count windows.
Output is NDJSON on stdout, designed to pipe into pgvector/qdrant/chroma.

The chunker is a **leaf** package, not a service. It takes a `*store.Repo`
and a `Policy`, walks the parsed symbols, and writes chunks. No I/O of its
own (other than the writer the caller hands it), no caching, no
embeddings, no MCP server. The `yactt chunk` subcommand is the only
caller in the repo; external callers (CI, ingestion scripts) link the
package directly.

## 2. What's already available (no new parsing needed)

| Need                     | Source today                                     | Notes for chunker |
|--------------------------|--------------------------------------------------|-------------------|
| Symbol list per file     | `store.Repo.SymbolsByPath() map[string][]parser.Symbol` | already populated at Load |
| Symbol body bytes        | `source.File.Bytes` (from `Repo.CachedFile(path)`) + `Symbol.StartRow/EndRow` | row-based slicing via existing `store.ByteRangeFromRows` helper |
| Doc comments             | `store.Repo.DocComment(path, sym)`              | prepend to chunk text |
| Stable ID                | `entity.Entity.ID()` (`fn:`, `meth:`, `class:`, `module:`) | single source of truth, *not* re-derive |
| Qualified name           | `entity.Entity.pkg + "." + name` (via `id.JoinDotted`) | one chunk field, used for LangChain metadata |
| Callers                  | `store.Repo.EdgesByCallee(name)`                | already indexed; per-symbol Caller list |
| Callees                  | `store.Repo.EdgesByCaller(file, sym)`            | already indexed; per-symbol Callee list |
| Signature                | `store.Repo.signatureMaterializer` (private)     | needs to be exported OR inlined; see §5 |
| File:line                | `Symbol.StartRow` (0-based)                      | convert to 1-based in the chunk for human display |
| Language                 | `source.File.Grammar.Name()`                     | metadata field |
| File pkg                 | `store.pkgFromPath(root, path, rootPkg)` (private) | needs a public `Repo.PackageOf(path)` helper; see §5 |

**No new parsing code is required.** The chunker is a layer above
`store.Repo` plus one new helper for the private materializers.

## 3. Package layout

```
internal/chunker/
  chunker.go         // Policy, Chunk, Run; the public API
  chunker_test.go    // unit tests against repofixture + a tiny inline repo
  langchain.go       // optional Document adapter (see §7)
  llamaindex.go      // optional Document adapter (see §7)

cmd/yactt/main.go    // add `yactt chunk` subcommand
cmd/yactt/main_test.go (existing)  // add a CLI smoke test

tests/chunking/
  benchmark_test.go  // 100-file synthetic repo, measure wall time + recall
  recall_set.jsonl   // 30-50 hand-tagged (question, expected-symbol) pairs
```

`internal/chunker` is small (~400 LOC) and self-contained. It depends on
`store`, `entity`, `parser`, `domain`, `id`, and `summarizer` — all
already in the module. No new transitive deps.

## 4. Public API

```go
// Package chunker turns a parsed store.Repo into AST-bounded chunks
// suitable for vector-store ingest. See issue #34.
package chunker

// Policy selects the granularity of emitted chunks.
type Policy string

const (
    PolicyFunction Policy = "function" // default: one chunk per function/method body
    PolicyClass    Policy = "class"    // one chunk per class, methods listed
    PolicyModule   Policy = "module"   // one chunk per file (fallback)
)

// Chunk is the on-the-wire shape. Field names are stable; downstream
// adapters (langchain.go, llamaindex.go) read them directly.
type Chunk struct {
    ID            string   `json:"id"`            // canonical: fn:auth.Login
    QualifiedName string   `json:"qualified_name"`// auth.Login
    Kind          string   `json:"kind"`          // FUNCTION | METHOD | CLASS | MODULE
    Language      string   `json:"language"`      // go | typescript | ...
    File          string   `json:"file"`          // absolute path
    StartLine     int      `json:"start_line"`    // 1-based
    EndLine       int      `json:"end_line"`      // 1-based, inclusive
    Signature     string   `json:"signature"`     // decl line + leading doc comment
    Text          string   `json:"text"`          // the chunk payload (body or class overview)
    Callers       []string `json:"callers"`       // canonical IDs of callers
    Callees       []string `json:"callees"`       // canonical IDs of callees
    Policy        Policy   `json:"policy"`        // the policy that produced this chunk
}

// Options tunes a single Run call. Zero value uses PolicyFunction.
type Options struct {
    Policy     Policy          // default: PolicyFunction
    Languages  []parser.Name   // nil = all wired languages
    Include    []string        // filepath.Match globs (relative to root); nil = all
    Exclude    []string        // filepath.Match globs; default hides *_test.go if WithTests is false
    WithTests  bool            // default false (test files bloat recall sets)
    MaxChunks  int             // 0 = unlimited; cap for sanity in CI
}

// Run walks the repo once and writes one NDJSON line per chunk to w.
// Returns the number of chunks emitted and the first non-fatal error
// (if any). A write error short-circuits and is returned.
func Run(ctx context.Context, r *store.Repo, opts Options, w io.Writer) (int, error)
```

### 4.1 Output contract (NDJSON)

One `Chunk` per line, JSON, terminated by `\n`. Example (one line):

```
{"id":"fn:auth.Login","qualified_name":"auth.Login","kind":"FUNCTION","language":"go","file":"/abs/auth/login.go","start_line":17,"end_line":36,"signature":"// Login authenticates a user by token...\nfunc Login(token string) (string, error)","text":"<full body>","callers":[],"callees":["fn:auth.ValidateToken","fn:auth.SetSession","fn:payments.Charge"],"policy":"function"}
```

A trailing summary line is **not** emitted; downstream tools should
`wc -l` to count chunks. (Adding a summary footer would break piping.)

## 5. Required store/entity surface changes

These are small and mechanical — they unblock the chunker without
altering the public contract of `store.Repo`.

| Change | Where | Why |
|--------|-------|-----|
| Export `signatureMaterializer` (rename to `Signature` on a new exported `RepoMaterializer` value, or expose `Repo.Signature(path, sym) string`) | `internal/store/node.go` | chunker needs the signature without re-walking the AST |
| Add `Repo.PackageOf(path string) string` wrapping the package-private `pkgFromPath` | `internal/store/resolver.go` | chunker needs the package name for `qualified_name` |
| Add `Repo.SymbolID(path, sym) (string, error)` returning `entity.FromParser(sym, file, pkg).ID()` | `internal/store/resolver.go` | single source of truth — chunker shouldn't re-derive canonical IDs from grammar strings |

These three accessors mirror existing private helpers 1:1, so the diff
is small. Each gets a `_test.go` case so the contract is pinned.

**Do not** add a `Repo.Chunks()` method. The chunker is a separate
package for a reason: it composes *across* the parser/store layers
rather than living inside them, and external callers (ingestion
scripts) link it directly without a `Repo` value.

## 6. Algorithm (per policy)

For each file `f` in `Repo.Files()` (filtered by `Options.Include` / `Options.Exclude` and `Options.Languages`):

1. `pkg := Repo.PackageOf(f)`, `lang := grammar.Name(f)`.
2. `syms := Repo.Symbols(f)`, grouped by grammar kind.

### 6.1 PolicyFunction (default)

For each `s` in `syms` where `s.Kind` ∈ `function_declaration`, `method_declaration`:

- `start, end := ByteRangeFromRows(file.Bytes, s.StartRow, s.EndRow)` — reuse the existing helper.
- `body := string(file.Bytes[start:end])`.
- `sig := Repo.Signature(f, s)` (newly exported).
- `text := leadingDocComment(f, s) + "\n" + sig + "\n" + body` (only if a doc comment exists; else just `sig + "\n" + body`).
- `id := Repo.SymbolID(f, s)`.
- `callers := calleeIDs(Repo.EdgesByCallee(s.Name))` (deduped, canonically formatted).
- `callees := callerIDs(Repo.EdgesByCaller(f, s))` (deduped).
- emit `Chunk`.

Methods (`s.Kind == "method_declaration"`) get identical treatment —
the canonical ID includes the receiver (`meth:auth.User.Greet`), so
callers/callees keying just works.

### 6.2 PolicyClass

For each `s` in `syms` where `s.Kind` is a class-shaped grammar
(type_declaration, class_declaration, struct_declaration, etc.):

- Body = the entire class block (same `ByteRangeFromRows`).
- Methods listed in the `Text` are *signatures only* (no method bodies).
  Build a synthetic overview: `text = sig + "\n\nMethods:\n" + join(method_sigs, "\n")`.
- The same `id`/`callers`/`callees` logic applies, but `callees` for a
  class is the union of its methods' callees (deduped, stable order).
- `Repo.signatureMaterializer` already returns just the decl line, so
  iterating `syms` for method-shaped kinds inside the class's row range
  gives the method signatures.

### 6.3 PolicyModule

One chunk per file. `id` = `module:<pkg>.<fileBase>`, `Text` = full
file bytes, `callers`/`callees` = union over the file's symbols.

This is the fallback for tiny files (≤ N lines, default 10) when
PolicyFunction would produce mostly-empty chunks; the chunker auto-downgrades
to PolicyModule for files under the threshold unless the caller passes
`Options.MaxChunks=0` *and* explicitly opts in. The downgrade is logged to
stderr by the CLI; the chunk policy field still says the *requested* policy
so callers can detect it.

### 6.4 Stability guarantees

- Output is deterministic for a given `(commit, policy, options)`:
  no goroutines, no map iteration order in the emitted JSON. Use a
  sorted slice of symbols (sort by `(file, startRow, name)`) so two
  runs produce byte-identical output.
- ID is the canonical `entity.Entity.ID()` form. Cross-chunk refs
  (`callers`/`callees`) round-trip through `id.Parse`.

## 7. Adapters (optional, cheap)

`langchain.go` and `llamaindex.go` each provide one function that
emits the downstream library's `Document` shape:

```go
// LangChainDocument returns a map[string]any shaped like
// langchain_core.documents.Document: {"page_content": ..., "metadata": {...}}.
// All metadata fields are present in the source Chunk.
func LangChainDocument(c Chunk) map[string]any
```

Why the adapter lives inside `internal/chunker`: the wire shape is
identical, and it lets a 20-line caller do
`json.NewEncoder(qdrant).Encode(chunker.LangChainDocument(c))` with
no copy-paste of field names. Both adapters total < 50 LOC and are
behind a `//go:build langchain` / `//go:build llamaindex` constraint
once the optionality is exercised — until then they're unconditional
maps so callers don't need build tags.

**Recommendation:** defer these to a follow-up issue. The base package
+ CLI is the issue-34 deliverable; adapters are a 1-day add-on.

## 8. CLI: `yactt chunk`

Add to `cmd/yactt/main.go` alongside `overview` and `mcp serve`:

```
yactt chunk --repo <path> [--policy function|class|module]
            [--include GLOB]... [--exclude GLOB]... [--with-tests]
            [--languages go,typescript,...] [--max-chunks N]
            [-o FILE]

Writes NDJSON to -o (default stdout). Exit 0 on success,
1 on load error, 2 on usage error. Stderr gets the per-file
parse-error count (mirrors `yactt overview`).
```

Update the `usage` string and add `case "chunk":` to the `main()`
switch — same shape as the existing `case "overview":`.

**Required tests** (in `cmd/yactt/main_test.go`):
1. `--help` exit code 0, mentions `chunk`.
2. `chunk` with no args exits 2.
3. End-to-end: `yactt chunk --repo <sample-go>` produces ≥ N NDJSON lines,
   each line is valid JSON, and every line has non-empty `id`,
   `qualified_name`, `file`, `text`.
4. `--policy class` produces fewer lines than `--policy function` on
   the same repo (sanity: classes < functions).
5. `chunk` against a non-repo path exits 1 with "load: …" on stderr.

## 9. Tests

### 9.1 Unit (`internal/chunker/chunker_test.go`)

Use `internal/store/repofixture` (already a thing) + a tiny inline
Go source for the doc-comment + signature cases.

| Test | What it pins |
|------|--------------|
| `TestRun_FunctionPolicy_EmitsOneChunkPerSymbol` | the headline contract |
| `TestRun_MethodHasReceiverInID` | `meth:auth.User.Greet` shape |
| `TestRun_CallersCalleesPopulatedForCrossPackageCall` | edges wiring |
| `TestRun_ClassPolicyListsMethodSignatures` | text shape |
| `TestRun_ModulePolicyEmitsOneChunkPerFile` | fallback |
| `TestRun_AutoDowngradeForTinyFile` | small-file → module |
| `TestRun_OutputIsDeterministic` | two runs → byte-identical |
| `TestRun_RespectsIncludeExclude` | glob filtering |
| `TestRun_RespectsWithTests` | `_test.go` inclusion |
| `TestRun_MaxChunksCaps` | cap honoured, returns count |
| `TestRun_NilLanguagesIncludesAll` | default policy |
| `TestChunk_RoundTripJSON` | all fields survive Marshal/Unmarshal |

### 9.2 Benchmark / recall (`tests/chunking/benchmark_test.go`)

The issue's "≥2× retrieval recall" success criterion needs a fixture
and a metric. The plan:

1. **Fixture**: a synthetic 100-file Go repo generated by
   `tests/chunking/genfixture/main.go` (write the generator; it's 60
   LOC). 100 files × ~5 functions each ≈ 500 chunks. The repo
   references 3 third-party packages so cross-package call edges
   exist. The generator is deterministic (seeded `math/rand`).

2. **Recall set** (`recall_set.jsonl`): 30 hand-tagged
   `(question, expected_symbol_id)` pairs. Example:
   `{"q":"where is the rate limiter reset","id":"fn:pkg.RateLimiter.Reset"}`.
   Hand-tag = the issue author, not a model. The set lives in git
   under `tests/chunking/` so it's reviewable.

3. **Baseline chunker** (char-count, 1000-char windows, 200-char
   overlap): 40 LOC in the same package under
   `internal/chunker/baseline.go` so the comparison is in one place.

4. **Retrieval backend**: the in-memory `nomic-embed-text` via Ollama
   (already installed on this machine per the local RAG setup) +
   brute-force cosine. Embeddings are cached by chunk ID on disk
   under `tests/chunking/.cache/`.

5. **Metric**: recall@5 and recall@10. AST chunker should win by
   ≥2× on recall@5 per the issue. If it doesn't, the plan's
   success criterion is unmet and the work isn't done.

6. **Wall-time**: `BenchmarkRun_100Files` (Go bench) on the
   synthetic repo. Budget: ≤ 10s per the issue. Report via
   `go test -bench=. -benchtime=1x` in CI.

## 10. Success-criteria mapping (issue → artifact)

| Issue criterion | Artifact |
|-----------------|----------|
| 100-file repo in ≤10s | `BenchmarkRun_100Files` (output in `docs/benchmarks.md` update) |
| ≥2× recall vs char-count | `tests/chunking/benchmark_test.go` TestRun_Recall_ASTBeatsBaseline |
| Drop-in (20-line caller + pgvector) | `docs/quickstart-chunker.md` recipe + the LangChain adapter (if shipped) |

## 11. Risks / open questions

1. **Signature materializer is private.** Two options: (a) export
   `Repo.Signature(path, sym)`, or (b) re-walk the parse tree in the
   chunker. (a) is ~3 LOC; (b) duplicates code. Recommend (a).
2. **Class-shaped "method signatures inside the chunk"** depends on
   PolicyFunction having run first (for the canonical ID lookup), OR
   the chunker calling `entity.FromParser` inline. Inline is fine
   since the chunker already imports `entity`. No circular dep.
3. **Tests in CI.** The recall benchmark pulls Ollama. Two options:
   (a) gate it behind `-short` and skip in normal CI, (b) cache
   embeddings so the bench runs in <5s. (b) is the right answer —
   the cache lives in `tests/chunking/.cache/` and is gitignored.
4. **`Exclude` defaults.** The default should hide `*_test.go`
   unless `--with-tests`. Pin in `Options` so the policy is
   explicit, not implicit in a hidden helper.
5. **Module policy for tiny files.** The auto-downgrade threshold
   needs a constant; default 10 lines (one-line decls + body). Make
   it configurable via env (`YACTT_CHUNK_TINY_FILE_LINES`) for
   downstream callers that disagree.
6. **`cmd/yactt-chunker` vs `yactt chunk`.** The issue says "or
   `cmd/yactt-chunker`". Recommend `yactt chunk` — it lives in the
   same binary, ships in every release, and doesn't add a second
   build target to maintain. Trivial to split out later if a
   consumer wants a tiny static binary.

## 12. Out of scope (per issue)

- Vector store construction. Emit chunks, period.
- Cross-repo indexing. One repo at a time.
- Embedding generation. Caller's choice.
- LangChain / LlamaIndex adapters. Deferred to a follow-up
  (one-day add-on). Will not block issue-34 close.

## 13. File-by-file diff estimate

| File | New/Modified | LOC delta |
|------|--------------|-----------|
| `internal/store/node.go` | modified | +8 (export Signature) |
| `internal/store/resolver.go` | modified | +20 (PackageOf, SymbolID) |
| `internal/store/node_test.go` (or new) | modified | +20 (one test per new method) |
| `internal/chunker/chunker.go` | new | ~280 |
| `internal/chunker/baseline.go` | new | ~50 |
| `internal/chunker/chunker_test.go` | new | ~250 |
| `internal/chunker/langchain.go` | new (deferred) | ~30 |
| `internal/chunker/llamaindex.go` | new (deferred) | ~30 |
| `cmd/yactt/main.go` | modified | +50 (chunk subcommand + usage) |
| `cmd/yactt/main_test.go` | modified | +80 (5 CLI tests) |
| `tests/chunking/genfixture/main.go` | new | ~60 |
| `tests/chunking/benchmark_test.go` | new | ~150 |
| `tests/chunking/recall_set.jsonl` | new | 30 hand-tagged lines |
| `docs/plans/issue-34-ast-chunker.md` | new | this file |

Total: ~1,000 LOC, ~13 files. Sized for a single PR.

## 14. Suggested PR sequencing

1. PR 1: export `Repo.Signature`, `Repo.PackageOf`, `Repo.SymbolID`
   (with tests). Pure store-layer change, no chunker yet.
2. PR 2: `internal/chunker` package + unit tests. No CLI, no
   benchmark. Land behind an internal flag.
3. PR 3: `yactt chunk` CLI + `cmd/yactt` tests.
4. PR 4: `tests/chunking/` benchmark + recall set + baseline
   chunker. This is the gate on the "≥2× recall" success criterion.

PR 1 + PR 2 + PR 3 close the issue's scope. PR 4 satisfies the
benchmark criterion — it's optional for the issue but cheap, so
include it unless recall is hard to demonstrate on the synthetic
fixture (in which case swap to a real OSS repo and document the
result).
