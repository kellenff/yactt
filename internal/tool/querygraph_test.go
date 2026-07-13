package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/kellenff/yactt/internal/store"
	"github.com/kellenff/yactt/internal/store/repofixture"
)

// loadQueryGraphRepo builds the fixture and loads it once per test. Mirrors
// loadTestRepo in graphschema_test.go.
func loadQueryGraphRepo(t *testing.T) *store.Repo {
	t.Helper()
	fx := repofixture.New(t)
	r, _, err := store.Load(fx.Root)
	if err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// runQueryGraph drives the handler with raw JSON and type-asserts.
func runQueryGraph(t *testing.T, repo *store.Repo, args string) *QueryGraphResult {
	t.Helper()
	out, err := QueryGraph(seedRegFromRepo(t, r))(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	qg, ok := out.(*QueryGraphResult)
	if !ok {
		t.Fatalf("result type: got %T", out)
	}
	return qg
}

// TestQueryGraph_AcceptanceTraversal pins the headline acceptance criterion
// from issue #9: a multi-hop traversal the fixed tools cannot express in
// one call. node_edges is single-hop; chaining two of them yourself also
// requires feeding the second call the right id, format, and dedup logic.
// query_graph composes that for you.
//
// The fixture seeds:
//
//	auth.Alpha.Ping    → payments.Charge
//	auth.Authenticate  → payments.Charge
//	auth.User.Refresh  → payments.Charge
//	auth.Beta.Ping     → payments.Refund
//
// A 2-hop "follow callees then callers" walk from Alpha.Ping discovers
// Charge at depth 1, then the two OTHER callers of Charge at depth 2.
// Alpha.Ping itself appears at depth 2 as a caller of Charge but is
// deduped against the seed.
func TestQueryGraph_AcceptanceTraversal(t *testing.T) {
	r := loadQueryGraphRepo(t)

	qg := runQueryGraph(t, r, `{
		"from": "meth:auth.Alpha.Ping",
		"follow": ["callees", "callers"],
		"depth": 2,
		"limit": 50
	}`)

	if qg.From != "meth:auth.Alpha.Ping" {
		t.Errorf("from = %q, want fn:auth.Alpha.Ping", qg.From)
	}
	if len(qg.Followed) != 2 || qg.Followed[0] != "callees" || qg.Followed[1] != "callers" {
		t.Errorf("followed = %v, want [callees, callers]", qg.Followed)
	}
	if qg.Visited < 2 {
		t.Errorf("visited = %d, want >= 2 (one per hop)", qg.Visited)
	}
	if qg.Count == 0 {
		t.Fatalf("expected non-empty rows, got %+v", qg)
	}

	// Bucket rows by depth for easy assertions.
	byDepth := map[int][]QueryGraphRow{}
	for _, row := range qg.Rows {
		byDepth[row.Depth] = append(byDepth[row.Depth], row)
	}

	// Depth 1: Alpha.Ping's callees must include payments.Charge.
	rows := byDepth[1]
	if len(rows) == 0 {
		t.Errorf("depth 1 rows empty, want payments.Charge")
	} else {
		foundCharge := false
		for _, row := range rows {
			if row.TargetID == "fn:payments.Charge" {
				foundCharge = true
				if len(row.Via) != 1 || row.Via[0] != "callees" {
					t.Errorf("depth 1 via = %v, want [callees]", row.Via)
				}
			}
		}
		if !foundCharge {
			t.Errorf("depth 1 missing fn:payments.Charge; got %d rows", len(rows))
		}
	}

	// Depth 2: callers of Charge. Alpha.Ping is deduped (it's the seed),
	// so the result is the two OTHER callers — auth.Authenticate and
	// auth.User.Refresh.
	rows = byDepth[2]
	if len(rows) < 2 {
		t.Errorf("depth 2 rows = %d, want >= 2 (Authenticate, Refresh); got %v", len(rows), targetIDs(rows))
	} else {
		seen := map[string]bool{}
		for _, row := range rows {
			seen[row.TargetID] = true
		}
		if !seen["fn:auth.Authenticate"] {
			t.Errorf("depth 2 missing fn:auth.Authenticate; got %v", targetIDs(rows))
		}
		if !seen["meth:auth.User.Refresh"] {
			t.Errorf("depth 2 missing meth:auth.User.Refresh; got %v", targetIDs(rows))
		}
		if seen["meth:auth.Alpha.Ping"] {
			t.Errorf("depth 2 should not include Alpha.Ping (deduped against seed); got %v", targetIDs(rows))
		}
	}
}

// TestQueryGraph_KindFilter verifies the kind filter is a row-emission
// gate, not a traversal gate — agents can drill past non-matching nodes
// to reach deeper matches.
func TestQueryGraph_KindFilter(t *testing.T) {
	r := loadQueryGraphRepo(t)

	qg := runQueryGraph(t, r, `{
		"from": "meth:auth.Alpha.Ping",
		"follow": ["callees", "callers"],
		"depth": 2,
		"kind": "function",
		"limit": 50
	}`)

	for _, row := range qg.Rows {
		if row.TargetKind != "FUNCTION" {
			t.Errorf("row %q kind=%q, want FUNCTION", row.TargetID, row.TargetKind)
		}
	}
}

// TestQueryGraph_DefaultsAndValidation pins the validation contract: empty
// args must fail, unknown follow kind must fail, depth/limit out of range
// must fail. Depth:0 silently uses the default; limit:0 does the same.
func TestQueryGraph_DefaultsAndValidation(t *testing.T) {
	r := loadQueryGraphRepo(t)

	cases := []struct {
		name    string
		args    string
		wantErr string
	}{
		{"missing from and seeds", `{"follow":["callees"]}`, "provide one of from or seeds"},
		{"both from and seeds", `{"from":"meth:auth.Alpha.Ping","seeds":["fn:auth.Login"],"follow":["callees"]}`, "mutually exclusive"},
		{"empty follow", `{"from":"meth:auth.Alpha.Ping","follow":[]}`, "follow must be a non-empty list"},
		{"unknown follow", `{"from":"meth:auth.Alpha.Ping","follow":["wat"]}`, "unknown follow kind"},
		{"depth too high", `{"from":"meth:auth.Alpha.Ping","follow":["callees"],"depth":99}`, "depth 99 out of range"},
		{"limit too high", `{"from":"meth:auth.Alpha.Ping","follow":["callees"],"limit":99999}`, "limit 99999 out of range"},
		{"unknown kind", `{"from":"meth:auth.Alpha.Ping","follow":["callees"],"kind":"bogus"}`, "unknown kind"},
		{"unknown exclude", `{"from":"meth:auth.Alpha.Ping","follow":["callees"],"exclude":"prod"}`, "unknown exclude value"},
		{"unresolved from", `{"from":"fn:auth.NoSuch","follow":["callees"]}`, "cannot locate"},
		{"bad from id", `{"from":"nope:nope","follow":["callees"]}`, ""},
		{"weights align mismatch", `{"seeds":["fn:auth.Login"],"weights":[0.5,0.5],"follow":["callers"]}`, "weights must align"},
		{"bad seed id", `{"seeds":["fn:auth.NoSuchFn"],"follow":["callers"]}`, "cannot locate seeds"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := QueryGraph(seedRegFromRepo(t, r))(context.Background(), json.RawMessage(tc.args))
			if tc.wantErr == "" {
				// Just exercise the call — bad-from-id may resolve to
				// "cannot locate" or a parse error; both are valid.
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}

	t.Run("depth 0 uses default", func(t *testing.T) {
		_, err := QueryGraph(seedRegFromRepo(t, r))(context.Background(), json.RawMessage(`{
			"from":"meth:auth.Alpha.Ping","follow":["callees"],"depth":0
		}`))
		if err != nil {
			t.Fatalf("depth:0 should default to 2, got error: %v", err)
		}
	})

	t.Run("limit 0 uses default", func(t *testing.T) {
		_, err := QueryGraph(seedRegFromRepo(t, r))(context.Background(), json.RawMessage(`{
			"from":"meth:auth.Alpha.Ping","follow":["callees"],"limit":0
		}`))
		if err != nil {
			t.Fatalf("limit:0 should default to 100, got error: %v", err)
		}
	})

	t.Run("seeds list capped at 50", func(t *testing.T) {
		// Build a JSON with 51 seed entries — past the cap.
		seedJSON := make([]string, 51)
		for i := range seedJSON {
			seedJSON[i] = fmt.Sprintf(`"fn:auth.Fn%d"`, i)
		}
		args := `{"seeds":[` + strings.Join(seedJSON, ",") + `],"follow":["callers"]}`
		_, err := QueryGraph(seedRegFromRepo(t, r))(context.Background(), json.RawMessage(args))
		if err == nil {
			t.Fatalf("expected cap error, got nil")
		}
		if !strings.Contains(err.Error(), "capped at 50") {
			t.Fatalf("error %q does not contain %q", err.Error(), "capped at 50")
		}
	})
}

// TestQueryGraph_Provenance pins that the tool stamps Tool="yactt" — the
// answer is composed in-process from in-memory indices, not from a parse
// pass, so "tree-sitter" would be misleading. Matches get_graph_schema's
// precedent.
func TestQueryGraph_Provenance(t *testing.T) {
	r := loadQueryGraphRepo(t)
	qg := runQueryGraph(t, r, `{
		"from":"meth:auth.Alpha.Ping","follow":["callees"],"depth":1
	}`)
	if qg.Provenance.Tool != "yactt" {
		t.Errorf("provenance.Tool = %q, want \"yactt\"", qg.Provenance.Tool)
	}
	if qg.Provenance.Version == "" {
		t.Error("provenance.Version is empty")
	}
	for _, row := range qg.Rows {
		if row.Provenance.Tool != "yactt" {
			t.Errorf("row %q provenance.Tool = %q, want \"yactt\"", row.TargetID, row.Provenance.Tool)
		}
	}
}

// targetIDs extracts just the TargetID field for compact error messages.
func targetIDs(rows []QueryGraphRow) []string {
	out := make([]string, len(rows))
	for i, row := range rows {
		out[i] = row.TargetID
	}
	return out
}

// TestQueryGraph_MultiSeed_Fanout pins the GraphRAG contract for two seeds
// that reach DISTINCT trees: each seed's neighbourhood is independent, so
// the union of rows reflects both. The fixture's Alpha.Ping and Beta.Ping
// call Charge and Refund respectively (different callees at depth 1) —
// with multi-seed and follow=callees, we expect both Charge and Refund
// in the output at depth 1.
func TestQueryGraph_MultiSeed_Fanout(t *testing.T) {
	r := loadQueryGraphRepo(t)

	qg := runQueryGraph(t, r, `{
		"seeds": ["meth:auth.Alpha.Ping", "meth:auth.Beta.Ping"],
		"follow": ["callees"],
		"depth": 1,
		"limit": 50
	}`)

	if qg.From != "" {
		t.Errorf("From = %q, want \"\" for multi-seed path", qg.From)
	}
	if len(qg.Seeds) != 2 {
		t.Errorf("Seeds = %v, want 2 entries", qg.Seeds)
	}
	if len(qg.Weights) != 2 {
		t.Errorf("Weights = %v, want 2 entries (uniform 1.0)", qg.Weights)
	}
	for _, w := range qg.Weights {
		if w != 1.0 {
			t.Errorf("Weights = %v, want uniform 1.0", qg.Weights)
		}
	}

	depth1 := map[string]bool{}
	for _, row := range qg.Rows {
		if row.Depth != 1 {
			t.Errorf("row %q has depth %d, want 1", row.TargetID, row.Depth)
		}
		depth1[row.TargetID] = true
	}
	if !depth1["fn:payments.Charge"] {
		t.Errorf("depth 1 missing fn:payments.Charge; got %v", targetIDs(qg.Rows))
	}
	if !depth1["fn:payments.Refund"] {
		t.Errorf("depth 1 missing fn:payments.Refund; got %v", targetIDs(qg.Rows))
	}

	// Per-seed max Score: each seed produced one row at score 0.5.
	for seedID, score := range qg.SeedScores {
		if score != 0.5 {
			t.Errorf("seedScores[%q] = %f, want 0.5", seedID, score)
		}
	}
	if len(qg.SeedScores) != 2 {
		t.Errorf("SeedScores has %d entries, want 2", len(qg.SeedScores))
	}
}

// TestQueryGraph_MultiSeed_Dedup pins the dedup contract for the
// "two seeds reach the same node" case. Authenticate, User.Refresh, and
// Alpha.Ping ALL call Charge (depth 1). The output must contain Charge
// ONCE — not three times — with the highest-scoring contribution.
func TestQueryGraph_MultiSeed_Dedup(t *testing.T) {
	r := loadQueryGraphRepo(t)

	qg := runQueryGraph(t, r, `{
		"seeds": ["fn:auth.Authenticate", "meth:auth.User.Refresh", "meth:auth.Alpha.Ping"],
		"follow": ["callees"],
		"depth": 1,
		"limit": 50
	}`)

	// Charge should appear exactly once.
	count := 0
	for _, row := range qg.Rows {
		if row.TargetID == "fn:payments.Charge" {
			count++
			if row.Depth != 1 {
				t.Errorf("Charge row depth = %d, want 1", row.Depth)
			}
			if row.Score != 0.5 {
				t.Errorf("Charge row score = %f, want 0.5 (1/(1+1) with uniform weights)", row.Score)
			}
		}
	}
	if count != 1 {
		t.Errorf("Charge appeared %d times, want 1 (multi-seed dedup contract)", count)
	}

	// All three seeds contributed → all three in SeedScores at score 0.5.
	for _, seedID := range []string{
		"fn:auth.Authenticate",
		"meth:auth.User.Refresh",
		"meth:auth.Alpha.Ping",
	} {
		if score, ok := qg.SeedScores[seedID]; !ok {
			t.Errorf("SeedScores missing seed %q", seedID)
		} else if score != 0.5 {
			t.Errorf("SeedScores[%q] = %f, want 0.5", seedID, score)
		}
	}
}

// TestQueryGraph_ScoreFormula_Weighted pins that Score = weight * (1/(1+depth))
// when weights are non-uniform. Two seeds at depth 1, weights [0.8, 0.2].
// Both call Charge → dedup to one row with max(0.8 * 0.5, 0.2 * 0.5) =
// max(0.4, 0.1) = 0.4. SeedScores tracks each seed's contribution
// independently: 0.4 and 0.1 respectively.
func TestQueryGraph_ScoreFormula_Weighted(t *testing.T) {
	r := loadQueryGraphRepo(t)

	qg := runQueryGraph(t, r, `{
		"seeds": ["fn:auth.Authenticate", "meth:auth.Alpha.Ping"],
		"weights": [0.8, 0.2],
		"follow": ["callees"],
		"depth": 1,
		"limit": 50
	}`)

	var chargeRow *QueryGraphRow
	for i := range qg.Rows {
		if qg.Rows[i].TargetID == "fn:payments.Charge" {
			chargeRow = &qg.Rows[i]
			break
		}
	}
	if chargeRow == nil {
		t.Fatalf("Charge row not found; got %v", targetIDs(qg.Rows))
	}
	want := 0.4 // max(0.8*0.5, 0.2*0.5)
	if chargeRow.Score != want {
		t.Errorf("Charge row Score = %f, want %f (max of weighted contributions)", chargeRow.Score, want)
	}

	// Per-seed scores are the contributions regardless of which seed won
	// the row. Authenticate's contribution is 0.8 * 0.5 = 0.4; Alpha.Ping
	// contributes 0.2 * 0.5 = 0.1.
	wantAuth, wantAlpha := 0.4, 0.1
	if got := qg.SeedScores["fn:auth.Authenticate"]; got != wantAuth {
		t.Errorf("SeedScores[Authenticate] = %f, want %f", got, wantAuth)
	}
	if got := qg.SeedScores["meth:auth.Alpha.Ping"]; got != wantAlpha {
		t.Errorf("SeedScores[Alpha.Ping] = %f, want %f", got, wantAlpha)
	}
}

// TestQueryGraph_CapsFire_Limit pins that hitting the limit truncates
// with Truncated=true. The fixture's call graph is small, but we can
// force truncation by setting limit=1 with multiple seeds that fan out.
func TestQueryGraph_CapsFire_Limit(t *testing.T) {
	r := loadQueryGraphRepo(t)

	qg := runQueryGraph(t, r, `{
		"seeds": ["meth:auth.Alpha.Ping", "meth:auth.Beta.Ping", "fn:auth.Authenticate"],
		"follow": ["callees"],
		"depth": 5,
		"limit": 1
	}`)

	if !qg.Truncated {
		t.Errorf("Truncated = false, want true (limit=1 with multi-seed fanout)")
	}
	if len(qg.Rows) > 1 {
		t.Errorf("len(Rows) = %d, want <= 1", len(qg.Rows))
	}
}

// TestQueryGraph_DedupSeedsInput pins that the seed list is deduped at
// the boundary — re-listing the same seed id does not produce duplicate
// frontier entries. Using a single seed duplicated 3x should behave
// identically to a single-seed list.
func TestQueryGraph_DedupSeedsInput(t *testing.T) {
	r := loadQueryGraphRepo(t)

	// Single seed, depth 2, follow callers.
	qgSingle := runQueryGraph(t, r, `{
		"from": "fn:payments.Charge",
		"follow": ["callers"],
		"depth": 1,
		"limit": 50
	}`)

	// Same seed listed 3 times — should produce the same row set (with
	// SeedScores reflecting only the deduplicated seed).
	qgDups := runQueryGraph(t, r, `{
		"seeds": ["fn:payments.Charge", "fn:payments.Charge", "fn:payments.Charge"],
		"follow": ["callers"],
		"depth": 1,
		"limit": 50
	}`)

	if len(qgSingle.Rows) != len(qgDups.Rows) {
		t.Errorf("deduped seeds: single row count = %d, dups row count = %d", len(qgSingle.Rows), len(qgDups.Rows))
	}
	if len(qgDups.Seeds) != 1 {
		t.Errorf("deduped seeds: Seeds slice length = %d, want 1", len(qgDups.Seeds))
	}
	if len(qgDups.Weights) != 1 {
		t.Errorf("deduped seeds: Weights slice length = %d, want 1", len(qgDups.Weights))
	}
}

// TestQueryGraph_SingleSeedPreservesFromField pins that the single-seed
// path (from=...) still echoes From in the response and omits Seeds/
// Weights/SeedScores. The multi-seed path inverts this — Seeds is set,
// From is empty. The contract: exactly one of (From) or (Seeds) carries
// the seed id, never both.
func TestQueryGraph_SingleSeedPreservesFromField(t *testing.T) {
	r := loadQueryGraphRepo(t)

	qg := runQueryGraph(t, r, `{
		"from": "fn:auth.Login",
		"follow": ["callees"],
		"depth": 1,
		"limit": 10
	}`)

	if qg.From != "fn:auth.Login" {
		t.Errorf("From = %q, want \"fn:auth.Login\"", qg.From)
	}
	if qg.Seeds != nil {
		t.Errorf("Seeds = %v, want nil (single-seed path omits Seeds)", qg.Seeds)
	}
	if qg.Weights != nil {
		t.Errorf("Weights = %v, want nil (single-seed path omits Weights)", qg.Weights)
	}
	if qg.SeedScores != nil {
		t.Errorf("SeedScores = %v, want nil (single-seed path omits SeedScores)", qg.SeedScores)
	}
}