// Package acceptance exercises the 10 MCP tools end-to-end against a small
// fixture Go project at ../fixtures/sample-go.
//
// Acceptance tests live separately from contract tests because they:
//
//   - load a real repo (slower — 10s of ms vs. µs),
//   - assert domain-level facts about outputs (e.g. "tree_overview emits a
//     user node with the fixture's functions"),
//   - catch correctness regressions that contract tests can't reach.
//
// Naming convention: each Test is named after a tool, with subtests for the
// scenarios it covers.
package acceptance_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/search"
	"github.com/kellenff/yactt/internal/store"
	"github.com/kellenff/yactt/internal/tool"
)

// fixtureRepo is the per-test repo handle. Cached across tests for speed.
var fixtureRepoCache *store.Repo

func loadRepo(t *testing.T) *store.Repo {
	t.Helper()
	if fixtureRepoCache != nil {
		return fixtureRepoCache
	}
	repo, errs, err := store.Load("../fixtures/sample-go")
	if err != nil {
		t.Fatalf("loading fixture: %v", err)
	}
	if len(errs) > 0 {
		t.Logf("load reported %d file errors: %v", len(errs), errs)
	}
	fixtureRepoCache = repo
	return repo
}

// call runs the handler with `argsJSON` and returns the raw result. Concrete
// type varies per tool; tests either direct-cast or JSON-round-trip through
// callAsMap for structural assertions.
func callJSON(t *testing.T, h func(ctx context.Context, args json.RawMessage) (any, error), argsJSON string) any {
	t.Helper()
	out, err := h(context.Background(), json.RawMessage(argsJSON))
	if err != nil {
		t.Fatalf("handler error: %v (args=%s)", err, argsJSON)
	}
	return out
}

// callAsMap runs the handler and JSON-round-trips the result. This normalises
// all types to map[string]any/slice/primitive shapes — convenient for tests
// that don't care about the concrete result type, only its contents.
func callAsMap(t *testing.T, h func(ctx context.Context, args json.RawMessage) (any, error), argsJSON string) any {
	t.Helper()
	out, err := h(context.Background(), json.RawMessage(argsJSON))
	if err != nil {
		t.Fatalf("handler error: %v (args=%s)", err, argsJSON)
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var asMap any
	if err := json.Unmarshal(b, &asMap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return asMap
}

// treeHelper walks a tree_overview result and returns the count of leaves.
func treeLeaves(tree any) int {
	m, ok := tree.(tool.TreeOverviewResult)
	if !ok {
		return 0
	}
	count := 0
	for _, c := range m.Children {
		count += treeLeaves(c)
	}
	count++ // this node itself
	return count
}

func TestTreeOverviewAcceptance(t *testing.T) {
	repo := loadRepo(t)
	out := callJSON(t, tool.TreeOverview(repo), `{"repo":"","depth":2}`)
	root, ok := out.(tool.TreeOverviewResult)
	if !ok {
		t.Fatalf("root type: got %T", out)
	}
	if root.ID == "" || root.Kind != domain.KindRepo {
		t.Fatalf("root: id=%q kind=%v", root.ID, root.Kind)
	}
	if treeLeaves(root) == 0 {
		t.Fatalf("root has no descendants; tree=%+v", root)
	}
	// Find a package node whose summary mentions `auth`.
	var found bool
	for _, p := range root.Children {
		if strings.Contains(p.Summary, "auth") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected an auth package in tree, got %d children", len(root.Children))
	}
}

func TestNodeGetAcceptance(t *testing.T) {
	repo := loadRepo(t)
	out := callJSON(t, tool.GetNode(repo), `{"id":"fn:auth.Login","layers":["summary","signature","body"]}`)
	n, ok := out.(*domain.Node)
	if !ok {
		t.Fatalf("node type: got %T", out)
	}
	if n.Summary == "" {
		t.Fatal("expected non-empty summary")
	}
	if n.Signature == nil || n.Signature.Text == "" {
		t.Fatalf("expected signature text; got %+v", n.Signature)
	}
	if n.Body == nil || len(n.Body.Stmts) == 0 {
		t.Fatalf("expected non-empty body")
	}
	if !strings.Contains(strings.ToLower(n.Summary), "authenticates") {
		t.Fatalf("summary should reflect doc-comment, got %q", n.Summary)
	}
}

func TestNodeSourceAcceptance(t *testing.T) {
	repo := loadRepo(t)
	out := callJSON(t, tool.NodeSource(repo), `{"id":"fn:auth.ValidateToken"}`)
	r, ok := out.(*tool.NodeSourceResult)
	if !ok {
		t.Fatalf("source type: got %T", out)
	}
	if r.Text == "" {
		t.Fatal("expected non-empty source")
	}
	if !strings.Contains(r.Text, "func ValidateToken") {
		t.Fatalf("expected function header in source, got %q", r.Text)
	}
}

func TestSearchAcceptance(t *testing.T) {
	repo := loadRepo(t)
	out := callJSON(t, tool.Search(repo), `{"query":"ValidateToken","scope":""}`)
	hits, ok := out.([]search.Result)
	if !ok {
		t.Fatalf("search result type: got %T", out)
	}
	if len(hits) == 0 {
		t.Fatal("expected at least one search hit")
	}
	if hits[0].Node.ID != "fn:auth.ValidateToken" {
		t.Fatalf("expected first hit to be ValidateToken, got %s", hits[0].Node.ID)
	}
}

func TestGetSymbolsOverviewAcceptance(t *testing.T) {
	repo := loadRepo(t)
	out := callAsMap(t, tool.GetSymbolsOverview(repo), `{"file":"auth/login.go","depth":1}`)
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("overview type: got %T", out)
	}
	syms, ok := m["symbols"].([]any)
	if !ok {
		t.Fatalf("symbols slice type: got %T", m["symbols"])
	}
	if len(syms) < 3 {
		t.Fatalf("expected at least 3 symbols in login.go, got %d", len(syms))
	}
	want := map[string]bool{"Login": false, "ValidateToken": false, "SetSession": false}
	for _, s := range syms {
		n, _ := s.(map[string]any)
		if name, _ := n["name"].(string); name != "" {
			if _, ok := want[name]; ok {
				want[name] = true
			}
		}
	}
	for name, found := range want {
		if !found {
			t.Fatalf("expected symbol %q in login.go outline", name)
		}
	}
}

func TestFindCodeAcceptance(t *testing.T) {
	repo := loadRepo(t)
	out := callJSON(t, tool.FindCode(repo), `{"pattern":"ValidateToken","pattern_kind":"regex","limit":20}`)
	hits, ok := out.([]tool.FindCodeMatch)
	if !ok {
		t.Fatalf("find_code type: got %T", out)
	}
	if len(hits) == 0 {
		t.Fatal("expected hits for ValidateToken")
	}
}

func TestFindSymbolAcceptance(t *testing.T) {
	repo := loadRepo(t)
	out := callJSON(t, tool.FindSymbol(repo), `{"name_path":"auth/Login*","limit":10,"include_body":true}`)
	hits, ok := out.([]tool.FindSymbolResult)
	if !ok {
		t.Fatalf("find_symbol type: got %T", out)
	}
	if len(hits) == 0 {
		t.Fatal("expected at least one match for auth/Login*")
	}
}

func TestFindReferencingSymbolsAcceptance(t *testing.T) {
	repo := loadRepo(t)
	out := callAsMap(t, tool.FindReferencingSymbols(repo), `{"symbol":"fn:auth.ValidateToken","kinds":["tests","calls"]}`)
	edges, ok := out.([]any)
	if !ok {
		t.Fatalf("find_referencing_symbols type: got %T", out)
	}
	if len(edges) == 0 {
		t.Fatal("expected at least one edge (tests or callers)")
	}
}

func TestEditImpactAcceptance(t *testing.T) {
	repo := loadRepo(t)
	out := callAsMap(t, tool.EditImpact(repo), `{"renames":[{"id":"fn:auth.SetSession","new_name":"OpenSession"}]}`)
	r, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("edit_impact type: got %T", out)
	}
	renames, ok := r["renames"].([]any)
	if !ok || len(renames) != 1 {
		t.Fatalf("expected one rename analysis, got %d", len(renames))
	}
	first := renames[0].(map[string]any)
	if first["safeToRename"] != true {
		t.Fatalf("expected safe_to_rename=true, got %+v", first)
	}
}

func TestNodeEdgesAcceptance(t *testing.T) {
	repo := loadRepo(t)
	out := callAsMap(t, tool.NodeEdges(repo), `{"id":"fn:auth.Login","kinds":["callees"],"limit":10}`)
	edges, ok := out.([]any)
	if !ok {
		t.Fatalf("node_edges type: got %T", out)
	}
	if len(edges) == 0 {
		t.Fatal("expected callees for Login")
	}
}
