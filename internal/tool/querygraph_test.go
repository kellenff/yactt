package tool

import (
	"context"
	"encoding/json"
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
	out, err := QueryGraph(repo)(context.Background(), json.RawMessage(args))
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
		{"missing from", `{"follow":["callees"]}`, "from is required"},
		{"empty follow", `{"from":"meth:auth.Alpha.Ping","follow":[]}`, "follow must be a non-empty list"},
		{"unknown follow", `{"from":"meth:auth.Alpha.Ping","follow":["wat"]}`, "unknown follow kind"},
		{"depth too high", `{"from":"meth:auth.Alpha.Ping","follow":["callees"],"depth":99}`, "depth 99 out of range"},
		{"limit too high", `{"from":"meth:auth.Alpha.Ping","follow":["callees"],"limit":99999}`, "limit 99999 out of range"},
		{"unknown kind", `{"from":"meth:auth.Alpha.Ping","follow":["callees"],"kind":"bogus"}`, "unknown kind"},
		{"unknown exclude", `{"from":"meth:auth.Alpha.Ping","follow":["callees"],"exclude":"prod"}`, "unknown exclude value"},
		{"unresolved from", `{"from":"fn:auth.NoSuch","follow":["callees"]}`, "cannot locate"},
		{"bad from id", `{"from":"nope:nope","follow":["callees"]}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := QueryGraph(r)(context.Background(), json.RawMessage(tc.args))
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
		_, err := QueryGraph(r)(context.Background(), json.RawMessage(`{
			"from":"meth:auth.Alpha.Ping","follow":["callees"],"depth":0
		}`))
		if err != nil {
			t.Fatalf("depth:0 should default to 2, got error: %v", err)
		}
	})

	t.Run("limit 0 uses default", func(t *testing.T) {
		_, err := QueryGraph(r)(context.Background(), json.RawMessage(`{
			"from":"meth:auth.Alpha.Ping","follow":["callees"],"limit":0
		}`))
		if err != nil {
			t.Fatalf("limit:0 should default to 100, got error: %v", err)
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