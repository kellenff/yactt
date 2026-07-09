"""LangChain adapter for yactt's hybrid.VectorBackend interface.

Reference sketch, not a runnable end-to-end test. yactt doesn't publish
a Python binding yet (separate effort); this file documents the
plug-in shape so a LangChain user can write their own adapter.

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

In Python, the equivalent shape is two methods on a class. The Go side
calls Index once per query (passing the chunker-produced chunk corpus)
and TopK once with the user's query string. The Python wrapper is
responsible for its own caching: the LangChain VectorStore has its own
index, so Index becomes a no-op or a batch upsert.
"""

from dataclasses import dataclass
from typing import List

from langchain.embeddings.base import Embeddings
from langchain.vectorstores.base import VectorStore


@dataclass
class YacttHit:
    """Mirror of Go's hybrid.VectorHit."""
    chunk_id: str
    score: float


class LangChainYacttVectorBackend:
    """Wrap a LangChain VectorStore as a yactt hybrid.VectorBackend.

    Usage:
        backend = LangChainYacttVectorBackend(chroma_store, openai_embeddings)
        index = backend.index(chunks)            # chunks: List[yactt.Chunk]
        hits = index.top_k("where is auth login", k=10)

    Notes:
        - Index() upserts the chunk texts into the LangChain store.
          If the store is already populated (e.g. you ran the
          chunker once and indexed earlier), skip this call.
        - The chunk's yactt ID is stored in the LangChain metadata
          under "yactt_id" so TopK can recover it.
        - LangChain returns (doc, score) tuples; some stores return
          distance (lower is better) instead of similarity. Reshape
          here if needed (cosine distance -> similarity: 1 - dist).
    """

    def __init__(self, store: VectorStore, embeddings: Embeddings):
        self.store = store
        self.embeddings = embeddings

    def index(self, chunks):
        texts = [c.text for c in chunks]
        metadatas = [
            {
                "yactt_id": c.id,
                "qualified_name": c.qualified_name,
                "kind": c.kind,
                "language": c.language,
                "file": c.file,
                "start_line": c.start_line,
                "end_line": c.end_line,
            }
            for c in chunks
        ]
        ids = [c.id for c in chunks]
        self.store.add_texts(texts, metadatas=metadatas, ids=ids)
        return _LangChainYacttVectorIndex(self.store)


class _LangChainYacttVectorIndex:
    """The handle returned by LangChainYacttVectorBackend.index()."""

    def __init__(self, store: VectorStore):
        self.store = store

    def top_k(self, query: str, k: int) -> List[YacttHit]:
        # Chroma's similarity_search_with_score returns (Document, score)
        # where score is distance (lower is better). Reshape here.
        # Other stores differ; adapt per backend.
        results = self.store.similarity_search_with_score(query, k=k)
        return [
            YacttHit(chunk_id=doc.metadata["yactt_id"], score=1.0 - dist)
            for doc, dist in results
        ]


# ---------------------------------------------------------------------------
# End-to-end call site: yactt structural + LangChain BM25 + LangChain vector
# ---------------------------------------------------------------------------
#
# Pseudocode (yactt Python binding doesn't exist yet):
#
#     from yactt import Hybrid, Channels, Chunker
#     from langchain.retrievers import BM25Retriever
#     from langchain.vectorstores import Chroma
#     from langchain.embeddings import OpenAIEmbeddings
#
#     # 1. Chunk the repo via yactt's chunker (AST-bounded, not windowed).
#     chunker = Chunker(repo_path="/path/to/repo", policy="function")
#     chunks = chunker.run()
#
#     # 2. Build BM25 retriever over the same chunks (no separate index).
#     bm25 = BM25Retriever.from_texts(
#         texts=[c.text for c in chunks],
#         metadatas=[{"yactt_id": c.id} for c in chunks],
#     )
#
#     # 3. Wrap the LangChain vector store as a yactt hybrid backend.
#     chroma = Chroma(collection_name="yactt", embedding_function=OpenAIEmbeddings())
#     vector = LangChainYacttVectorBackend(chroma, OpenAIEmbeddings()).index(chunks)
#
#     # 4. Run the orchestrator (or build your own RRF merge).
#     results = hybrid.run(
#         repo_path="/path/to/repo",
#         query="where is auth login",
#         structural=True,           # yactt's symbol index
#         bm25=bm25,                 # LangChain BM25
#         vector=vector,             # LangChain Chroma
#         limit=10,
#     )
#     for hit in results:
#         print(hit.id, hit.score, hit.chunk.qualified_name)