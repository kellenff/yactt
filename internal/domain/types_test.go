package domain_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/kellenff/yactt/internal/domain"
)

func TestLineRangeContains(t *testing.T) {
	r := domain.LineRange{Start: 2, End: 5}
	cases := []struct {
		line int
		want bool
	}{
		{1, false}, // below start
		{2, true},  // inclusive lower
		{3, true},
		{4, true},
		{5, false}, // exclusive upper
		{6, false},
	}
	for _, tc := range cases {
		if got := r.Contains(tc.line); got != tc.want {
			t.Errorf("Contains(%d) = %v, want %v", tc.line, got, tc.want)
		}
	}
}

func TestLineRangeContainsEmpty(t *testing.T) {
	r := domain.LineRange{Start: 3, End: 3}
	for _, line := range []int{0, 2, 3, 4, 100} {
		if r.Contains(line) {
			t.Errorf("empty range should not contain %d", line)
		}
	}
}

func TestLineRangeLength(t *testing.T) {
	cases := []struct {
		name string
		r    domain.LineRange
		want int
	}{
		{"normal", domain.LineRange{Start: 0, End: 10}, 10},
		{"nonzero start", domain.LineRange{Start: 3, End: 7}, 4},
		{"empty", domain.LineRange{Start: 5, End: 5}, 0},
		{"inverted", domain.LineRange{Start: 7, End: 3}, 0},
		{"negative inverted", domain.LineRange{Start: 5, End: -1}, 0},
	}
	for _, tc := range cases {
		if got := tc.r.Length(); got != tc.want {
			t.Errorf("%s: Length() = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestProvenanceNew(t *testing.T) {
	// RFC3339 has second precision; capture before/after at the same scale so
	// the inclusive range comparison is stable on fast machines.
	before := time.Now().UTC().Truncate(time.Second)
	p := domain.NewProvenance("lsp", "v1.2.3")
	after := time.Now().UTC()

	if p.Tool != "lsp" {
		t.Errorf("Tool = %q, want %q", p.Tool, "lsp")
	}
	if p.Version != "v1.2.3" {
		t.Errorf("Version = %q, want %q", p.Version, "v1.2.3")
	}
	ts, err := time.Parse(time.RFC3339, p.FetchedAt)
	if err != nil {
		t.Fatalf("FetchedAt %q is not RFC3339: %v", p.FetchedAt, err)
	}
	if ts.Location() != time.UTC {
		t.Errorf("FetchedAt location = %v, want UTC", ts.Location())
	}
	if ts.Before(before) || ts.After(after) {
		t.Errorf("FetchedAt %v not in [%v, %v]", ts, before, after)
	}
	if p.FallbackUsed != "" {
		t.Errorf("FallbackUsed = %q, want empty", p.FallbackUsed)
	}
}

func TestProvenanceWithFallback(t *testing.T) {
	p := domain.NewProvenance("lsp", "v1")
	p2 := p.WithFallback("lsp-unavailable")
	if p2.FallbackUsed != "lsp-unavailable" {
		t.Errorf("FallbackUsed = %q, want %q", p2.FallbackUsed, "lsp-unavailable")
	}
	// Original must be unchanged.
	if p.FallbackUsed != "" {
		t.Errorf("original was mutated: %q", p.FallbackUsed)
	}
	// And other fields preserved.
	if p2.Tool != "lsp" || p2.Version != "v1" {
		t.Errorf("WithFallback mutated other fields: %+v", p2)
	}
}

func TestProvenancePtr(t *testing.T) {
	p := domain.NewProvenance("lsp", "v1")
	ptr := p.Ptr()
	if ptr == nil {
		t.Fatal("Ptr() returned nil")
	}
	if !reflect.DeepEqual(*ptr, p) {
		t.Errorf("Ptr round-trip differs: %+v vs %+v", *ptr, p)
	}
}

func TestNodeKindIsCode(t *testing.T) {
	code := []domain.NodeKind{domain.KindFunction, domain.KindMethod, domain.KindClass, domain.KindModule}
	nonCode := []domain.NodeKind{domain.KindRepo, domain.KindPackage, domain.KindFile, ""}
	for _, k := range code {
		if !k.IsCode() {
			t.Errorf("%q.IsCode() = false, want true", k)
		}
	}
	for _, k := range nonCode {
		if k.IsCode() {
			t.Errorf("%q.IsCode() = true, want false", k)
		}
	}
}

func TestAllEdgesContains(t *testing.T) {
	want := map[domain.EdgeKind]bool{
		domain.EdgeCallers: false,
		domain.EdgeCallees: false,
		domain.EdgeTests:   false,
	}
	for _, e := range domain.AllEdges {
		if _, ok := want[e]; !ok {
			t.Errorf("AllEdges contains unexpected kind %q", e)
		}
		want[e] = true
	}
	for k, seen := range want {
		if !seen {
			t.Errorf("AllEdges missing %q", k)
		}
	}
}

func TestAllLayerNames(t *testing.T) {
	want := []domain.LayerName{
		domain.LayerSummary,
		domain.LayerSignature,
		domain.LayerBody,
		domain.LayerSource,
		domain.LayerTokens,
	}
	if !reflect.DeepEqual(domain.AllLayerNames, want) {
		t.Errorf("AllLayerNames = %v, want %v", domain.AllLayerNames, want)
	}
}

func TestTreeSitterProvenanceDefaults(t *testing.T) {
	p := domain.TreeSitterProvenance()
	if p.Tool != "tree-sitter" {
		t.Errorf("Tool = %q, want %q", p.Tool, "tree-sitter")
	}
	if p.Version == "" {
		t.Error("Version should be set")
	}

	ptr := domain.TreeSitterProvenancePtr()
	if ptr == nil {
		t.Fatal("TreeSitterProvenancePtr returned nil")
	}
	if ptr.FallbackUsed == "" {
		t.Error("Ptr form should mark fallback")
	}
}
