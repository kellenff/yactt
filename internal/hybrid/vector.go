package hybrid

import (
	"context"

	"github.com/kellenff/yactt/internal/chunker"
)

// VectorBackend produces a vector index over a chunk corpus and
// queries it. Production users wrap their existing vector store
// (pgvector, Chroma, Qdrant, OpenAI, Cohere, a local model, ...);
// the in-tree reference is BagOfTokens, a stdlib-only stand-in
// that's good enough for the benchmark but not competitive with
// a real embedding model.
//
// Implementations are responsible for their own caching, batching,
// and remote I/O. The orchestrator only calls these two methods.
type VectorBackend interface {
	// Index builds an index over the supplied chunks. The orchestrator
	// calls Index once per Run; implementations cache embeddings
	// across calls if they want to.
	Index(ctx context.Context, chunks []chunker.Chunk) (VectorIndex, error)
}

// VectorIndex scores a query against a previously-built chunk index.
type VectorIndex interface {
	// TopK returns the top-K (chunk-id, score) pairs by descending
	// similarity. Implementations choose their own distance metric
	// (typically cosine) and score range; the orchestrator treats
	// the score as a higher-is-better scalar.
	TopK(ctx context.Context, query string, k int) ([]VectorHit, error)
}

// VectorHit is one match from a VectorIndex.
type VectorHit struct {
	ChunkID string  // matches chunker.Chunk.ID
	Score   float64 // higher = more similar (cosine in [-1,1] is reshaped to [0,1] by the backend)
}

// vectorHits reshapes a VectorIndex result into the orchestrator's
// Hit type, attaching the chunk payload from the corpus so callers
// don't have to look it up by ID. Chunks that vanished from the
// corpus between Index and TopK (extremely rare; would only happen
// if the user mutated the slice between calls) are dropped.
func vectorHits(raw []VectorHit, chunks []chunker.Chunk) []Hit {
	if len(raw) == 0 || len(chunks) == 0 {
		return nil
	}
	byID := make(map[string]chunker.Chunk, len(chunks))
	for _, c := range chunks {
		byID[c.ID] = c
	}
	out := make([]Hit, 0, len(raw))
	for _, h := range raw {
		c, ok := byID[h.ChunkID]
		if !ok {
			continue
		}
		out = append(out, Hit{
			ID:      h.ChunkID,
			Score:   h.Score,
			Channel: ChannelVector,
			Chunk:   &c,
		})
	}
	return out
}