"""LlamaIndex adapter for yactt's hybrid.VectorBackend interface.

Reference sketch, not a runnable end-to-end test. yactt doesn't publish
a Python binding yet (separate effort); this file documents the
plug-in shape so a LlamaIndex user can write their own adapter.

The orchestrator's Go API is in internal/hybrid/vector.go:

    type VectorBackend interface {
        Index(ctx context.Context, chunks []chunker.Chunk) (VectorIndex, error)
    }
    type VectorIndex interface {
        TopK(ctx context.Context, query string, k int) ([]VectorHit, error)
    }
    type VectorHit struct {
        ChunkID string
        Score   float64  // higher = more similar
    }

In LlamaIndex land, the analog of LangChain's VectorStore is the
`VectorStore` abstract base class. The wiring is almost identical to
the LangChain adapter; the differences are:

  - LlamaIndex uses `VectorStoreQuery` / `VectorStoreQueryResult` for
    queries; the adapter translates.
  - LlamaIndex's `node_id` plays the same role as LangChain's `ids`
    argument; we stash the yactt chunk ID in metadata and recover it
    from `result.nodes[i].node_id` (or `extra_info`).
"""

from dataclasses import dataclass
from typing import List

from llama_index.vector_stores.types import (
    VectorStore,
    VectorStoreQuery,
    VectorStoreQueryResult,
)


@dataclass
class YacttHit:
    """Mirror of Go's hybrid.VectorHit."""
    chunk_id: str
    score: float


class LlamaIndexYacttVectorBackend:
    """Wrap a LlamaIndex VectorStore as a yactt hybrid.VectorBackend.

    Usage:
        backend = LlamaIndexYacttVectorBackend(chroma_store)
        index = backend.index(chunks)            # chunks: List[yactt.Chunk]
        hits = index.top_k("where is auth login", k=10)
    """

    def __init__(self, store: VectorStore):
        self.store = store

    def index(self, chunks):
        # Translate yactt chunks -> LlamaIndex nodes. Each yactt chunk
        # becomes one TextNode with the yactt ID stashed in metadata.
        from llama_index.schema import TextNode
        nodes = [
            TextNode(
                text=c.text,
                id_=c.id,
                metadata={
                    "yactt_id": c.id,
                    "qualified_name": c.qualified_name,
                    "kind": c.kind,
                    "language": c.language,
                    "file": c.file,
                    "start_line": c.start_line,
                    "end_line": c.end_line,
                },
            )
            for c in chunks
        ]
        self.store.add(nodes)
        return _LlamaIndexYacttVectorIndex(self.store)


class _LlamaIndexYacttVectorIndex:
    """The handle returned by LlamaIndexYacttVectorBackend.index()."""

    def __init__(self, store: VectorStore):
        self.store = store

    def top_k(self, query: str, k: int) -> List[YacttHit]:
        # Embedding happens upstream (the retriever / query engine
        # layer); at this layer we ask the store to score an already-
        # embedded query vector. The orchestrator's Go side doesn't
        # expose embedding — it pushes that responsibility onto the
        # VectorBackend so each backend can use its own model.
        #
        # For the sketch: assume the caller passes a query_str and
        # the adapter embeds it with the store's default embed model.
        # In production you'd separate embedding from query.
        from llama_index.embeddings import resolve_embed_model
        embed_model = resolve_embed_model("default")
        query_embedding = embed_model.get_text_embedding(query)
        result: VectorStoreQueryResult = self.store.query(
            VectorStoreQuery(query_str=query, query_embedding=query_embedding, similarity_top_k=k)
        )
        return [
            YacttHit(chunk_id=node.node_id, score=score)
            for node, score in zip(result.nodes, result.similarities or [])
        ]


# ---------------------------------------------------------------------------
# End-to-end call site: yactt structural + LlamaIndex BM25 + LlamaIndex vector
# ---------------------------------------------------------------------------
#
# Pseudocode (yactt Python binding doesn't exist yet):
#
#     from yactt import Hybrid, Chunker
#     from llama_index import (
#         VectorStoreIndex,
#         StorageContext,
#     )
#     from llama_index.vector_stores import ChromaVectorStore
#     from llama_index.retrievers import BM25Retriever
#     import chromadb
#
#     # 1. Chunk the repo via yactt's chunker (AST-bounded, not windowed).
#     chunks = Chunker(repo_path="/path/to/repo", policy="function").run()
#
#     # 2. BM25 retriever over the same chunks (no separate index).
#     bm25 = BM25Retriever.from_documents(
#         [Document(text=c.text, metadata={"yactt_id": c.id}) for c in chunks],
#     )
#
#     # 3. Wrap LlamaIndex's ChromaVectorStore as a yactt hybrid backend.
#     db = chromadb.PersistentClient(path="./chroma")
#     chroma_store = ChromaVectorStore(chroma_collection=db.get_or_create_collection("yactt"))
#     vector = LlamaIndexYacttVectorBackend(chroma_store).index(chunks)
#
#     # 4. Run the orchestrator (or build your own RRF merge).
#     results = hybrid.run(
#         repo_path="/path/to/repo",
#         query="where is auth login",
#         structural=True,           # yactt's symbol index
#         bm25=bm25,                 # LlamaIndex BM25
#         vector=vector,             # LlamaIndex Chroma
#         limit=10,
#     )
#     for hit in results:
#         print(hit.id, hit.score, hit.chunk.qualified_name)