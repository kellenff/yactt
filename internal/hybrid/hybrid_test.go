package hybrid

import (
	"context"
	"math"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/kellenff/yactt/internal/chunker"
	"github.com/kellenff/yactt/internal/store"
	"github.com/kellenff/yactt/internal/store/repofixture"
)

// fakeBackend is a deterministic VectorBackend used by the unit
// tests. It doesn't actually score — it returns the corpus order
// prefixed by the query string, so the test can assert on which
// IDs the vector channel surfaced.
type fakeBackend struct {
	ids []string
}

func (f *fakeBackend) Index(ctx context.Context, chunks []chunker.Chunk) (VectorIndex, error) {
	if len(f.ids) > 0 {
		return &fakeIndex{ids: f.ids}, nil
	}
	ids := make([]string, 0, len(chunks))
	for _, c := range chunks {
		ids = append(ids, c.ID)
	}
	return &fakeIndex{ids: ids}, nil
}

type fakeIndex struct{ ids []string }

func (f *fakeIndex) TopK(ctx context.Context, query string, k int) ([]VectorHit, error) {
	if k <= 0 || k > len(f.ids) {
		k = len(f.ids)
	}
	out := make([]VectorHit, k)
	for i := 0; i < k; i++ {
		out[i] = VectorHit{ChunkID: f.ids[i], Score: 1.0 - float64(i)*0.01}
	}
	return out, nil
}

// loadRepo is a tiny helper that mirrors internal/chunker/chunker_test.go's
// pattern: load the repofixture repo with no extra options.
func loadRepo(t *testing.T, path string) (*store.Repo, []error) {
	t.Helper()
	r, errs, err := store.Load(path)
	if err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	return r, errs
}

// TestRRFMerge_Math pins the RRF formula. Three channels each rank
// "alpha" 1, "beta" 2, "gamma" 3. With k=60 the expected scores are:
//
//	alpha: 3 * 1/(60+1) = 3/61   (present in all 3 channels)
//	beta:  2 * 1/(60+2) = 2/62   (present in 2 channels; missing in c3 → 0)
//	gamma: 1 * 1/(60+3) = 1/63   (present in 1 channel; missing in c2, c3 → 0)
//
// The "missing channel contributes 0" choice is the simpler of the
// two RRF variants: the alternative ("missing channel contributes
// 1/(k + len(channel)+1)") keeps a document alive with a tiny
// penalty rank. We picked 0 because it's the canonical Cormack
// formulation and avoids picking an arbitrary penalty constant.
func TestRRFMerge_Math(t *testing.T) {
	mk := func(channel string, ids ...string) []Hit {
		out := make([]Hit, len(ids))
		for i, id := range ids {
			out[i] = Hit{ID: id, Score: float64(len(ids) - i), Channel: channel}
		}
		return out
	}
	got := rrfMerge([][]Hit{
		mk("c1", "alpha", "beta", "gamma"),
		mk("c2", "alpha", "beta"),
		mk("c3", "alpha"),
	}, 60, 10)

	want := map[string]float64{
		"alpha": 3.0 / 61,
		"beta":  2.0 / 62,
		"gamma": 1.0 / 63,
	}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (%v)", len(got), len(want), got)
	}
	// Output must be sorted by descending score.
	for i := 1; i < len(got); i++ {
		if got[i].Score > got[i-1].Score {
			t.Fatalf("not sorted desc: %v", scores(got))
		}
	}
	for _, h := range got {
		w, ok := want[h.ID]
		if !ok {
			t.Errorf("unexpected id %q", h.ID)
			continue
		}
		if !floatEq(h.Score, w, 1e-12) {
			t.Errorf("%s = %v, want %v", h.ID, h.Score, w)
		}
	}
}

// TestRRFMerge_Limit pins the top-K behavior.
func TestRRFMerge_Limit(t *testing.T) {
	mk := func(channel string, ids ...string) []Hit {
		out := make([]Hit, len(ids))
		for i, id := range ids {
			out[i] = Hit{ID: id, Score: float64(len(ids) - i), Channel: channel}
		}
		return out
	}
	got := rrfMerge([][]Hit{
		mk("c1", "a", "b", "c", "d"),
	}, 60, 2)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].ID != "a" || got[1].ID != "b" {
		t.Errorf("got %v, want [a b]", got)
	}
}

// TestTokenize pins the tokenizer's behavior: lowercase, split on
// non-letter/digit/underscore, preserve _ and digits.
func TestTokenize(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"hello world", []string{"hello", "world"}},
		{"Hello, World!", []string{"hello", "world"}},
		{"snake_case AND camelCase", []string{"snake_case", "and", "camelcase"}},
		{"a1 b2 c3", []string{"a1", "b2", "c3"}},
		{"foo\tbar\nbaz", []string{"foo", "bar", "baz"}},
	}
	for _, c := range cases {
		got := tokenize(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("tokenize(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestSplitTerms covers the lenient query tokenizer used by the
// structural channel. Unlike the MCP tool's strict tokenizer, this
// one forgives unbalanced quotes (the orchestrator is a fuse, not
// a gate).
func TestSplitTerms(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"hello world", []string{"hello", "world"}},
		{`"hello world" foo`, []string{"hello world", "foo"}},
		{`unclosed "phrase`, []string{"unclosed", "phrase"}},
		{"", nil},
		{"   spaces   ", []string{"spaces"}},
	}
	for _, c := range cases {
		got := splitTerms(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("splitTerms(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestBM25_PinsScore: two docs, query contains "fox" → only one doc
// matches → it should rank #1. The second doc has "the" in common
// but no "fox"; it scores > 0 (low) but lower than the matching
// doc. The empty doc scores 0 and is dropped.
func TestBM25_PinsScore(t *testing.T) {
	chunks := []chunker.Chunk{
		{ID: "a", Text: "the quick brown fox"},
		{ID: "b", Text: "the lazy dog"},
		{ID: "c", Text: ""},
	}
	hits := bm25Channel("the fox", chunks, 5)
	if len(hits) < 1 {
		t.Fatalf("no hits")
	}
	if hits[0].ID != "a" {
		t.Errorf("top hit = %s, want a (the only doc containing 'fox')", hits[0].ID)
	}
	if hits[0].Score <= 0 {
		t.Errorf("top score = %v, want > 0", hits[0].Score)
	}
	if len(hits) < 2 || hits[1].ID != "b" {
		t.Fatalf("expected a then b, got %v", ids(hits))
	}
	if hits[1].Score >= hits[0].Score {
		t.Errorf("a score %v should beat b score %v", hits[0].Score, hits[1].Score)
	}
	for _, h := range hits {
		if h.ID == "c" {
			t.Errorf("empty chunk c should not score, got %v", hits)
		}
	}
}

// TestBM25_EmptyQuery returns no hits.
func TestBM25_EmptyQuery(t *testing.T) {
	hits := bm25Channel("", []chunker.Chunk{{ID: "a", Text: "anything"}}, 5)
	if len(hits) != 0 {
		t.Errorf("empty query should yield no hits, got %v", hits)
	}
}

// TestBagOfTokens_RoundTrip: encode a chunk + a query that shares
// tokens; assert the score is positive and ranks the matching
// chunk above a non-matching one.
func TestBagOfTokens_RoundTrip(t *testing.T) {
	b := NewBagOfTokens()
	chunks := []chunker.Chunk{
		{ID: "login", Text: "authenticate user with username and password"},
		{ID: "logout", Text: "destroy session and clear tokens from cookie store"},
		{ID: "metrics", Text: "collect runtime metrics from cpu and memory"},
	}
	idx, err := b.Index(context.Background(), chunks)
	if err != nil {
		t.Fatal(err)
	}
	hits, err := idx.TopK(context.Background(), "login password authenticate", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("no hits")
	}
	if hits[0].ChunkID != "login" {
		t.Errorf("top hit = %s, want login", hits[0].ChunkID)
	}
}

// TestRun_AllChannelsEndToEnd pins the happy path on the
// repofixture repo. Three channels, one query that should match
// "Login" in the auth package.
func TestRun_AllChannelsEndToEnd(t *testing.T) {
	fix := repofixture.New(t)
	repo, _ := loadRepo(t, fix.Root)
	defer func() { _ = repo.Close() }()

	got, err := Run(context.Background(), Options{
		Repo:     repo,
		Query:    "login",
		Limit:    5,
		Channels: AllChannels(),
		Vector:   NewBagOfTokens(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatalf("no hits")
	}
	if !strings.Contains(got[0].ID, "Login") && !strings.Contains(got[0].ID, "login") {
		t.Errorf("top hit = %q, want one containing Login/login", got[0].ID)
	}
	if got[0].Channel != "rrf" {
		t.Errorf("merged hit channel = %q, want rrf", got[0].Channel)
	}
}

// TestRun_NilRepo returns an error.
func TestRun_NilRepo(t *testing.T) {
	_, err := Run(context.Background(), Options{Query: "x", Vector: NewBagOfTokens()})
	if err == nil {
		t.Fatal("expected error for nil repo")
	}
}

// TestRun_EmptyQuery returns an error.
func TestRun_EmptyQuery(t *testing.T) {
	_, err := Run(context.Background(), Options{Repo: nil, Query: "", Vector: NewBagOfTokens()})
	if err == nil {
		t.Fatal("expected error for empty query")
	}
}

// TestRun_AllChannelsDisabled returns an error.
func TestRun_AllChannelsDisabled(t *testing.T) {
	fix := repofixture.New(t)
	repo, _ := loadRepo(t, fix.Root)
	defer func() { _ = repo.Close() }()
	_, err := Run(context.Background(), Options{
		Repo:     repo,
		Query:    "x",
		Channels: Channels{},
	})
	if err == nil {
		t.Fatal("expected error for all channels disabled")
	}
}

// TestRun_VectorBackendRequired returns an error when the vector
// channel is on but no backend is supplied.
func TestRun_VectorBackendRequired(t *testing.T) {
	fix := repofixture.New(t)
	repo, _ := loadRepo(t, fix.Root)
	defer func() { _ = repo.Close() }()
	_, err := Run(context.Background(), Options{
		Repo:     repo,
		Query:    "x",
		Channels: Channels{Structural: true, BM25: false, Vector: true},
		Vector:   nil,
	})
	if err == nil {
		t.Fatal("expected error for missing vector backend")
	}
}

// TestExplain_PerChannel verifies the per-channel decomposition.
func TestExplain_PerChannel(t *testing.T) {
	fix := repofixture.New(t)
	repo, _ := loadRepo(t, fix.Root)
	defer func() { _ = repo.Close() }()

	got, err := Explain(context.Background(), Options{
		Repo:     repo,
		Query:    "login",
		Limit:    5,
		Channels: AllChannels(),
		Vector:   NewBagOfTokens(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{ChannelStructural, ChannelBM25, ChannelVector, "rrf"} {
		if _, ok := got[k]; !ok {
			t.Errorf("missing channel %q in explain output", k)
		}
	}
}

// TestRun_OnlyStructural verifies the orchestrator works when only
// the structural channel is enabled.
func TestRun_OnlyStructural(t *testing.T) {
	fix := repofixture.New(t)
	repo, _ := loadRepo(t, fix.Root)
	defer func() { _ = repo.Close() }()
	got, err := Run(context.Background(), Options{
		Repo:     repo,
		Query:    "Login",
		Limit:    3,
		Channels: Channels{Structural: true, BM25: false, Vector: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("no hits")
	}
	if !strings.Contains(got[0].ID, "Login") {
		t.Errorf("top hit = %q, want Login-containing", got[0].ID)
	}
}

// helpers ------------------------------------------------------------

func floatEq(a, b, eps float64) bool { return math.Abs(a-b) < eps }

func scores(h []Hit) []float64 {
	out := make([]float64, len(h))
	for i, x := range h {
		out[i] = x.Score
	}
	return out
}

func ids(h []Hit) []string {
	out := make([]string, len(h))
	for i, x := range h {
		out[i] = x.ID
	}
	sort.Strings(out)
	return out
}