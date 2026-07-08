package tool

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/entity"
	"github.com/kellenff/yactt/internal/store"
	"github.com/kellenff/yactt/internal/store/repofixture"
)

// loadTestRepo loads the repofixture-based repo. Shared by every test in
// this package — keeps the table tests readable.
func loadTestRepo(t *testing.T) *store.Repo {
	t.Helper()
	fx := repofixture.New(t)
	r, _, err := store.Load(fx.Root)
	if err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func runSchema(t *testing.T, repo *store.Repo) *GraphSchemaResult {
	t.Helper()
	out, err := GetGraphSchema(repo)(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	gs, ok := out.(*GraphSchemaResult)
	if !ok {
		t.Fatalf("result type: got %T", out)
	}
	return gs
}

// TestGetGraphSchema_AllListsPopulated asserts that none of the five enum
// slices is empty. Catches "forgot to add the new NodeKind to the slice"
// regressions in one place.
func TestGetGraphSchema_AllListsPopulated(t *testing.T) {
	r := loadTestRepo(t)
	out := runSchema(t, r)

	if len(out.NodeKinds) == 0 {
		t.Error("nodeKinds is empty")
	}
	if len(out.EdgeKinds) == 0 {
		t.Error("edgeKinds is empty")
	}
	if len(out.Layers) == 0 {
		t.Error("layers is empty")
	}
	if len(out.DefaultEdges) == 0 {
		t.Error("defaultEdges is empty")
	}
	if len(out.CodeKinds) == 0 {
		t.Error("codeKinds is empty")
	}
	if out.KindMap == nil {
		t.Error("kindMap is nil")
	}
}

// TestGetGraphSchema_KindMap_CoversAllNodeKinds pins that every
// canonical kind in NodeKinds has an entry in KindMap with a non-empty
// id prefix. Per issue #25: this is the schema entry a model uses to
// discover the cross-layer translations.
func TestGetGraphSchema_KindMap_CoversAllNodeKinds(t *testing.T) {
	r := loadTestRepo(t)
	out := runSchema(t, r)

	if len(out.KindMap) != len(out.NodeKinds) {
		t.Errorf("kindMap len = %d, want %d (one per nodeKind)", len(out.KindMap), len(out.NodeKinds))
	}
	for _, k := range out.NodeKinds {
		m, ok := out.KindMap[k]
		if !ok {
			t.Errorf("kindMap missing entry for %q", k)
			continue
		}
		if m.ID == "" {
			t.Errorf("kindMap[%q].ID is empty", k)
		}
	}
}

// TestGetGraphSchema_KindMap_AlignsWithEntity pins that get_graph_schema's
// kindMap matches the entity package's canonical table byte-for-byte.
// Catches drift between the schema tool's output and the source of truth.
func TestGetGraphSchema_KindMap_AlignsWithEntity(t *testing.T) {
	r := loadTestRepo(t)
	out := runSchema(t, r)

	want := entity.AllMappingsByKind()
	if len(out.KindMap) != len(want) {
		t.Fatalf("kindMap len = %d, want %d", len(out.KindMap), len(want))
	}
	for k, w := range want {
		got, ok := out.KindMap[k]
		if !ok {
			t.Errorf("kindMap missing %q", k)
			continue
		}
		if got.ID != w.ID {
			t.Errorf("kindMap[%q].ID = %q, want %q", k, got.ID, w.ID)
		}
		if len(got.Grammars) != len(w.Grammars) {
			t.Errorf("kindMap[%q].Grammars len = %d, want %d", k, len(got.Grammars), len(w.Grammars))
		}
	}
}

// TestGetGraphSchema_CodeKinds verifies CodeKinds == {k in NodeKinds : IsCode()}.
func TestGetGraphSchema_CodeKinds(t *testing.T) {
	r := loadTestRepo(t)
	out := runSchema(t, r)

	set := map[domain.NodeKind]bool{}
	for _, k := range out.NodeKinds {
		set[k] = true
	}
	for _, k := range out.CodeKinds {
		if !set[k] {
			t.Errorf("codeKind %q not in nodeKinds", k)
		}
		if !k.IsCode() {
			t.Errorf("codeKind %q does not satisfy IsCode()", k)
		}
	}
	for _, k := range out.NodeKinds {
		if k.IsCode() {
			found := false
			for _, ck := range out.CodeKinds {
				if ck == k {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("nodeKind %q satisfies IsCode() but missing from codeKinds", k)
			}
		}
	}
}

// TestGetGraphSchema_DefaultEdgesSubset guards against a future edit that
// adds an edge to the default list without exposing it on the schema.
func TestGetGraphSchema_DefaultEdgesSubset(t *testing.T) {
	r := loadTestRepo(t)
	out := runSchema(t, r)

	all := map[domain.EdgeKind]bool{}
	for _, k := range out.EdgeKinds {
		all[k] = true
	}
	for _, k := range out.DefaultEdges {
		if !all[k] {
			t.Errorf("defaultEdge %q not in edgeKinds", k)
		}
	}
}

// TestGetGraphSchema_Provenance pins that the tool stamps Tool="yactt"
// rather than "tree-sitter" — the answer is from the domain constants, not
// from a parse pass.
func TestGetGraphSchema_Provenance(t *testing.T) {
	r := loadTestRepo(t)
	out := runSchema(t, r)

	if out.Provenance.Tool != "yactt" {
		t.Errorf("provenance.Tool = %q, want \"yactt\"", out.Provenance.Tool)
	}
	if out.Provenance.Version == "" {
		t.Error("provenance.Version is empty")
	}
	if out.Provenance.FetchedAt == "" {
		t.Error("provenance.FetchedAt is empty")
	}
}