package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/id"
	"github.com/kellenff/yactt/internal/parser"
	"github.com/kellenff/yactt/internal/store"
)

// Cost caps for query_graph. Mirrors the per-tool limits used by node_edges
// (limit 50), find_symbol (20), and get_architecture (gated Tarjan).
//
// The visited cap protects against an accidentally huge frontier; the
// wallclock cap (via context.WithTimeout) protects against a single scanner
// hanging on a pathological input. The seed cap (NEW) bounds the input shape
// so a multi-seed run can't OOM a single-seed run — Visited is shared
// across seeds (one FIFO counter), so 50 seeds at depth 5 with a 5000 cap
// leaves room for ~100 visited nodes per seed in the worst case.
const (
	queryGraphDefaultDepth = 2
	queryGraphMaxDepth     = 5
	queryGraphDefaultLimit = 100
	queryGraphMaxLimit     = 1000
	queryGraphMaxSeeds     = 50
	queryGraphMaxVisited   = 5000
	queryGraphMaxRuntime   = 5 * time.Second
)

// validFollowKinds is the set of edge kinds query_graph can walk. Mirrors
// the five kinds node_edges surfaces.
var validFollowKinds = map[string]struct{}{
	"callers":   {},
	"callees":   {},
	"tests":     {},
	"imports":   {},
	"overrides": {},
}

// QueryGraphArgs is the typed input for query_graph.
//
// Two entry shapes are accepted (mutually exclusive, expressed as `anyOf`
// in QueryGraphSchema):
//
//   - Single seed:  {"from": "<id>", "follow": [...], ...}
//   - Multi-seed:   {"seeds": ["<id>", "<id>", ...], "follow": [...],
//                    "weights": [w0, w1, ...], ...}
//
// "from" matches the original issue #9 contract. "seeds" + "weights"
// adds the GraphRAG-shaped seed-set input (issue #35) where the seed
// list comes from a vector retriever and the weights carry the
// per-seed similarity score (or 1.0 for uniform).
type QueryGraphArgs struct {
	From    string    `json:"from"`
	Seeds   []string  `json:"seeds"`
	Weights []float64 `json:"weights"`
	Follow  []string  `json:"follow"`
	Depth   int       `json:"depth"`
	Kind    string    `json:"kind"`
	Exclude string    `json:"exclude"`
	Limit   int       `json:"limit"`
}

// QueryGraphRow is one row in the result. Mirrors NodeEdgesResult so clients
// already rendering edges can render rows with no new code; adds depth,
// via (the follow kind that produced this row), and score (the graph-
// relevance ranking signal — higher means closer to a seed with higher
// weight) for traversal + ranking context.
type QueryGraphRow struct {
	EdgeKind      domain.EdgeKind   `json:"edgeKind"`
	TargetID      string            `json:"targetId"`
	TargetKind    domain.NodeKind   `json:"targetKind,omitempty"`
	TargetSummary string            `json:"targetSummary,omitempty"`
	Location      domain.Location   `json:"location"`
	Confidence    float64           `json:"confidence"`
	Provenance    domain.Provenance `json:"provenance"`
	Depth         int               `json:"depth"`
	Via           []string          `json:"via"`
	Score         float64           `json:"score"`
}

// QueryGraphResult is the structuredContent envelope for query_graph.
//
// Count == len(Rows). Truncated is set when the answer was clipped by
// limit, visited cap, or wallclock. Visited is the count of unique nodes
// handed to a scanner (shared across seeds in the multi-seed path).
//
// Single-seed path: From echoes the input; Seeds/Weights/SeedScores are
// omitted (From carries the seed).
//
// Multi-seed path: Seeds/Weights echo the normalised input; SeedScores
// carries per-seed max Score contributed (useful for debug/explain —
// "which seeds produced the high-ranking hits").
type QueryGraphResult struct {
	Rows       []QueryGraphRow    `json:"rows"`
	Count      int                `json:"count"`
	Truncated  bool               `json:"truncated"`
	Visited    int                `json:"visited"`
	From       string             `json:"from"`
	Seeds      []string           `json:"seeds,omitempty"`
	Weights    []float64          `json:"weights,omitempty"`
	SeedScores map[string]float64 `json:"seedScores,omitempty"`
	Followed   []string           `json:"followed"`
	Provenance domain.Provenance  `json:"provenance"`
}

// QueryGraphSchema is the JSON Schema for query_graph. The `anyOf` block
// expresses the mutual exclusion: exactly one of (from) or (seeds) must
// be provided alongside the always-required follow list.
var QueryGraphSchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "anyOf": [
    { "required": ["from", "follow"] },
    { "required": ["seeds", "follow"] }
  ],
  "properties": {
    "from":    { "type": "string" },
    "seeds":   {
      "type": "array",
      "minItems": 1,
      "maxItems": 50,
      "items": {
        "type": "string",
        "description": "Stable node id (fn:/meth:/class:/pkg:/file:). Pre-resolve via node_get or hybrid results before passing."
      }
    },
    "weights": {
      "type": "array",
      "minItems": 1,
      "items": { "type": "number", "minimum": 0, "maximum": 1 },
      "description": "Optional per-seed scalar weights (must align 1:1 with seeds). Defaults to uniform 1.0. Use to bias hits the vector retriever scored highly."
    },
    "follow": {
      "type": "array",
      "minItems": 1,
      "items": { "enum": ["callers", "callees", "tests", "imports", "overrides"] }
    },
    "depth":    { "type": "integer", "minimum": 1, "maximum": 5, "default": 2 },
    "kind":     {
      "type": "string",
      "enum": ["FUNCTION", "METHOD", "CLASS", "MODULE", "FILE", "PACKAGE", "REPO"],
      "description": "Filter rows by target NodeKind. Omit to return all kinds. Case-insensitive — the canonical names are FUNCTION/METHOD/CLASS/MODULE/FILE/PACKAGE/REPO."
    },
    "exclude":  { "enum": ["tests"] },
    "limit":    { "type": "integer", "minimum": 1, "maximum": 1000, "default": 100 }
  },
  "additionalProperties": false
}`)

// QueryGraphOutputSchema declares the structuredContent shape. Top-level
// type:"object" is the MCP contract (server.go:166-169).
var QueryGraphOutputSchema = json.RawMessage(`{
  "type": "object",
  "required": ["rows", "count", "truncated", "visited", "from", "followed", "provenance"],
  "properties": {
    "rows": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["edgeKind", "targetId", "location", "confidence", "provenance", "depth", "via", "score"],
        "properties": {
          "edgeKind":      { "type": "string" },
          "targetId":      { "type": "string" },
          "targetKind":    { "type": "string" },
          "targetSummary": { "type": "string" },
          "location":      { "type": "object" },
          "confidence":    { "type": "number" },
          "provenance":    { "type": "object" },
          "depth":         { "type": "integer" },
          "via":           { "type": "array", "items": { "type": "string" } },
          "score":         { "type": "number", "description": "Graph-relevance ranking signal. Higher = closer to a higher-weighted seed." }
        }
      }
    },
    "count":      { "type": "integer" },
    "truncated":  { "type": "boolean" },
    "visited":    { "type": "integer" },
    "from":       { "type": "string" },
    "seeds":      { "type": "array", "items": { "type": "string" } },
    "weights":    { "type": "array", "items": { "type": "number" } },
    "seedScores": {
      "type": "object",
      "additionalProperties": { "type": "number" },
      "description": "Per-seed max Score contributed across all emitted rows. Useful for explain / debug."
    },
    "followed":   { "type": "array", "items": { "type": "string" } },
    "provenance": { "type": "object" }
  },
  "additionalProperties": false
}`)

// seedEntry pairs a seed id with its (file, sym) location and weight. The
// single-seed path builds a 1-element slice of these; multi-seed uses the
// full input. Each seedEntry is the head of one BFS tree within the shared
// traversal — the seed id is what we use to attribute scores via SeedScores.
type seedEntry struct {
	id     string
	file   string
	sym    parser.Symbol
	weight float64
}

// frontierNode is one entry in the BFS frontier. The `seed` field records
// which seed's tree this node belongs to — the row-emission scoring looks
// up that seed's weight to compute the row's Score. Two frontier nodes can
// carry the same TargetID if they were reached at different steps from
// different seeds; the BFS code below dedupes both frontier expansion and
// row emission.
type frontierNode struct {
	id   string
	file string
	sym  parser.Symbol
	seed string // originating seed id (for scoring)
}

// QueryGraph returns a Handler that performs a multi-hop traversal from a
// single seed (legacy) OR a multi-seed set (issue #35 GraphRAG shape). The
// traversal composes the existing scanXxx functions from nodeedges.go —
// no new graph API is introduced. Depth, result count, total visited
// nodes, and wallclock are all bounded so a misbehaving query can't OOM
// the process.
func QueryGraph(repo *store.Repo) func(ctx context.Context, args json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		var a QueryGraphArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("invalid query_graph args: %w", err)
		}
		follow, err := normaliseFollow(a.Follow)
		if err != nil {
			return nil, fmt.Errorf("query_graph: %w", err)
		}
		if a.Depth == 0 {
			a.Depth = queryGraphDefaultDepth
		}
		if a.Depth < 1 || a.Depth > queryGraphMaxDepth {
			return nil, fmt.Errorf("query_graph: depth %d out of range [1,%d]", a.Depth, queryGraphMaxDepth)
		}
		if a.Limit == 0 {
			a.Limit = queryGraphDefaultLimit
		}
		if a.Limit < 1 || a.Limit > queryGraphMaxLimit {
			return nil, fmt.Errorf("query_graph: limit %d out of range [1,%d]", a.Limit, queryGraphMaxLimit)
		}
		kindFilter, err := normaliseKind(a.Kind)
		if err != nil {
			return nil, fmt.Errorf("query_graph: %w", err)
		}
		excludeTests, err := normaliseExclude(a.Exclude)
		if err != nil {
			return nil, fmt.Errorf("query_graph: %w", err)
		}

		// Resolve input mode: single-seed (from) or multi-seed (seeds).
		// Validation of mutual exclusion, seeds-list cap, weights alignment
		// lives in normaliseSeedsAndWeights so the handler body stays flat.
		seeds, weights, singleFrom, err := normaliseSeedsAndWeights(a.From, a.Seeds, a.Weights)
		if err != nil {
			return nil, fmt.Errorf("query_graph: %w", err)
		}
		// Locate each seed in the repo (parse id, find (file, sym)). Bad
		// seeds surface as "cannot locate seeds[N]" with the offending index.
		seedEntries, err := locateSeeds(repo, seeds, weights)
		if err != nil {
			return nil, fmt.Errorf("query_graph: %w", err)
		}

		rctx, cancel := context.WithTimeout(ctx, queryGraphMaxRuntime)
		defer cancel()

		// Prov stamped on every row. YacttProvenance — the answer is
		// composed in-process from in-memory indices, not from a parse pass.
		prov := domain.YacttProvenance()

		rows, visited, truncated, seedScores := bfsFromSeeds(
			rctx, repo, seedEntries, follow,
			a.Depth, a.Limit, kindFilter, excludeTests, &prov,
		)

		return &QueryGraphResult{
			Rows:       rows,
			Count:      len(rows),
			Truncated:  truncated,
			Visited:    visited,
			From:       singleFrom,
			Seeds:      seedsOnly(seeds, singleFrom),
			Weights:    weightsOnly(weights, singleFrom),
			SeedScores: seedScoresOnly(seedScores, singleFrom),
			Followed:   follow,
			Provenance: prov,
		}, nil
	}
}

// normaliseFollow lower-cases and validates each follow entry. Returning
// the normalised slice (not the input) keeps the rest of the handler
// lowercase-only and lets the error path run before any state changes.
func normaliseFollow(in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, fmt.Errorf("follow must be a non-empty list")
	}
	out := make([]string, 0, len(in))
	for _, raw := range in {
		f := strings.ToLower(strings.TrimSpace(raw))
		if _, ok := validFollowKinds[f]; !ok {
			return nil, fmt.Errorf("unknown follow kind %q (allowed: callers, callees, tests, imports, overrides)", raw)
		}
		out = append(out, f)
	}
	return out, nil
}

// normaliseKind upper-cases the kind filter and verifies it's a known
// NodeKind. Empty string is allowed (no filter).
func normaliseKind(in string) (domain.NodeKind, error) {
	if in == "" {
		return "", nil
	}
	k := domain.NodeKind(strings.ToUpper(strings.TrimSpace(in)))
	switch k {
	case domain.KindRepo, domain.KindPackage, domain.KindFile,
		domain.KindFunction, domain.KindMethod, domain.KindClass, domain.KindModule:
		return k, nil
	}
	return "", fmt.Errorf("unknown kind %q", in)
}

// normaliseExclude parses the exclude knob. Only "tests" is supported in v1
// — it filters rows whose target file ends in _test.go.
func normaliseExclude(in string) (bool, error) {
	if in == "" {
		return false, nil
	}
	if strings.EqualFold(strings.TrimSpace(in), "tests") {
		return true, nil
	}
	return false, fmt.Errorf("unknown exclude value %q (only \"tests\" is supported)", in)
}

// normaliseSeedsAndWeights handles the from-vs-seeds mutual exclusion and
// returns the (deduplicated) seed id list, parallel weights (default 1.0),
// the original `from` string for the single-seed path, or a validation
// error. Validation covers: exactly-one-of, seeds cap (50), weights
// alignment 1:1 with seeds.
//
// Dedup keeps the FIRST occurrence's id; for duplicates, weight is the
// max across duplicates so a re-listed seed doesn't lose its strongest
// signal.
func normaliseSeedsAndWeights(from string, seeds []string, weights []float64) ([]string, []float64, string, error) {
	hasFrom := from != ""
	hasSeeds := len(seeds) > 0
	if hasFrom && hasSeeds {
		return nil, nil, "", fmt.Errorf("from and seeds are mutually exclusive")
	}
	if !hasFrom && !hasSeeds {
		return nil, nil, "", fmt.Errorf("provide one of from or seeds")
	}
	if hasFrom {
		return []string{from}, []float64{1.0}, from, nil
	}
	// Multi-seed path.
	if len(seeds) > queryGraphMaxSeeds {
		return nil, nil, "", fmt.Errorf("seeds list capped at %d (got %d)", queryGraphMaxSeeds, len(seeds))
	}
	if len(weights) > 0 && len(weights) != len(seeds) {
		return nil, nil, "", fmt.Errorf("weights must align 1:1 with seeds (got %d weights for %d seeds)", len(weights), len(seeds))
	}
	if len(weights) == 0 {
		weights = make([]float64, len(seeds))
		for i := range weights {
			weights[i] = 1.0
		}
	}
	// Dedup: keep first occurrence's id; weight = max over duplicates.
	// Map from seed id -> (idx in deduped slice, current weight).
	type entry struct {
		idx     int
		weight  float64
	}
	seen := map[string]entry{}
	dedupedSeeds := []string{}
	dedupedWeights := []float64{}
	for i, s := range seeds {
		if e, ok := seen[s]; ok {
			if weights[i] > dedupedWeights[e.idx] {
				dedupedWeights[e.idx] = weights[i]
				seen[s] = entry{idx: e.idx, weight: dedupedWeights[e.idx]}
			}
			continue
		}
		seen[s] = entry{idx: len(dedupedSeeds), weight: weights[i]}
		dedupedSeeds = append(dedupedSeeds, s)
		dedupedWeights = append(dedupedWeights, weights[i])
	}
	return dedupedSeeds, dedupedWeights, "", nil
}

// locateSeeds resolves seed ids to seedEntry tuples (id + file + sym +
// weight); the first bad seed surfaces as "cannot locate seeds[N]" with
// the offending index. The seed id is preserved as-is so the response
// can echo it back.
func locateSeeds(repo *store.Repo, seeds []string, weights []float64) ([]seedEntry, error) {
	entries := make([]seedEntry, len(seeds))
	for i, s := range seeds {
		nodeID, err := id.Parse(s)
		if err != nil {
			return nil, fmt.Errorf("cannot parse seeds[%d] %q: %w", i, s, err)
		}
		file, sym, ok, lerr := repo.LocateSymbol(nodeID)
		if lerr != nil || !ok {
			return nil, fmt.Errorf("cannot locate seeds[%d] %q", i, s)
		}
		entries[i] = seedEntry{
			id:     s,
			file:   file,
			sym:    sym,
			weight: weights[i],
		}
	}
	return entries, nil
}

// seedsOnly / weightsOnly / seedScoresOnly hide the multi-seed-only fields
// on the single-seed path so the JSON envelope stays minimal for the
// legacy `from` callers. From already carries the seed id; SeedScores is
// trivially {from: score-from-its-only-row} when len(seeds) == 1, which is
// noise rather than information.
func seedsOnly(seeds []string, singleFrom string) []string {
	if singleFrom != "" {
		return nil
	}
	return seeds
}
func weightsOnly(weights []float64, singleFrom string) []float64 {
	if singleFrom != "" {
		return nil
	}
	return weights
}
func seedScoresOnly(m map[string]float64, singleFrom string) map[string]float64 {
	if singleFrom != "" {
		return nil
	}
	return m
}

// rowBest tracks per-node best row during the BFS so a node reached by
// multiple seeds at different depths collapses to ONE row with the
// winning seed's contribution. The slice index lets us update rows[]
// in place when a later step produces a better contribution.
type rowBest struct {
	rowIdx int
	depth  int
	score  float64
	seed   string
}

// stepHit is a (target, seed, score, depth, edge) tuple gathered during one
// BFS step. Within a single step all seeds reach their neighbours at the
// same depth; the per-step bestByTarget dedup picks the seed whose
// contribution produces the highest Score (tie: smaller depth).
type stepHit struct {
	target string
	seed   string
	score  float64
	depth  int
	edge   NodeEdgesResult
}

// bfsFromSeeds is the shared BFS core used by both single-seed and
// multi-seed paths. It walks `depth` steps, cycling across `follow` kinds,
// deduping both the frontier (via `seen`) and the row set (via
// `bestByNode`), accumulating per-row Score from the contributing seed,
// and respecting the visited/limit/wallclock caps.
//
// Score formula: for a node reached at step d from seed s with weight w_s,
//
//	base     = 1 / (1 + d)            // d=1 → 0.5, d=2 → 0.33, …
//	weighted = w_s * base             // per-seed contribution
//
// When multiple seeds reach the same node, the row's Score is the max
// weighted contribution; ties broken by smaller depth. Confidence and
// other edge metadata come from whichever seed's scan emitted the row
// first; subsequent improvements update Score + Depth but not the
// metadata (Location, Provenance, Via) — that's a deliberate tradeoff
// for v1 simplicity; the metadata still represents one valid traversal
// path to the node.
//
// Returns: rows, visited count, truncated flag, per-seed max Score.
func bfsFromSeeds(
	ctx context.Context,
	repo *store.Repo,
	seeds []seedEntry,
	follow []string,
	depth int,
	limit int,
	kindFilter domain.NodeKind,
	excludeTests bool,
	p *domain.Provenance,
) ([]QueryGraphRow, int, bool, map[string]float64) {
	rows := []QueryGraphRow{}
	seen := map[string]bool{}         // node id -> already expanded (or will be)
	bestByNode := map[string]rowBest{} // node id -> best row so far
	seedScores := map[string]float64{} // seed id -> max Score contributed
	visited := 0
	truncated := false

	// Initial frontier: every seed at depth 0. Mark seeds as seen so the
	// BFS doesn't emit them as their own neighbours (matches the original
	// single-seed behaviour).
	frontier := make([]frontierNode, 0, len(seeds))
	for _, s := range seeds {
		seen[s.id] = true
		frontier = append(frontier, frontierNode{id: s.id, file: s.file, sym: s.sym, seed: s.id})
	}

	// Cache the seed id -> weight lookup; the frontier carries seed ids,
	// not weights, so we resolve on demand. The slice is tiny (<= 50).
	weightOf := func(seedID string) float64 {
		for _, s := range seeds {
			if s.id == seedID {
				return s.weight
			}
		}
		return 1.0
	}

	for step := 0; step < depth; step++ {
		if ctx.Err() != nil {
			truncated = true
			break
		}
		via := follow[step%len(follow)]

		// Gather every (target, seed, score) this step produces, across
		// all frontier nodes. Dedupe within step (best seed wins), then
		// update bestByNode (best across all steps wins).
		hits := make([]stepHit, 0, len(frontier)*4)
		for _, fn := range frontier {
			if visited >= queryGraphMaxVisited {
				truncated = true
				break
			}
			if ctx.Err() != nil {
				truncated = true
				break
			}
			visited++
			edges := scanFor(via, repo, fn.file, fn.sym, limit, p)
			d := step + 1
			w := weightOf(fn.seed)
			base := 1.0 / float64(1+d)
			score := w * base
			for _, e := range edges {
				hits = append(hits, stepHit{
					target: e.TargetID,
					seed:   fn.seed,
					score:  score,
					depth:  d,
					edge:   e,
				})
			}
		}
		if truncated {
			break
		}

		// Within-step dedup: per target, pick the highest-scoring hit
		// (ties: smaller depth, then first seen). This collapses "two
		// seeds reach the same node at this step" into one entry while
		// keeping the better contribution.
		bestForTarget := map[string]stepHit{}
		for _, h := range hits {
			if existing, ok := bestForTarget[h.target]; ok {
				if h.score > existing.score ||
					(h.score == existing.score && h.depth < existing.depth) {
					bestForTarget[h.target] = h
				}
			} else {
				bestForTarget[h.target] = h
			}
		}

		// Per-seed max: track EVERY contributing seed's best score —
		// including ones that lost the within-step tiebreak. Otherwise
		// a higher-weight seed "hides" the lower-weight seeds' hits
		// from SeedScores, and the explain output can't show "these
		// seeds contributed rows too, just at a lower rank".
		for _, h := range hits {
			if existing, ok := seedScores[h.seed]; !ok || h.score > existing {
				seedScores[h.seed] = h.score
			}
		}

		// Expand frontier + emit/update rows.
		next := make([]frontierNode, 0, len(bestForTarget))
		for _, h := range bestForTarget {
			// Capture "is this a new expansion?" before mutating seen.
			// Two signals matter here:
			//
			//   seen        — already in the frontier's expansion set
			//                 (seeds are marked up front; cycles reach
			//                 already-expanded nodes). Gated by expand.
			//   bestByNode  — already emitted as a row. Gated by the
			//                 update-vs-emit branch below.
			//
			// A later seed reaching an already-emitted node should
			// UPDATE the row's score/depth, not skip — that's the
			// multi-seed dedup contract.
			expand := !seen[h.target]
			if expand {
				seen[h.target] = true
				tid, perr := id.Parse(h.target)
				if perr == nil {
					if tf, ts, ok2, _ := repo.LocateSymbol(tid); ok2 {
						next = append(next, frontierNode{
							id:   h.target,
							file: tf,
							sym:  ts,
							seed: h.seed,
						})
					}
				}
			}

			// Apply row-level filters BEFORE emission/update.
			if excludeTests && strings.HasSuffix(h.edge.Location.File, "_test.go") {
				continue
			}
			if kindFilter != "" && h.edge.TargetKind != kindFilter {
				continue
			}

			// Three branches:
			//
			//   bestByNode exists → row already emitted; update if better
			//   !bestByNode && expand → first emission for this target
			//   !bestByNode && !expand → cycle back to seed or already-
			//     expanded node; the original single-seed contract
			//     suppressed this row, so we keep that contract
			if existing, ok := bestByNode[h.target]; ok {
				if h.score > existing.score ||
					(h.score == existing.score && h.depth < existing.depth) {
					rows[existing.rowIdx].Depth = h.depth
					rows[existing.rowIdx].Score = h.score
					bestByNode[h.target] = rowBest{
						rowIdx: existing.rowIdx,
						depth:  h.depth,
						score:  h.score,
						seed:   h.seed,
					}
				}
			} else if expand {
				if len(rows) >= limit {
					truncated = true
					continue
				}
				rows = append(rows, QueryGraphRow{
					EdgeKind:      h.edge.EdgeKind,
					TargetID:      h.target,
					TargetKind:    h.edge.TargetKind,
					TargetSummary: h.edge.TargetSummary,
					Location:      h.edge.Location,
					Confidence:    h.edge.Confidence,
					Provenance:    h.edge.Provenance,
					Depth:         h.depth,
					Via:           []string{via},
					Score:         h.score,
				})
				bestByNode[h.target] = rowBest{
					rowIdx: len(rows) - 1,
					depth:  h.depth,
					score:  h.score,
					seed:   h.seed,
				}
			}
		}

		if len(next) == 0 {
			break
		}
		frontier = next
	}

	if ctx.Err() != nil {
		truncated = true
	}

	return rows, visited, truncated, seedScores
}

// scanFor dispatches to the matching scanner. All five edge kinds reuse
// the existing scanXxx from nodeedges.go — the handlers share a package,
// so the dispatch is in-process and free.
func scanFor(kind string, repo *store.Repo, file string, sym parser.Symbol, limit int, p *domain.Provenance) []NodeEdgesResult {
	switch kind {
	case "callees":
		return scanCallees(repo, file, sym, limit, p)
	case "callers":
		return scanCallers(repo, file, sym, limit, p)
	case "tests":
		return scanTests(repo, file, sym, limit, p)
	case "imports":
		return scanImports(repo, file, sym, limit, p)
	case "overrides":
		return scanOverrides(repo, file, sym, limit, p)
	}
	return nil
}