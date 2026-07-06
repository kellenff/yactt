// Package acceptance_test — search_code acceptance. Drives the production
// handler against ../fixtures/sample-go and asserts the headline
// behaviour from issue #10:
//
//   - multiple raw matches inside one function collapse to one group,
//   - the dedup'd group carries the enclosing function's NodeID,
//   - the envelope stamps Provenance.Tool.
//
// The acceptance layer catches correctness regressions that contract
// tests can't reach (real on-disk fixture with go.mod + _test.go files;
// tests for the test-bucket path live here).
package acceptance_test

import (
	"strings"
	"testing"

	"github.com/kellenff/yactt/internal/tool"
)

// TestSearchCodeAcceptance_Dedup pins the headline behaviour. The
// fixture's auth/login.go `Login` function uses `err` on three lines
// of its body (the `if err := ...; err != nil` checks at lines 25,
// 29, 30). A regex scan for `err` produces three raw matches inside
// the same function; search_code must collapse them into one group
// keyed by the enclosing function's NodeID, with matchCount=3 and
// len(matches)=3.
func TestSearchCodeAcceptance_Dedup(t *testing.T) {
	repo := loadRepo(t)
	out := callJSON(t, tool.SearchCode(repo), `{"pattern":"err","pattern_kind":"regex","limit":50}`)
	env, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("search_code envelope type: got %T", out)
	}
	groups, ok := env["groups"].([]tool.SearchCodeGroup)
	if !ok {
		t.Fatalf("search_code groups slice type: got %T", env["groups"])
	}
	if len(groups) == 0 {
		t.Fatal("expected at least one group for err; got 0")
	}

	// Dedup invariant: every group's matchCount must equal len(matches).
	// A regression that drifts the counter from the slice would surface
	// here. This is the structural property of "one group per
	// containing function".
	for _, g := range groups {
		if g.MatchCount != len(g.Matches) {
			t.Errorf("group %s: matchCount=%d != len(matches)=%d (dedup invariant violated)",
				g.NodeID, g.MatchCount, len(g.Matches))
		}
	}

	// At least one group must be the `Login` function with matchCount
	// >= 3 — the three `err` references inside it must collapse to a
	// single group, not three groups.
	var loginGroup *tool.SearchCodeGroup
	for i := range groups {
		if groups[i].NodeID == "fn:auth.Login" {
			loginGroup = &groups[i]
			break
		}
	}
	if loginGroup == nil {
		t.Fatalf("expected a group for fn:auth.Login; got %+v", groups)
	}
	if loginGroup.MatchCount < 3 {
		t.Errorf("expected fn:auth.Login to dedup ≥3 raw err refs; got matchCount=%d", loginGroup.MatchCount)
	}
}

// TestSearchCodeAcceptance_Bucket_Test pins the bucket logic for the
// test-file path. The fixture has auth/login_test.go, so a pattern
// matching anything in that file must surface a group in the `test`
// bucket — never in `definition`.
func TestSearchCodeAcceptance_Bucket_Test(t *testing.T) {
	repo := loadRepo(t)
	// `Login` is referenced inside login_test.go; the group keyed by
	// the enclosing TestLogin function in that file must land in `test`.
	out := callJSON(t, tool.SearchCode(repo), `{"pattern":"Login","pattern_kind":"regex","limit":50}`)
	env, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("search_code envelope type: got %T", out)
	}
	groups, ok := env["groups"].([]tool.SearchCodeGroup)
	if !ok {
		t.Fatalf("search_code groups slice type: got %T", env["groups"])
	}

	var sawTestBucket bool
	for _, g := range groups {
		if !strings.HasSuffix(g.File, "_test.go") {
			continue
		}
		if g.Bucket != "test" {
			t.Errorf("group %s in %s: bucket=%q, want \"test\"", g.NodeID, g.File, g.Bucket)
		}
		sawTestBucket = true
	}
	if !sawTestBucket {
		t.Fatalf("expected a test-bucket group (Login is referenced in login_test.go); got %+v", groups)
	}
}

// TestSearchCodeAcceptance_Provenance mirrors the issue #10 contract:
// every answer stamps `Provenance.Tool`. Round-trips through JSON so
// the assertion matches what MCP clients actually see on the wire.
func TestSearchCodeAcceptance_Provenance(t *testing.T) {
	repo := loadRepo(t)
	out := callAsMap(t, tool.SearchCode(repo), `{"pattern":"Login","pattern_kind":"regex","limit":10}`)
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("envelope type: got %T", out)
	}
	prov, ok := m["provenance"].(map[string]any)
	if !ok {
		t.Fatalf("provenance type: got %T", m["provenance"])
	}
	if name, _ := prov["tool"].(string); name != "yactt" {
		t.Errorf("provenance.tool = %q, want \"yactt\"", name)
	}
}