# Hybrid retrieval — adding yactt as the structural channel

> **Status.** Reference orchestrator shipped in `internal/hybrid` + `cmd/yactt
> hybrid`. This page is the integration guide — how to plug yactt into your
> existing RAG pipeline as the **structural channel** alongside BM25 and
> vector retrieval.

## What yactt contributes

In a hybrid retriever (vector + BM25 + structural graph), yactt plays the
**structural channel**. Concretely, given a natural-language query, yactt
returns ranked symbol hits with:

- **Stable IDs** — `fn:auth.Login`, `meth:auth.User.Greet`, `class:auth.Session`.
  Survives renames in the symbol itself (only moves when the declaration moves).
- **Structured metadata** — every hit carries signature, doc comment, file +
  line range, callers, callees, language, kind.
- **Call-graph centrality signal** — symbol-name + doc + path proximity, ranked
  by match strength (existing `internal/search` package).
- **Single repo, no I/O** — the structural channel runs in-memory against
  the parsed symbol index. No remote calls, no model download, no GPU.

What yactt does **not** do:

- No embedding model, no vector store, no reranker.
- No LLM in the loop.
- No multi-repo (one repo at a time, indexed in-process).

Plug your existing BM25 + vector backends alongside yactt's structural channel.
The orchestrator fans the query out, then merges the three ranked lists via
**reciprocal rank fusion (RRF)** with `k=60` (Cormack 2009 default).

## End-to-end CLI

The reference orchestrator ships as `yactt hybrid`:

```bash
yactt hybrid --repo /path/to/repo --query "where is the auth login" \
  --limit 10 --explain
```

Output (default):

```jsonc
{
  "results": [
    {
      "id": "fn:auth.Login",
      "score": 0.049...,         // RRF-merged score
      "channel": "rrf",
      "chunk": {                 // populated for BM25 / vector hits; null for structural
        "id": "fn:auth.Login",
        "qualified_name": "auth.Login",
        "kind": "FUNCTION",
        "file": "/path/to/repo/auth/login.go",
        "start_line": 14,
        "end_line": 22,
        "signature": "// Login authenticates a user and returns a session.\nfunc Login(user, pass string) (Session, error) {",
        "text": "...",
        "callers": [...],
        "callees": [...]
      }
    }
  ]
}
```

With `--explain` you get the per-channel decomposition:

```jsonc
{
  "structural": [ {"id": "fn:auth.Login", "score": 0.9,  "channel": "structural"}, ... ],
  "bm25":       [ {"id": "fn:auth.Login", "score": 4.21, "channel": "bm25",       "chunk": {...}}, ... ],
  "vector":     [ {"id": "fn:auth.Login", "score": 0.83, "channel": "vector",     "chunk": {...}}, ... ],
  "rrf":        [ {"id": "fn:auth.Login", "score": 0.049, "channel": "rrf"}, ... ]
}
```

The CLI ships the **stdlib-only `BagOfTokens` reference vector backend** —
good enough to demo the merge and run the benchmark, not competitive with a
real embedding model. To wire a real vector backend, embed yactt as a Go
library (see below). The CLI's argument parser is [Cobra](https://github.com/spf13/cobra);
the two direct deps yactt pulls in (`smacker/go-tree-sitter` + `spf13/cobra`)
and their two indirect deps (`mousetrap` + `pflag`) are listed in full in
the [README's receipts section](https://github.com/kellenff/yactt#whats-behind-the-badge-row).

## Embedding yactt as a library (Go)

The orchestrator is in `internal/hybrid`. Import it directly:

```go
import (
    "github.com/kellenff/yactt/internal/hybrid"
    "github.com/kellenff/yactt/internal/store"
)

repo, _, _ := store.Load("/path/to/repo")
defer repo.Close()

// Wire YOUR vector backend (OpenAI, Cohere, Ollama, Chroma, pgvector, ...).
// The interface is two methods; see internal/hybrid/vector.go.
myBackend := &myOpenAIBackend{APIKey: os.Getenv("OPENAI_API_KEY")}

hits, err := hybrid.Run(ctx, hybrid.Options{
    Repo:     repo,
    Query:    "where is the auth login",
    Limit:    10,
    Channels: hybrid.AllChannels(),
    Vector:   myBackend,
})
// hits is []hybrid.Hit — canonical yactt IDs, ready for your prompt builder.
```

The plug-in shape is small: implement two methods on
`hybrid.VectorBackend` and `hybrid.VectorIndex`:

```go
type VectorBackend interface {
    Index(ctx context.Context, chunks []chunker.Chunk) (VectorIndex, error)
}

type VectorIndex interface {
    TopK(ctx context.Context, query string, k int) ([]VectorHit, error)
}

type VectorHit struct {
    ChunkID string
    Score   float64 // higher = more similar (cosine reshaped to [0,1])
}
```

`hybrid.VectorBackend` is responsible for its own caching, batching, and
remote I/O — the orchestrator calls `Index` once per `Run` and `TopK` once.

## Plug-in shapes for common stacks

### pgvector (Go)

```go
type pgvectorBackend struct{ pool *pgxpool.Pool }

func (b *pgvectorBackend) Index(ctx context.Context, chunks []chunker.Chunk) (hybrid.VectorIndex, error) {
    // Embed each chunk with your model of choice (OpenAI, local sentence-
    // transformers, ...). INSERT rows with (chunk_id, embedding vector).
    // Cache the chunk_id list for TopK lookups.
    return &pgvectorIndex{pool: b.pool, chunkIDs: idsFrom(chunks)}, nil
}

func (b *pgvectorIndex) TopK(ctx context.Context, query string, k int) ([]hybrid.VectorHit, error) {
    emb := embed(query)
    rows, _ := b.pool.Query(ctx,
        `SELECT chunk_id, 1 - (embedding <=> $1) AS score
         FROM chunks ORDER BY embedding <=> $1 LIMIT $2`, emb, k)
    // ...
}
```

### Chroma (Go) / Qdrant / Weaviate

Same shape; swap the storage backend. The orchestrator's `VectorIndex` is
the only seam.

### LangChain (Python)

```python
from langchain.embeddings import OpenAIEmbeddings
from langchain.vectorstores import Chroma
from yactt import Hybrid, BagOfTokens  # not yet published; see examples/hybrid/

class LangChainVectorBackend:
    """Wrap a LangChain VectorStore as a yactt hybrid.VectorBackend."""
    def __init__(self, store: Chroma, embeddings: OpenAIEmbeddings):
        self.store, self.embeddings = store, embeddings

    def index(self, chunks):  # chunks: list[yactt.Chunk]
        texts = [c.text for c in chunks]
        metadatas = [{"yactt_id": c.id, "qualified_name": c.qualified_name} for c in chunks]
        ids = [c.id for c in chunks]
        self.store.add_texts(texts, metadatas=metadatas, ids=ids)
        return LangChainVectorIndex(self.store)

    def top_k(self, query, k):
        docs = self.store.similarity_search_with_score(query, k=k)
        return [VectorHit(chunk_id=d.metadata["yactt_id"], score=s) for d, s in docs]
```

A more complete sketch is in [`examples/hybrid/langchain.py`](../examples/hybrid/langchain.py).
Run it through your LangChain pipeline as the "yactt structural channel"
next to whatever BM25 + vector setup you already have.

### LlamaIndex (Python)

Same pattern, LlamaIndex's `VectorStore` API. Sketch in
[`examples/hybrid/llamaindex.py`](../examples/hybrid/llamaindex.py).

## How the merge works

For every distinct ID across the per-channel ranked lists, the orchestrator
sums `1 / (k + rank)` over the channels that surfaced the ID. The default
`k = 60` (Cormack et al. 2009) is the well-known RRF damping constant.

- **No score normalization.** RRF uses rank, not score. The per-channel
  scores in the output (`Score` field) are in each channel's natural scale
  (e.g. BM25 in raw `idf * tf / den`, vector in cosine). Use them for
  debug, not for cross-channel comparison.
- **Channels that don't surface an ID contribute 0.** A document only
  appears in the merged output if at least one channel ranked it. This is
  the canonical RRF variant; the alternative "missing rank = `len(channel)+1`"
  keeps documents alive with a tiny penalty but adds an arbitrary constant.
- **Top-K cap on the merged list.** Default 10. Each channel is asked for
  `Limit * Overscan` (default 4×) so the merge has rank depth.

## What the benchmark shows

[`tests/hybrid/eval_test.go`](../tests/hybrid/eval_test.go) holds the headline
gate from issue #37:

```
recall@1: hybrid=0.87 structural=0.30 bm25=0.83 vector=0.74 (n=23)
no-regression rate: vs structural=1.00 vs bm25=0.87 vs vector=0.96
```

- Hybrid's @1 recall is ≥ each single channel's @1 recall (0.87 > 0.83 > 0.74 > 0.30).
- For ≥70% of questions where a single channel got @1 right, hybrid also
  got @1 right — the success criterion from the issue.

Run it with:

```sh
go test -v -run TestHybrid_WinsOverSingles ./tests/hybrid/...
```

The recall set is hand-tagged against `tests/chunking/genfixture` (the
100-file synthetic repo from issue #34). It's stable across runs because
the genfixture is deterministic (`Seed=0x1AC1AC1A`). If the genfixture
ever changes, re-tag by running:

```sh
go test -v -run TestDumpIDs ./tests/hybrid/...   # developer-only; un-skip in the file
```

## Don't over-invest

yactt is the **structural channel**, not a RAG framework. The orchestrator
exists so the four other slot issues (#33 MCP, #34 chunker, #35 GraphRAG,
#36 packer) have a composition point. Plug yactt into your existing RAG
pipeline; don't fork it into a new framework.

For deeper multi-hop retrieval (the "what depends on X transitively?"
questions), yactt also exposes the call graph directly via the existing
`query_graph` MCP tool — see issue #35. For context assembly (packing
the top-K into a token-budgeted prompt), see issue #36.

## GraphRAG without the LLM step

yactt's structural channel isn't just a single-trip BM25/vector proxy.
Because yactt already holds the property graph for the parsed repo
(functions, methods, classes, files, packages + call edges), it can play
the role that Microsoft GraphRAG's "graph LLM" step plays for prose:
**expand a seed set into a subgraph and rank the results for packing.**

For code, the graph is *given* by the AST — no entity-extraction LLM
step is needed. The yactt-native pipeline:

```
vector / BM25 retriever ──► top-K chunk ids ──► seeds (canonical yactt ids)
                                                       │
                                                       ▼
                                     query_graph (multi-seed, follow=*)
                                                       │
                                                       ▼
                                    ranked subgraph (rows with score)
                                                       │
                                                       ▼
                                         packer (issue #36)
                                                       │
                                                       ▼
                                                      LLM
```

The graph-channel work happens entirely in-process against the in-memory
symbol index, so the expansion itself is fast — measured on the 100-file
`tests/chunking/genfixture` fixture, a 5-hop transitive-caller traversal
across the seeded chain returns ≤1000 ranked rows in low single-digit ms
wallclock (`go test ./internal/tool/... -bench BenchmarkQueryGraph`). The
500 ms budget in issue #35's success criterion is the BFS + scanner
ceiling for a 100K-LOC monorepo; the in-memory dispatch has ~100×
headroom on the smaller fixture.

### Worked example (CLI)

```bash
# 1. Vector retriever (any embedding-backed CLI). For the demo, we
#    use yactt's stdlib-only BagOfTokens via the hybrid orchestrator:
yactt hybrid --repo ./tests/fixtures/sample-go \
  --query "who calls auth Login" --limit 3 --explain > /tmp/hits.json

# 2. Extract seed node ids from the structural channel hits — these
#    are already stable fn:/meth: addresses.
SEEDS=$(jq -r '[.structural[].id] | join(",")' /tmp/hits.json)

# 3. Hand the seed set to query_graph. Pass weights when the vector
#    channel has similarity scores; otherwise query_graph defaults
#    to uniform 1.0 weights.
yactt --mcp-call query_graph --repo ./tests/fixtures/sample-go -- '
{
  "seeds":  ($s | split(",")),
  "follow": ["callers","callees","tests"],
  "depth":  3,
  "limit":  100
}'

# 4. Pipe the returned ranked rows into the packer (issue #36; not
#    shipped in this slice). When the packer ships:
# yactt pack --rows <query_graph.json> --question "..."
```

Inside yactt the pipeline is two method calls:

```go
// 1. Hybrid retrieval produces canonical yactt ids (chunker.Chunk.ID
//    == graph address).
hits, _ := hybrid.Run(ctx, hybrid.Options{
    Repo:     repo,
    Query:    "who calls auth Login",
    Limit:    5,
    Channels: hybrid.AllChannels(),
    Vector:   myOpenAIBackend,
})

// 2. Feed hits as seeds to query_graph. Pass RRF scores as weights
//    to bias the expansion toward what the retriever scored highly.
seeds   := make([]string, len(hits))
weights := make([]float64, len(hits))
for i, h := range hits {
    seeds[i]   = h.ID
    weights[i] = h.Score // clamp to [0, 1]
}

qgArgs, _ := json.Marshal(map[string]any{
    "seeds":   seeds,
    "weights": weights,
    "follow":  []string{"callers", "callees", "tests"},
    "depth":   3,
    "limit":   100,
})
out, _ := tool.QueryGraph(repo)(ctx, qgArgs)
graph  := out.(*tool.QueryGraphResult)

// graph.Rows is ranked: higher `score` = closer to a high-weight seed.
// graph.SeedScores maps each contributing seed to its max score —
// useful for explain / debugging.
// graph.Rows[i].TargetID is the canonical yactt id; feed it to a
// node_get / get_code_snippet call to materialise the body.
```

### yactt ↔ Microsoft GraphRAG ↔ AST

| Microsoft GraphRAG                  | yactt                                |
|-------------------------------------|--------------------------------------|
| text chunks → LLM entity extraction | AST → fn/meth/class file addresses   |
| LLM-derived entity graph            | persisted call-edge index            |
| community detection + summarisation | depth-bounded BFS + score ranking    |
| chunk → entity → graph → community  | chunk → seed → graph → pack → LLM    |

The LLM extraction step is unnecessary when the AST is the source of
truth: the symbol index already gives you named nodes with stable ids
and call-graph edges.

### Cross-links

- Issue #36 (packer) — TODO: this pipeline ends at the LLM step; the
  intermediate `rows → packed-prompt` transform is owned by the packer.
  Once #36 lands, the wired example above shrinks from two CLI invocations
  to one.
- `internal/tool/querygraph.go` — the engine itself. Multi-seed seeds,
  weights, and per-row score are all defined here. Pin `MaxSeeds=50`,
  `MaxVisited=5000`, `MaxRuntime=5s` so the cost caps are documented
  alongside the call.
- `internal/hybrid/hybrid.go` — for the upstream seed source. Hits already
  carry stable yactt ids.