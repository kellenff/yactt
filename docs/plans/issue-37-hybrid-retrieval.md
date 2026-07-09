# Plan: hybrid retrieval (yactt as structural channel) — issue #37

> **Status:** proposed (pre-implementation). Scope derived from the issue body,
> the related slot issues (#33 MCP, #34 chunker, #35 GraphRAG, #36 packer),
> and the existing `internal/{search,chunker,tool}` packages.

## 1. Goal

Ship a thin reference orchestrator that fans a single query out to
**three retrieval channels** and merges the ranked lists via
**reciprocal rank fusion (RRF)**:

1. **Structural** — yactt's symbol/name/doc/path index (`internal/search`).
   Already a real, existing MCP tool. Unique to yactt: stable IDs, ranked
   by call-graph centrality.
2. **BM25** — tokenized lexical scoring over the chunker-produced AST
   chunks (`internal/chunker.Chunk.Text`). Reusing the chunker output
   means a BM25 backend doesn't need its own document index.
3. **Vector** — cosine over chunker-produced chunks. The reference
   orchestrator ships a **pluggable `VectorBackend` interface** so users
   can wire LangChain / LlamaIndex / Chroma / Qdrant / pgvector
   embeddings. A stdlib-only "bag-of-tokens" reference backend is
   included so the orchestrator runs end-to-end without external deps.

The orchestrator is a **leaf CLI** (`yactt hybrid ...`) plus a small Go
package (`internal/hybrid`) that other tools (LangChain adapter, custom
pipelines) can call directly. No new parsing, no new transitive deps,
no MCP server surface (the per-channel tools are already exposed; the
orchestrator is glue).

## 2. What's already available (no new parsing needed)

| Need                            | Source today                              | Notes |
|---------------------------------|-------------------------------------------|-------|
| Structural (symbol-name + path) | `internal/search.Search`                  | existing |
| Document corpus                 | `internal/chunker.Run` → `[]Chunk`        | existing (issue #34) |
| Stable IDs                      | `chunker.Chunk.ID` (fn:/meth:/class:/module:) | existing |
| File → symbols                  | `store.Repo.SymbolsByPath()`              | existing |
| Call-graph centrality           | `store.Repo.EdgesByCallee(name)`          | existing fan-in signal |

The only new code is:
- a BM25 scorer over chunk texts (no deps — see §3),
- a tiny bag-of-tokens vector backend (no deps — for the reference impl),
- the RRF merge,
- a pluggable `VectorBackend` interface so users can swap in their own,
- the CLI glue + benchmark + docs.

## 3. BM25 in stdlib (no deps)

Robertson–Walker–Jones BM25 is ~30 LOC. We don't need stemming or stop-
word lists; the chunker already produces semantic, narrowly-scoped
chunks (one function/method body), and `internal/tool/search.go`
already tokenizes queries (whitespace + quoted-phrase). The score for
a chunk `D` given query terms `q₁..qₙ` is:

```
IDF(qi) = ln((N - df(qi) + 0.5) / (df(qi) + 0.5) + 1)
score(D, Q) = Σ IDF(qi) * (tf(qi,D) * (k1 + 1)) /
                       (tf(qi,D) + k1 * (1 - b + b * |D|/avgdl))
```

Defaults: `k1 = 1.2`, `b = 0.75`. Document length-normalized. Same
formula every textbook uses; no clever tricks.

## 4. Package layout

```
internal/hybrid/
  hybrid.go        // types: Hit, Channel, RRF merge, Options
  structural.go    // adapter: structural channel over internal/search
  bm25.go          // stdlib BM25 scorer over []chunker.Chunk
  vector.go        // VectorBackend interface + BagOfTokens reference impl
  hybrid_test.go   // unit tests (RRF math, BM25 score, pluggable vector)

cmd/yactt/
  main.go          // add `yactt hybrid ...` subcommand
  main_test.go     // add CLI smoke test

examples/hybrid/
  README.md        // how to plug your own vector backend
  langchain.py     // minimal LangChain adapter (out-of-scope to ship,
                   // included as a worked example; #37 scope says "doc
                   // the integration shape" not "ship LangChain").
  llamaindex.py    // minimal LlamaIndex adapter.

docs/
  hybrid-retrieval.md   // the 1-page integration guide (success criterion)

tests/hybrid/
  README.md        // benchmark methodology
  eval_test.go     // hold-out (query, expected-symbol-id) pairs
  recall_set.jsonl // 30+ hand-tagged pairs over a known repo
```

## 5. Public API

```go
// Package hybrid is the reference orchestrator for issue #37: it
// fans a query out to (a) yactt's structural index, (b) BM25 over
// chunker-produced chunks, (c) a pluggable vector backend, and merges
// the three ranked lists via reciprocal rank fusion (RRF).
package hybrid

// Hit is a single ranked match surfaced by a channel. Channel is one
// of "structural"|"bm25"|"vector". Score is the per-channel score in
// that channel's natural scale (the orchestrator doesn't normalize —
// RRF uses rank, not score).
type Hit struct {
    ID      string  // canonical yactt ID (fn:auth.Login, meth:auth.User.Greet, ...)
    Score   float64 // per-channel score (debug + per-channel output)
    Channel string  // "structural" | "bm25" | "vector"
    Chunk   *chunker.Chunk // nil for structural; populated for bm25/vector
}

// Options configures one Run call.
type Options struct {
    Repo     *store.Repo          // required: the repo under search
    Query    string               // required: the natural-language query
    Limit    int                  // final top-K; default 10
    Overscan int                  // per-channel overscan factor; default 4
    // Channels lets the caller disable a channel. All three default true.
    // Structural=false disables the yactt-specific channel — useful for
    // benchmarking "did the structural channel help?" but rarely what
    // production wants.
    Channels Channels
}

// Run fans the query out to the enabled channels and returns the
// merged, RRF-ranked top-K hits.
func Run(ctx context.Context, opts Options) ([]Hit, error)
```

## 6. CLI surface

```sh
yactt hybrid --repo <path> --query "<natural language>" [options]

Options:
  --repo <path>          Repository root (required).
  --query "<string>"     Query string (required).
  --limit <n>            Final top-K; default 10.
  --channels <csv>       Which channels to enable: structural,bm25,vector
                         (default all three). Repeat per-channel to disable.
  --include-tests        Include test files in the chunker corpus.
  --json                 Emit the merged result as JSON (default).
  --explain              Per-channel output side-by-side: structural / bm25
                         / vector / rrf-merged, each with channel scores.
```

`--explain` is the must-have for the success criterion ("benchmark
shows hybrid > each single channel on ≥70% of held-out questions"):
it makes the per-channel ranking visible so the user can see *why*
hybrid wins.

## 7. Reciprocal rank fusion

The 2009 Cormack et al. formula. For each document `d`:

```
rrf(d) = Σ_{c ∈ channels} 1 / (k + rank_c(d))
```

Where `rank_c(d)` is the document's rank in channel `c` (1-indexed;
missing = `len(channel) + 1` so it still contributes a tiny score
rather than being dropped). `k = 60` is the well-known default that
damps the impact of top-ranked hits in any one channel.

## 8. Pluggable VectorBackend

```go
// VectorBackend produces a vector for a chunk's text and scores a
// query against a pre-built index of chunk vectors. Implementations
// typically wrap an external service (OpenAI, Cohere, Ollama, a local
// model, pgvector, Chroma, Qdrant, ...).
//
// Implementations are responsible for their own caching, batching,
// and remote I/O. The orchestrator only calls these two methods.
type VectorBackend interface {
    // Index returns a handle that Query can score against. The
    // orchestrator calls Index once per Run; implementations cache
    // embeddings across calls.
    Index(ctx context.Context, chunks []chunker.Chunk) (VectorIndex, error)
}

// VectorIndex scores a query against a previously-built chunk index.
type VectorIndex interface {
    // TopK returns the top-K (chunk-id, score) pairs by descending
    // similarity. Implementations choose their own distance metric
    // (typically cosine) and score range.
    TopK(ctx context.Context, query string, k int) ([]VectorHit, error)
}

type VectorHit struct {
    ChunkID string  // matches chunker.Chunk.ID
    Score   float64 // higher = more similar (cosine in [-1,1] is reshaped to [0,1])
}
```

The shipped `BagOfTokens` reference backend hashes tokens to a fixed-
dim vector, L2-normalizes, and scores cosine — purely so the
orchestrator runs end-to-end with zero deps. It's not competitive
with a real embedding model; it's a test harness.

## 9. Benchmark (success criterion)

Held-out `(query, expected-yactt-id)` pairs over the
`tests/chunking/genfixture` 100-file synthetic repo (already in-tree,
re-use it). 30+ hand-tagged pairs:

- 10 method-level: "where is `<Type>.<Method>` in the auth domain?"
- 10 function-level: "where is `<Func>` handled for the payments package?"
- 10 class-level: "what is the `<Type>` type for the users domain?"

For each pair, score `@1` and `@5` for:

- structural-only
- bm25-only
- vector-only (with BagOfTokens reference backend)
- hybrid (RRF merge of all three)

Assert: hybrid's `@1` win-rate vs each single channel is ≥70%
over the 30 questions. Report the table.

The benchmark lives in `tests/hybrid/eval_test.go` and runs with
`go test ./tests/hybrid/...` (no Ollama required — uses BagOfTokens).
This is the same pattern as the chunker benchmark gate.

## 10. Documentation (success criterion)

`docs/hybrid-retrieval.md` — one page, end-to-end:

1. What yactt contributes (the structural channel, in one paragraph).
2. A 5-line code snippet: wire `hybrid.Run` against your existing
   vector store.
3. Worked example for LangChain + LlamaIndex (Python stubs in
   `examples/hybrid/`, referenced from the doc).
4. The benchmark numbers (links to `tests/hybrid/README.md`).
5. "Don't over-invest" footnote: yactt is the structural channel,
   not a RAG framework. Plug, don't fork.

## 11. Non-goals (re-stated from the issue)

- No vector store, no BM25 index, no reranker in-tree.
- No multi-repo. Single repo at a time.
- No LLM-in-the-loop. RAG is the orchestrator's user.
- No new transitive deps. Stdlib only for the in-tree code.

## 12. Success criteria (from the issue)

1. ✅ Reference orchestrator runs end-to-end on the existing sample
   repo with all three channels (`yactt hybrid --repo <fixture>
   --query "auth login"` returns structural + BM25 + vector hits merged).
2. ✅ Benchmark shows hybrid ≥70% `@1` win-rate vs each single
   channel on held-out questions (`tests/hybrid/eval_test.go`).
3. ✅ 1-page integration guide (`docs/hybrid-retrieval.md`) with
   LangChain + LlamaIndex adapter examples.