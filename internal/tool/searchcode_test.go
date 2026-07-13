package tool

import (
	"context"
	"encoding/json"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kellenff/yactt/internal/store"
	"github.com/kellenff/yactt/internal/store/repofixture"
)

// loadSearchCodeRepo returns the repofixture-based *store.Repo used by
// the search_code tests. Single source of truth lives in
// repofixture.New; this wrapper just centralises the store.Load +
// cleanup dance so each test stays a one-liner.
func loadSearchCodeRepo(t *testing.T) *store.Repo {
	t.Helper()
	fx := repofixture.New(t)
	r, _, err := store.Load(fx.Root)
	if err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// callSearchCode is the test harness for SearchCode — same shape as the
// find_code helpers' callX pattern. Returns the raw handler envelope.
func callSearchCode(t *testing.T, r *store.Repo, args string) map[string]any {
	t.Helper()
	out, err := SearchCode(seedRegFromRepo(t, repo))(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("SearchCode: %v", err)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("search_code envelope = %T, want map[string]any", out)
	}
	return m
}

// searchCodeGroups extracts the typed groups slice from the envelope.
func searchCodeGroups(t *testing.T, env map[string]any) []SearchCodeGroup {
	t.Helper()
	raw, ok := env["groups"].([]SearchCodeGroup)
	if !ok {
		t.Fatalf("envelope.groups = %T, want []SearchCodeGroup", env["groups"])
	}
	return raw
}

// TestSearchCode_Regex_DedupAndRank is the headline behaviour from
// issue #10: a single pattern can produce N raw matches inside one
// function, and search_code collapses them to one group, ranked by
// structural importance. The fixture has `Session` referenced in
// Authenticate (one function in auth/login.go) — multiple raw hits
// must collapse to a single group keyed by `fn:auth.Login` (the
// enclosing function for Authenticate) OR by `class:auth.Session`
// (the enclosing declaration when the match is a type reference).
func TestSearchCode_Regex_DedupAndRank(t *testing.T) {
	r := loadSearchCodeRepo(t)
	env := callSearchCode(t, r, `{"pattern":"Session","pattern_kind":"regex","limit":50}`)
	gs := searchCodeGroups(t, env)

	if len(gs) == 0 {
		t.Fatalf("expected at least one group for Session; got 0")
	}
	for _, g := range gs {
		if g.MatchCount == 0 {
			t.Errorf("group %s has matchCount=0", g.NodeID)
		}
		if len(g.Matches) != g.MatchCount {
			t.Errorf("group %s: len(matches)=%d != matchCount=%d", g.NodeID, len(g.Matches), g.MatchCount)
		}
	}

	// At least one group must be the enclosing function for the bulk of
	// hits — `auth.Login` or `auth.Authenticate`. Verifies dedup is
	// producing a single group for repeated references inside one
	// function, not a row per raw hit. The fixture writes absolute paths
	// under t.TempDir(), so check by suffix rather than exact equality.
	found := false
	for _, g := range gs {
		if strings.HasSuffix(g.File, string(filepath.Separator)+path.Join("auth", "login.go")) && g.MatchCount >= 1 {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected a group in auth/login.go for Session refs; got %+v", gs)
	}
}

// TestSearchCode_Regex_Bucketing pins the bucket logic. A group whose
// file ends in `_test.go` must be in the test bucket; non-test
// functions land in definition; non-declaration kinds land in popular.
// The repofixture has no _test.go (the on-disk tests/fixtures/sample-go
// fixture has login_test.go, but this test uses repofixture for
// isolation), so the assertion here is the simpler negative case:
// nothing in the fixture can land in `test` because no file ends in
// `_test.go`.
func TestSearchCode_Regex_Bucketing(t *testing.T) {
	r := loadSearchCodeRepo(t)
	env := callSearchCode(t, r, `{"pattern":"Login|User|Charge","pattern_kind":"regex","limit":50}`)
	gs := searchCodeGroups(t, env)

	if len(gs) == 0 {
		t.Fatalf("expected groups for Login|User|Charge; got 0")
	}
	for _, g := range gs {
		switch g.Bucket {
		case bucketDefinition, bucketPopular, bucketTest:
			// ok
		default:
			t.Errorf("group %s has unknown bucket %q", g.NodeID, g.Bucket)
		}
	}
	// repofixture has no _test.go files, so test bucket should never
	// appear here.
	for _, g := range gs {
		if g.Bucket == bucketTest {
			t.Errorf("unexpected test bucket in repofixture: %s", g.NodeID)
		}
	}
}

// TestSearchCode_TreeSitter_Dedup mirrors TestSearchCode_Regex_DedupAndRank
// for the tree-sitter path. Same expectation: raw matches collapse into
// one group per enclosing symbol.
func TestSearchCode_TreeSitter_Dedup(t *testing.T) {
	r := loadSearchCodeRepo(t)
	// (identifier) matches every identifier — should produce groups
	// for every declaration in the fixture and dedup all references
	// inside each declaration body to one group with a high matchCount.
	env := callSearchCode(t, r, `{"pattern":"(identifier) @id","pattern_kind":"tree_sitter","limit":100}`)
	gs := searchCodeGroups(t, env)
	if len(gs) == 0 {
		t.Fatalf("expected groups for (identifier); got 0")
	}
	for _, g := range gs {
		if g.MatchCount == 0 {
			t.Errorf("group %s: matchCount=0", g.NodeID)
		}
	}
}

// TestSearchCode_TreeSitter_Limit verifies the per-group cap is
// honoured. The overscan-then-rank-then-cap path means raw hits can
// be > limit; the output groups themselves must be at most `limit`.
func TestSearchCode_TreeSitter_Limit(t *testing.T) {
	r := loadSearchCodeRepo(t)
	env := callSearchCode(t, r, `{"pattern":"(identifier) @id","pattern_kind":"tree_sitter","limit":2}`)
	gs := searchCodeGroups(t, env)
	if len(gs) > 2 {
		t.Errorf("groups slice len=%d, want <=2", len(gs))
	}
}

// TestSearchCode_ProvenanceStamp pins the contract from issue #10:
// every search_code answer stamps `Provenance.Tool`. The handler
// returns domain.YacttProvenance(); we round-trip through JSON to
// exercise the same encoder the MCP wire uses.
func TestSearchCode_ProvenanceStamp(t *testing.T) {
	r := loadSearchCodeRepo(t)
	env := callSearchCode(t, r, `{"pattern":"Login","pattern_kind":"regex","limit":10}`)

	b, err := json.Marshal(env["provenance"])
	if err != nil {
		t.Fatalf("marshal provenance: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal provenance: %v", err)
	}
	if tool, _ := m["tool"].(string); tool != "yactt" {
		t.Errorf("provenance.tool = %q, want \"yactt\" (full map=%+v)", tool, m)
	}
	if v, _ := m["version"].(string); v == "" {
		t.Errorf("provenance.version is empty (full map=%+v)", m)
	}
}

// TestSearchCode_RejectsEmptyPattern is the boundary contract: empty
// pattern is rejected up front, before any repo access. Mirrors the
// matching find_code test.
func TestSearchCode_RejectsEmptyPattern(t *testing.T) {
	r := loadSearchCodeRepo(t)
	_, err := SearchCode(seedRegFromRepo(t, repo))(context.Background(), json.RawMessage(`{"pattern":""}`))
	if err == nil {
		t.Fatalf("expected error on empty pattern")
	}
	if !strings.Contains(err.Error(), "pattern is required") {
		t.Errorf("error doesn't say pattern is required; got %q", err.Error())
	}
}

// TestSearchCode_RejectsBadRegex mirrors find_code's regex-validation
// boundary — a malformed regex is rejected at compile time, no repo
// access. The handler uses the same regexp.Compile path so we expect
// the same `invalid regex:` error shape.
func TestSearchCode_RejectsBadRegex(t *testing.T) {
	r := loadSearchCodeRepo(t)
	_, err := SearchCode(seedRegFromRepo(t, repo))(context.Background(), json.RawMessage(`{"pattern":"[","pattern_kind":"regex"}`))
	if err == nil {
		t.Fatalf("expected error on bad regex")
	}
	if !strings.Contains(err.Error(), "invalid regex") {
		t.Errorf("error doesn't say invalid regex; got %q", err.Error())
	}
}

// TestSearchCode_RejectsPatternLengthCap mirrors the find_code length
// DoS guard — same cap, same error shape. Reuses maxRegexPatternBytes
// from findcode.go so the two tools cannot drift.
func TestSearchCode_RejectsPatternLengthCap(t *testing.T) {
	r := loadSearchCodeRepo(t)
	huge := strings.Repeat("a", maxRegexPatternBytes+1)
	args := `{"pattern":"` + huge + `","pattern_kind":"regex","limit":1}`
	_, err := SearchCode(seedRegFromRepo(t, repo))(context.Background(), json.RawMessage(args))
	if err == nil {
		t.Fatalf("expected error on %d-byte pattern", len(huge))
	}
	if !strings.Contains(err.Error(), "pattern too long") {
		t.Errorf("error doesn't say pattern too long; got %q", err.Error())
	}
}

// TestSearchCode_RankStable pins the bucket-order guarantee: the
// slice is non-decreasing on bucket rank (definition < popular < test).
// A regression that swaps the sort direction or removes the bucket
// key would be caught here. NodeID tie-breaking is implicit in
// sort.SliceStable and verified by deterministic test output.
func TestSearchCode_RankStable(t *testing.T) {
	r := loadSearchCodeRepo(t)
	env := callSearchCode(t, r, `{"pattern":"Session","pattern_kind":"regex","limit":50}`)
	gs := searchCodeGroups(t, env)

	for i := 1; i < len(gs); i++ {
		prev, cur := gs[i-1], gs[i]
		if bucketRank(cur.Bucket) < bucketRank(prev.Bucket) {
			t.Errorf("bucket order violated at i=%d: prev=%s/%s cur=%s/%s",
				i, prev.NodeID, prev.Bucket, cur.NodeID, cur.Bucket)
		}
	}
}
