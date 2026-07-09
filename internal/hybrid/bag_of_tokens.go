package hybrid

import (
	"context"
	"hash/fnv"
	"math"
	"sort"

	"github.com/kellenff/yactt/internal/chunker"
)

// BagOfTokens is the stdlib-only reference VectorBackend. It hashes
// each token into a fixed-dim unit vector with signed hashing
// (hashing-trick "signed" variant: positive bit → +1, negative bit
// → -1), L2-normalizes, and scores cosine. No deps; no model
// download; no network.
//
// This is NOT competitive with a real embedding model — it's the
// reference backend that lets the orchestrator run end-to-end with
// zero external dependencies, and that powers the benchmark gate
// (no Ollama needed, same pattern as tests/chunking). Production
// users are expected to swap in their own backend; the
// VectorBackend interface is the seam.
//
// The signature is "term presence, magnitude-bounded". It works
// surprisingly well on narrowly-scoped chunks (one function body)
// because term overlap is already a strong signal there; it falls
// apart on paraphrased queries because hash collisions are
// unrelated to semantics. That's the right tradeoff for a
// reference: simple, predictable, reproducible.
type BagOfTokens struct {
	// Dim is the hash-vector width. Higher = lower collision rate
	// but more memory. Default 1024. Power of two for the bitmask.
	Dim int
}

// NewBagOfTokens returns the reference backend with the default
// dimension. Use a literal struct only when you need to tune Dim.
func NewBagOfTokens() *BagOfTokens { return &BagOfTokens{Dim: 1024} }

// Index implements VectorBackend.
func (b *BagOfTokens) Index(ctx context.Context, chunks []chunker.Chunk) (VectorIndex, error) {
	if b.Dim <= 0 {
		b.Dim = 1024
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	idx := &bagIndex{
		dim:    b.Dim,
		chunks: make([]bagChunk, 0, len(chunks)),
	}
	for _, c := range chunks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		vec := idx.encode(c.Text)
		idx.chunks = append(idx.chunks, bagChunk{id: c.ID, vec: vec})
	}
	return idx, nil
}

// bagIndex is the indexed form: one vector per chunk, cached in
// memory. The orchestrator calls TopK once per Run.
type bagIndex struct {
	dim    int
	chunks []bagChunk
}

type bagChunk struct {
	id  string
	vec []float64
}

// encode turns a text into a fixed-dim signed-hash vector.
// Each token contributes +1 or -1 to a small number of positions
// (we use 4 hashes per token, the common practice for
// SimHash-style embeddings). Then L2-normalize so cosine
// reduces to dot product.
func (idx *bagIndex) encode(text string) []float64 {
	vec := make([]float64, idx.dim)
	toks := tokenize(text)
	if len(toks) == 0 {
		return vec
	}
	const hashesPerToken = 4
	for _, t := range toks {
		// Each of the four hashes uses a different salt prefix so
		// the same token hits four different positions. Without
		// the salts, a token would always hit the same position
		// regardless of which hash function "wins", which would
		// collapse the embedding's effective dimension to
		// ~len(toks).
		for i := 0; i < hashesPerToken; i++ {
			h := fnv.New64a()
			_, _ = h.Write([]byte{byte(i)})
			_, _ = h.Write([]byte(t))
			sum := h.Sum64()
			pos := int(sum & uint64(idx.dim-1))
			sign := 1.0
			if (sum>>32)&1 == 0 {
				sign = -1.0
			}
			vec[pos] += sign
		}
	}
	// L2 normalize.
	var n2 float64
	for _, v := range vec {
		n2 += v * v
	}
	if n2 == 0 {
		return vec
	}
	inv := 1.0 / math.Sqrt(n2)
	for i := range vec {
		vec[i] *= inv
	}
	return vec
}

// TopK implements VectorIndex.
func (idx *bagIndex) TopK(ctx context.Context, query string, k int) ([]VectorHit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if k <= 0 || len(idx.chunks) == 0 {
		return nil, nil
	}
	q := idx.encode(query)
	if q == nil {
		return nil, nil
	}
	type scored struct {
		id    string
		score float64
	}
	out := make([]scored, 0, len(idx.chunks))
	for _, c := range idx.chunks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Cosine = dot product when both vectors are unit-norm.
		var s float64
		for i, v := range c.vec {
			s += v * q[i]
		}
		if s > 0 {
			out = append(out, scored{id: c.id, score: s})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].id < out[j].id
	})
	if len(out) > k {
		out = out[:k]
	}
	hits := make([]VectorHit, 0, len(out))
	for _, s := range out {
		hits = append(hits, VectorHit{ChunkID: s.id, Score: s.score})
	}
	return hits, nil
}