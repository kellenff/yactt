// Package hybrid is the reference orchestrator for issue #37: it
// fans a query out to (a) yactt's structural index, (b) BM25 over
// chunker-produced chunks, (c) a pluggable vector backend, and merges
// the three ranked lists via reciprocal rank fusion (RRF).
//
// The orchestrator is glue, not a framework. It exists so external
// pipelines (LangChain, LlamaIndex, custom) can compose yactt's
// structural channel with their existing BM25 + vector backends
// without re-implementing the merge. The shipped reference vector
// backend is a stdlib-only bag-of-tokens scorer — good enough to
// demonstrate the merge and run the benchmark, not competitive with
// a real embedding model. Production users are expected to plug in
// their own VectorBackend; see docs/hybrid-retrieval.md.
//
// Non-goals (from the issue):
//   - No vector store, no BM25 index, no reranker shipped in-tree.
//   - No multi-repo. Single repo at a time.
//   - No LLM-in-the-loop. RAG is the orchestrator's user.
//   - No new transitive deps. Stdlib only for the in-tree code.
package hybrid

import (
	"context"
	"fmt"
	"sort"

	"github.com/kellenff/yactt/internal/chunker"
	"github.com/kellenff/yactt/internal/store"
)

// Channel names. Stable strings; users compare against these on the
// wire (the JSON output uses them as values).
const (
	ChannelStructural = "structural"
	ChannelBM25       = "bm25"
	ChannelVector     = "vector"
)

// Channels toggles individual channels. The zero value enables
// NONE — callers must opt in. AllChannels() is the production
// default ("use every channel"). Explicit per-channel booleans let
// callers benchmark single-channel vs hybrid without re-typing
// the channel list at the call site.
type Channels struct {
	Structural bool
	BM25       bool
	Vector     bool
}

// AllChannels returns the default channel set (all three on).
func AllChannels() Channels { return Channels{Structural: true, BM25: true, Vector: true} }

// Hit is a single ranked match surfaced by a channel.
//
// Channel is one of "structural", "bm25", "vector". Score is the
// per-channel score in that channel's natural scale — the
// orchestrator does NOT normalize across channels (RRF uses rank,
// not score; Score is for debug + per-channel explain output).
//
// Chunk is nil for structural hits (those don't go through the
// chunker) and populated for bm25 / vector hits (they scored a
// specific chunker.Chunk).
type Hit struct {
	ID      string         `json:"id"`
	Score   float64        `json:"score"`
	Channel string         `json:"channel"`
	Chunk   *chunker.Chunk `json:"chunk,omitempty"`
}

// Options configures one Run call.
type Options struct {
	// Repo is the repository under search. Required.
	Repo *store.Repo

	// Query is the natural-language query. Required.
	Query string

	// Limit is the final top-K returned to the caller. Default 10.
	// Each channel is asked for Limit*Overscan hits so the merge
	// has rank depth to work with.
	Limit int

	// Overscan is the per-channel overscan factor relative to Limit.
	// Default 4. The merged RRF list is then truncated to Limit.
	Overscan int

	// Channels lets the caller disable individual channels. Zero
	// value = all three on.
	Channels Channels

	// Vector is the vector backend for the "vector" channel. Required
	// when Channels.Vector is true. The orchestrator calls
	// Vector.Index once per Run to build the chunk index, then
	// queries it with the user's Query string.
	Vector VectorBackend

	// ChunkerOpts tunes the BM25 + vector corpus. Zero value = the
	// chunker's own defaults (PolicyFunction, no test files).
	ChunkerOpts chunker.Options

	// RRFK is the RRF damping constant. Default 60 (Cormack 2009).
	// Higher = ranks matter less; lower = top hit dominates more.
	RRFK int
}

// DefaultRRFK is the well-known RRF damping constant from Cormack
// et al. 2009. Exposed so callers can reason about the default.
const DefaultRRFK = 60

// Run fans the query out to the enabled channels and returns the
// merged, RRF-ranked top-K hits.
//
// Errors:
//   - store.Repo nil → "hybrid: nil repo"
//   - empty query → "hybrid: empty query"
//   - Channels.Vector=true and Options.Vector nil → "hybrid: nil VectorBackend"
//
// Per-channel failures do NOT short-circuit the merge: a failing
// vector backend degrades to structural + BM25 and returns the
// merged top-K from those two. The first channel to fail is
// surfaced via the returned error when no hits can be produced at
// all; otherwise failures are silenced so partial output is more
// useful than no output for an agent.
func Run(ctx context.Context, opts Options) ([]Hit, error) {
	if opts.Repo == nil {
		return nil, fmt.Errorf("hybrid: nil repo")
	}
	if opts.Query == "" {
		return nil, fmt.Errorf("hybrid: empty query")
	}
	if opts.Limit <= 0 {
		opts.Limit = 10
	}
	if opts.Overscan <= 0 {
		opts.Overscan = 4
	}
	if opts.RRFK <= 0 {
		opts.RRFK = DefaultRRFK
	}
	if !opts.Channels.Structural && !opts.Channels.BM25 && !opts.Channels.Vector {
		return nil, fmt.Errorf("hybrid: all channels disabled")
	}
	if opts.Channels.Vector && opts.Vector == nil {
		return nil, fmt.Errorf("hybrid: nil VectorBackend")
	}

	perChannel := opts.Limit * opts.Overscan

	// Structural channel: cheap (in-memory symbol index). Always run
	// when enabled; no I/O.
	var structHits []Hit
	if opts.Channels.Structural {
		structHits = structuralChannel(opts, perChannel)
	}

	// BM25 + vector share the chunker output (one walk over the
	// repo). Build it once, then fan out.
	var chunks []chunker.Chunk
	if opts.Channels.BM25 || opts.Channels.Vector {
		var err error
		chunks, err = buildChunks(ctx, opts)
		if err != nil {
			return nil, fmt.Errorf("hybrid: chunker: %w", err)
		}
	}

	// BM25 channel: in-memory, stdlib-only.
	var bm25Hits []Hit
	if opts.Channels.BM25 {
		bm25Hits = bm25Channel(opts.Query, chunks, perChannel)
	}

	// Vector channel: pluggable. Build the index once, query once.
	// A failing backend leaves bm25Hits + structHits intact; we
	// only return the error when no channel produced any hits.
	var vecHits []Hit
	if opts.Channels.Vector {
		v, err := opts.Vector.Index(ctx, chunks)
		if err != nil {
			// Demote to a warning; surface only if no hits remain.
			if len(structHits) == 0 && len(bm25Hits) == 0 {
				return nil, fmt.Errorf("hybrid: vector index: %w", err)
			}
		} else {
			raw, err := v.TopK(ctx, opts.Query, perChannel)
			if err != nil {
				if len(structHits) == 0 && len(bm25Hits) == 0 {
					return nil, fmt.Errorf("hybrid: vector query: %w", err)
				}
			} else {
				vecHits = vectorHits(raw, chunks)
			}
		}
	}

	merged := rrfMerge([][]Hit{structHits, bm25Hits, vecHits}, opts.RRFK, opts.Limit)

	// Stable secondary sort: by ID, so the output is byte-deterministic
	// across runs at the same rank. Without this, map-iteration
	// order in the merge can shuffle equal-rank hits.
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].Score != merged[j].Score {
			return merged[i].Score > merged[j].Score
		}
		return merged[i].ID < merged[j].ID
	})
	if len(merged) > opts.Limit {
		merged = merged[:opts.Limit]
	}
	return merged, nil
}

// Explain is the per-channel decomposition used by `yactt hybrid
// --explain`. The returned map has one entry per channel name with
// the channel's own top-K (NOT merged); the merged list is in the
// final entry under key "rrf". Use this to see WHY hybrid wins
// (or loses) on a given query.
func Explain(ctx context.Context, opts Options) (map[string][]Hit, error) {
	if opts.Repo == nil {
		return nil, fmt.Errorf("hybrid: nil repo")
	}
	if opts.Query == "" {
		return nil, fmt.Errorf("hybrid: empty query")
	}
	if opts.Limit <= 0 {
		opts.Limit = 10
	}
	if opts.Overscan <= 0 {
		opts.Overscan = 4
	}
	if opts.RRFK <= 0 {
		opts.RRFK = DefaultRRFK
	}
	if opts.Channels.Vector && opts.Vector == nil {
		return nil, fmt.Errorf("hybrid: nil VectorBackend")
	}

	perChannel := opts.Limit * opts.Overscan
	out := map[string][]Hit{}

	if opts.Channels.Structural {
		out[ChannelStructural] = structuralChannel(opts, perChannel)
	}
	var chunks []chunker.Chunk
	if opts.Channels.BM25 || opts.Channels.Vector {
		var err error
		chunks, err = buildChunks(ctx, opts)
		if err != nil {
			return nil, fmt.Errorf("hybrid: chunker: %w", err)
		}
	}
	if opts.Channels.BM25 {
		out[ChannelBM25] = bm25Channel(opts.Query, chunks, perChannel)
	}
	if opts.Channels.Vector {
		if opts.Vector == nil {
			// Already validated above; defensive.
			return out, nil
		}
		v, err := opts.Vector.Index(ctx, chunks)
		if err != nil {
			out[ChannelVector] = nil
		} else {
			raw, err := v.TopK(ctx, opts.Query, perChannel)
			if err != nil {
				out[ChannelVector] = nil
			} else {
				out[ChannelVector] = vectorHits(raw, chunks)
			}
		}
	}
	merged := rrfMerge(
		[][]Hit{out[ChannelStructural], out[ChannelBM25], out[ChannelVector]},
		opts.RRFK, opts.Limit,
	)
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].Score != merged[j].Score {
			return merged[i].Score > merged[j].Score
		}
		return merged[i].ID < merged[j].ID
	})
	if len(merged) > opts.Limit {
		merged = merged[:opts.Limit]
	}
	out["rrf"] = merged
	return out, nil
}