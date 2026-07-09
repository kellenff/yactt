package hybrid

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"

	"github.com/kellenff/yactt/internal/chunker"
)

// rrfMerge is the core merge routine. For every distinct ID across
// the per-channel ranked lists, sum 1 / (k + rank_c(d)) over the
// channels that surfaced d; rank_c is 1-indexed in each channel,
// and a missing channel contributes rank = len(channel)+1 (i.e.
// it still adds a tiny term so the document isn't dropped).
//
// The formula is the 2009 Cormack/Clarke/Buettcher multi-list
// fusion recipe, with k = 60 by default. k=60 is the well-known
// damping constant that makes the top-3 ranks of any channel
// roughly equivalent.
//
// Each input list is assumed to be sorted by descending per-channel
// score (which is what every channel produces); we don't re-sort.
// Outputs are sorted by descending rrf score; ties are broken by
// the lowest-best-rank across channels (a document that ranked 1
// in any channel beats one that ranked 5 in every channel), then
// by ID for byte-determinism.
func rrfMerge(channels [][]Hit, k, limit int) []Hit {
	if k <= 0 {
		k = DefaultRRFK
	}

	type accum struct {
		rrf float64
		// last-seen hit (so the merge output carries the chunk
		// payload for bm25/vector hits, or the structural score).
		// For an ID that appears in multiple channels the LATER
		// channel's payload wins — which is arbitrary but
		// deterministic (channels slice order is fixed).
		hit Hit
	}
	merged := map[string]*accum{}

	for _, list := range channels {
		if len(list) == 0 {
			continue
		}
		for rank, h := range list {
			a, ok := merged[h.ID]
			if !ok {
				a = &accum{hit: h}
				merged[h.ID] = a
			}
			// 1-indexed rank; this is the standard RRF formula
			// from Cormack et al. 2009. Documents that don't
			// appear in this channel contribute 0 (they're
			// absent from `list`, so they never enter the loop).
			a.rrf += 1.0 / float64(k+rank+1)
		}
	}

	out := make([]Hit, 0, len(merged))
	for id, a := range merged {
		h := a.hit
		h.ID = id
		h.Score = a.rrf
		// Channel is "rrf" in the merged output so callers can
		// distinguish merged hits from per-channel hits in
		// Explain output. The original channel is preserved
		// via h.Chunk (nil for structural; populated for the
		// other two).
		h.Channel = "rrf"
		out = append(out, h)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].ID < out[j].ID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// buildChunks runs the chunker into an in-memory buffer so the
// orchestrator can fan out to BM25 + vector without changing the
// chunker's streaming wire shape. The chunker emits one Chunk per
// NDJSON line; we re-decode them here. We deliberately don't
// duplicate the chunker's per-symbol work in-process because that
// would re-parse the whole repo twice; one walk is enough.
//
// For repos where the chunker produces >~50MB of NDJSON, this is
// a memory hazard. The orchestrator's expected scale is "single
// project, hundreds of files, single-machine" — well within
// bytes.Buffer's headroom. A streaming refactor (io.Pipe +
// chunk-by-chunk BM25/vector scoring) is deferred to a v2 when
// someone reports it actually hurts.
func buildChunks(ctx context.Context, opts Options) ([]chunker.Chunk, error) {
	var buf bytes.Buffer
	if _, err := chunker.Run(ctx, opts.Repo, opts.ChunkerOpts, &buf); err != nil {
		return nil, err
	}
	out := []chunker.Chunk{}
	dec := json.NewDecoder(&buf)
	for dec.More() {
		var c chunker.Chunk
		if err := dec.Decode(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}