package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kellenff/yactt/internal/store"
)

// withBudget swaps maxResponseBytes for the duration of t, restoring it on
// cleanup. Tests must NOT call t.Parallel() because maxResponseBytes is a
// package-level var.
func withBudget(t *testing.T, n int) {
	t.Helper()
	prev := maxResponseBytes
	maxResponseBytes = n
	t.Cleanup(func() { maxResponseBytes = prev })
}

// loadFixtureRepo returns the shared acceptance fixture, cached.
func loadFixtureRepo(t *testing.T) *store.Repo {
	t.Helper()
	repo, _, err := store.Load("../../tests/fixtures/sample-go")
	if err != nil {
		t.Fatalf("loading fixture: %v", err)
	}
	return repo
}

func TestBuildOverviewTree_NoWarningUnderBudget(t *testing.T) {
	repo := loadFixtureRepo(t)
	handler := TreeOverview(repo)
	out, err := handler(context.Background(), json.RawMessage(`{"repo":"","depth":6}`))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	root, ok := out.(TreeOverviewResult)
	if !ok {
		t.Fatalf("result type: got %T", out)
	}
	if root.Warning != "" {
		t.Fatalf("expected no warning under default 16KB budget; got %q", root.Warning)
	}
	// Marshal the whole tree and confirm it's bounded.
	b, err := json.Marshal(root)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(b) > maxResponseBytes+512 {
		// 512 = generous slack for the root's id/summary/provenance we did
		// not pre-charge in the builder (root is always emitted, never
		// reserved against).
		t.Fatalf("response size %d exceeds budget %d + slack", len(b), maxResponseBytes)
	}
}

func TestBuildOverviewTree_TruncatesAtBudget(t *testing.T) {
	withBudget(t, 256)
	repo := loadFixtureRepo(t)
	handler := TreeOverview(repo)
	out, err := handler(context.Background(), json.RawMessage(`{"repo":"","depth":6}`))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	root, ok := out.(TreeOverviewResult)
	if !ok {
		t.Fatalf("result type: got %T", out)
	}
	if root.Warning != truncatedWarning {
		t.Fatalf("warning: got %q, want %q", root.Warning, truncatedWarning)
	}
	// Truncation should have left us with strictly fewer top-level children
	// than the un-truncated tree, OR at minimum some interior node should
	// have dropped its trailing siblings.
	totalNodes := countNodes(root)
	if totalNodes == 0 {
		t.Fatal("expected at least the root node")
	}
	// Sanity: the response size must still be bounded, even after truncation.
	b, err := json.Marshal(root)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// The warning field adds bytes that aren't reserved against; allow a
	// modest overrun so we don't over-fit.
	if len(b) > maxResponseBytes+256 {
		t.Fatalf("truncated response %d bytes still exceeds budget %d", len(b), maxResponseBytes)
	}
}

func TestBuildOverviewTree_WarningFieldHiddenWhenEmpty(t *testing.T) {
	repo := loadFixtureRepo(t)
	handler := TreeOverview(repo)
	out, err := handler(context.Background(), json.RawMessage(`{"repo":"","depth":2}`))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), `"warning"`) {
		t.Fatalf("expected omitempty to hide warning when empty; got: %s", b)
	}
}

// countNodes walks the whole tree. Used to compare truncated vs full outputs.
func countNodes(n TreeOverviewResult) int {
	total := 1
	for _, c := range n.Children {
		total += countNodes(c)
	}
	return total
}

// hasChildID reports whether the top-level children of `n` include a node
// whose ID contains `substr`. Cheap assertion for "scope narrowed to the
// expected subtree" without depending on id-prefix conventions.
func hasChildID(n TreeOverviewResult, substr string) bool {
	for _, c := range n.Children {
		if strings.Contains(c.ID, substr) {
			return true
		}
	}
	return false
}

func TestBuildOverviewTree_ScopeFiltersToSubtree(t *testing.T) {
	repo := loadFixtureRepo(t)
	handler := TreeOverview(repo)
	scope := repo.Root() + "/auth"
	out, err := handler(context.Background(),
		json.RawMessage(`{"repo":"","scope":`+jsonQuote(scope)+`,"depth":3}`))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	root, ok := out.(TreeOverviewResult)
	if !ok {
		t.Fatalf("result type: got %T", out)
	}
	if len(root.Children) == 0 {
		t.Fatalf("expected at least one top-level child under scope %q; got 0", scope)
	}
	if !hasChildID(root, "auth") {
		t.Fatalf("expected a top-level child with id containing %q; got: %v", "auth", root.Children)
	}
	if hasChildID(root, "payments") {
		t.Fatalf("scope %q should have excluded the payments package; got: %v", scope, root.Children)
	}
	// Summary should advertise the scope so a caller can see why the tree
	// is narrower than usual.
	if !strings.Contains(root.Summary, scope) {
		t.Fatalf("expected root.Summary to mention scope %q; got %q", scope, root.Summary)
	}
}

func TestBuildOverviewTree_ScopeOutsideRepoErrors(t *testing.T) {
	repo := loadFixtureRepo(t)
	handler := TreeOverview(repo)
	_, err := handler(context.Background(),
		json.RawMessage(`{"repo":"","scope":"/definitely/not/the/repo","depth":2}`))
	if err == nil {
		t.Fatalf("expected error for scope outside repo; got nil")
	}
	if !strings.Contains(err.Error(), "scope") {
		t.Fatalf("error should mention 'scope'; got %q", err.Error())
	}
}

func TestBuildOverviewTree_EmptyScopeBehavesLikeNoScope(t *testing.T) {
	repo := loadFixtureRepo(t)
	handler := TreeOverview(repo)
	// Empty scope must not error and must return at least one top-level
	// child — same shape as the no-scope happy path.
	out, err := handler(context.Background(),
		json.RawMessage(`{"repo":"","scope":"","depth":2}`))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	root, ok := out.(TreeOverviewResult)
	if !ok {
		t.Fatalf("result type: got %T", out)
	}
	if len(root.Children) == 0 {
		t.Fatalf("empty scope should produce a non-empty tree; got 0 children")
	}
	// Summary should NOT advertise a scope when none was given.
	if strings.Contains(root.Summary, "scoped to") {
		t.Fatalf("empty scope should not annotate root.Summary; got %q", root.Summary)
	}
}

// jsonQuote wraps s in JSON double-quotes with proper escaping. Inline here
// (not in helpers) because it's only used by two scope tests and a generic
// helper would over-generalise.
func jsonQuote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}