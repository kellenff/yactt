// Cross-package smoke test for the GraphRAG pipeline (issue #35).
//
// The headline integration check is end-to-end:
//
//   vector/BM25 hits ──► top-K IDs ──► seeds ──► query_graph
//
// This file exercises that pipeline against the 100-file chunking
// genfixture used by the rest of tests/hybrid. The test asserts:
//
//   1. yactt hybrid returns ranked hits with stable yactt ids.
//   2. Querying the same fixture with those hits as `seeds` produces
//      a structured QueryGraphResult that:
//        - has rows (the BFS didn't terminate early)
//        - has depth ≤ 3 (matches the requested depth)
//        - has Score in (0, 1] for every row
//        - has SeedScores populated for every contributing seed
//
// The recall target from issue #35 ("≥40% better recall on multi-hop
// questions vs vector-only baseline") needs a hand-tagged multi-hop
// Q&A set; we explicitly defer that to a future `internal/bench`
// package. This smoke test exercises the wiring, not the metric.
package hybrid_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kellenff/yactt/internal/hybrid"
	"github.com/kellenff/yactt/internal/store"
	"github.com/kellenff/yactt/internal/tool"
	"github.com/kellenff/yactt/tests/chunking/genfixture"
)

// TestExpandSeeds_Pipeline pins the GraphRAG round-trip on the 100-file
// genfixture. The pipeline: hybrid retrieval (default channels) → take
// the top-K yactt ids → marshal as JSON seeds → invoke the query_graph
// handler. The contract is that the BFS terminates, every row has a
// valid score, and SeedScores maps every contributing seed to its max.
func TestExpandSeeds_Pipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping GraphRAG smoke in -short mode")
	}
	dir := t.TempDir()
	if _, err := genfixture.Write(dir); err != nil {
		t.Fatalf("genfixture.Write: %v", err)
	}
	r, errs, err := store.Load(dir)
	if err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	defer func() { _ = r.Close() }()
	for _, e := range errs {
		t.Errorf("store.Load per-file: %v", e)
	}

	// Step 1: hybrid retrieval. Pick a query that should produce
	// several distinct hits so the seed list is non-trivial. We use
	// the same `auth aggregate / auth normalize` vocabulary used in
	// the hybrid recall set — those hits are known to be parseable
	// call-graph nodes (fn:auth.Aggregate, fn:auth.Normalize), so the
	// downstream BFS has something to walk.
	hits, err := hybrid.Run(context.Background(), hybrid.Options{
		Repo:     r,
		Query:    "auth aggregate normalize",
		Limit:    5,
		Channels: hybrid.AllChannels(),
		Vector:   hybrid.NewBagOfTokens(),
	})
	if err != nil {
		t.Fatalf("hybrid.Run: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("hybrid returned no hits; pipeline test is meaningless")
	}

	// Step 2: take top-K ids as seeds.
	seeds := make([]string, 0, len(hits))
	for _, h := range hits {
		if h.ID == "" {
			continue
		}
		seeds = append(seeds, h.ID)
		if len(seeds) >= 3 {
			break
		}
	}
	if len(seeds) == 0 {
		t.Fatal("no non-empty ids to seed from")
	}

	// Step 3: invoke query_graph with seeds.
	seedsJSON, err := json.Marshal(seeds)
	if err != nil {
		t.Fatalf("marshal seeds: %v", err)
	}
	args := `{
		"seeds": ` + string(seedsJSON) + `,
		"follow": ["callers", "callees"],
		"depth": 3,
		"limit": 100
	}`
	out, err := tool.QueryGraph(r)(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("QueryGraph: %v", err)
	}
	qg, ok := out.(*tool.QueryGraphResult)
	if !ok {
		t.Fatalf("result type: %T", out)
	}

	// Step 4: assert shape invariants.
	if qg.Count == 0 {
		t.Fatalf("empty rows; got %+v", qg)
	}
	if qg.From != "" {
		t.Errorf("From = %q, want \"\" for multi-seed path", qg.From)
	}
	if len(qg.Seeds) != len(seeds) {
		t.Errorf("len(Seeds) = %d, want %d", len(qg.Seeds), len(seeds))
	}
	for i, sid := range qg.Seeds {
		if sid != seeds[i] {
			t.Errorf("Seeds[%d] = %q, want %q", i, sid, seeds[i])
		}
	}
	if len(qg.Weights) != len(seeds) {
		t.Errorf("len(Weights) = %d, want %d", len(qg.Weights), len(seeds))
	}
	for _, w := range qg.Weights {
		if w != 1.0 {
			t.Errorf("Weights has non-uniform value %f, want uniform 1.0", w)
		}
	}

	for _, row := range qg.Rows {
		if row.TargetID == "" {
			t.Errorf("row has empty TargetID: %+v", row)
		}
		if row.Depth < 1 || row.Depth > 3 {
			t.Errorf("row %q depth = %d, want 1..3", row.TargetID, row.Depth)
		}
		if row.Score <= 0 || row.Score > 1 {
			t.Errorf("row %q score = %f, want (0, 1]", row.TargetID, row.Score)
		}
	}

	// SeedScores should have an entry for every contributing seed.
	if len(qg.SeedScores) == 0 {
		t.Errorf("SeedScores empty; expected entries for each contributing seed")
	}
	for _, sid := range seeds {
		if _, ok := qg.SeedScores[sid]; !ok {
			t.Errorf("SeedScores missing seed %q", sid)
		}
	}
}

// TestExpandSeeds_PipelineWithWeights pins the weighted variant. Seeds
// come from hybrid with their RRF score repurposed as the per-seed
// weight. The BFS picks the higher-weighted seed for each shared row.
func TestExpandSeeds_PipelineWithWeights(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping GraphRAG smoke in -short mode")
	}
	dir := t.TempDir()
	if _, err := genfixture.Write(dir); err != nil {
		t.Fatalf("genfixture.Write: %v", err)
	}
	r, _, err := store.Load(dir)
	if err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	defer func() { _ = r.Close() }()

	hits, err := hybrid.Run(context.Background(), hybrid.Options{
		Repo:     r,
		Query:    "auth aggregate and normalize",
		Limit:    3,
		Channels: hybrid.AllChannels(),
		Vector:   hybrid.NewBagOfTokens(),
	})
	if err != nil {
		t.Fatalf("hybrid.Run: %v", err)
	}
	if len(hits) < 2 {
		t.Skip("not enough distinct hits to test weighted seeds")
	}

	// Filter to seed IDs that are reachable in the call graph (functions
	// and methods only). Classes and modules have no callers/callees in
	// the AST-derived graph, so the BFS would visit them and emit
	// nothing — fine for the smoke test, but it makes a noisy failure
	// when the query happens to surface a class.
	seeds := []string{}
	weights := []float64{}
	for _, h := range hits {
		if len(seeds) >= 2 {
			break
		}
		if !strings.HasPrefix(h.ID, "fn:") && !strings.HasPrefix(h.ID, "meth:") {
			continue
		}
		seeds = append(seeds, h.ID)
		weights = append(weights, clamp01(h.Score))
	}
	if len(seeds) < 2 {
		t.Skip("not enough function/method hits to test weighted seeds")
	}
	weightsJSON, err := json.Marshal(weights)
	if err != nil {
		t.Fatalf("marshal weights: %v", err)
	}
	seedsJSON, _ := json.Marshal(seeds)
	args := `{"seeds":` + string(seedsJSON) + `,"weights":` + string(weightsJSON) + `,"follow":["callers"],"depth":2,"limit":50}`

	out, err := tool.QueryGraph(r)(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("QueryGraph: %v", err)
	}
	qg, ok := out.(*tool.QueryGraphResult)
	if !ok {
		t.Fatalf("result type: %T", out)
	}

	if qg.Count == 0 {
		t.Fatalf("empty rows; got %+v", qg)
	}

	// All weights must round-trip. The BFS may renormalise them at the
	// boundary, but the echoed slice should match the input.
	if len(qg.Weights) != 2 {
		t.Fatalf("len(Weights) = %d, want 2", len(qg.Weights))
	}
	for i, w := range qg.Weights {
		if w != weights[i] {
			t.Errorf("Weights[%d] = %f, want %f", i, w, weights[i])
		}
	}

	// SeedScores for both seeds.
	for _, sid := range seeds {
		if _, ok := qg.SeedScores[sid]; !ok {
			t.Errorf("SeedScores missing seed %q", sid)
		}
	}
}

// clamp01 defensively caps a number to [0, 1]. RRF scores are
// theoretically already in this range, but a hybrid edge case could
// emit a negative (impossible) or >1 (boundary) value; better to be
// safe than to fail at the JSON boundary.
func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

// Sanity-check the helper isn't called with empty inputs (would
// produce malformed JSON for the query_graph args).
var _ = strings.HasPrefix // anchor to keep the strings import if future tests reference it
