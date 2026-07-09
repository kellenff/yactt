# examples/hybrid — plug-in shapes for external RAG stacks

The `internal/hybrid` package is the orchestrator's Go API. The Python
sketches in this directory show the same plug-in shape for the two most
common external stacks: LangChain and LlamaIndex. They are **reference
examples**, not runnable end-to-end — yactt doesn't publish a Python
binding yet (that's a separate effort; tracked in the issue tracker).

## What's here

- `langchain.py` — wrap a LangChain `VectorStore` as the yactt hybrid
  vector backend.
- `llamaindex.py` — same for LlamaIndex's `VectorStore`.

## How to read these

Each file shows:

1. The shape of the yactt `VectorBackend` interface (mirroring
   `internal/hybrid/vector.go`).
2. A concrete `VectorIndex` wrapper around the third-party store.
3. The call site: instantiate the orchestrator with yactt's structural
   channel + your BM25 + your vector backend, run a query.

The examples assume you're running yactt as a sidecar (in-process Go
library or MCP server) and orchestrating the three channels from
Python. The same shape works against the CLI: parse `yactt hybrid
--explain` JSON and merge the per-channel rankings yourself.

## When to skip these

If you're already using a managed RAG framework that has its own
multi-channel merge (LangChain's `EnsembleRetriever`, LlamaIndex's
`QueryFusionRetriever`), use that — yactt's `search` tool is the
structural channel; point your BM25 + vector at the same source files
and let the framework handle the merge. yactt's reference orchestrator
is for users who want a thin, auditable seam.