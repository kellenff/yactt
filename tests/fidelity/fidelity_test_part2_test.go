package fidelity_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kellenff/yactt/internal/store"
	"github.com/kellenff/yactt/internal/tool"
)

// TestFidelity_Task5_CrossLanguage_PythonParseRegex verifies the canonical
// flow "find every Python function matching parse_*".
//
//	Prompt: Show me every function in this repo named `parse_*`.
//
// The fixture's Python file is written to t.TempDir() at test time so
// no on-disk sample fixture is committed. ponytail: inline tempdir
// fixture is preferred to committing a sample .py file — keeps the
// fixture's shape under test author control.
func TestFidelity_Task5_CrossLanguage_PythonParseRegex(t *testing.T) {
	dir := t.TempDir()
	// Mix in a Go file too, so the search runs over both languages and
	// the regex must hit Python only.
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module sample\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc Parse() {}\n"), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "parse_module.py"), []byte(`"""Module docstring."""

def parse_url(url: str) -> dict:
    return {"url": url}


def parse_query(qs: str) -> dict:
    return {}


def other_helper(x):
    return x
`), 0o644); err != nil {
		t.Fatalf("write parse_module.py: %v", err)
	}

	repo, _, err := store.Load(dir)
	if err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	// Step 1: search_code with regex `^def parse_` — Python-only matches
	// because the pattern anchors on `def `, which Go doesn't have.
	sc := drive(t, tool.SearchCode(reg),
		`{"pattern":"^def parse_","pattern_kind":"regex","scope":"`+dir+`"}`)
	groups, ok := sc["groups"].([]any)
	if !ok {
		t.Fatalf("step 1 search_code: missing 'groups' field; got %v", sc)
	}

	// Final assertion: ≥2 groups surfaced, both containing "parse_"
	// (parse_url, parse_query).
	if len(groups) < 2 {
		t.Fatalf("step 1 search_code: expected ≥2 parse_* functions; got %d groups: %v", len(groups), groups)
	}
	foundNames := map[string]bool{}
	for _, g := range groups {
		if m, ok := g.(map[string]any); ok {
			if summary, _ := m["summary"].(string); strings.Contains(summary, "parse_") {
				if id, _ := m["nodeId"].(string); id != "" {
					foundNames[id] = true
				}
			}
		}
	}
	if len(foundNames) < 2 {
		t.Fatalf("step 1 search_code: expected ≥2 parse_* nodeIds in summaries; got %v", foundNames)
	}
}

// TestFidelity_Task6_Mixed_MultiStepRefactorSummary verifies the canonical
// multi-tool chain "give me the auth package's public surface and its
// callers".
//
//	Prompt: I just refactored the auth package — give me a summary of
//	what the public surface looks like now, and which callers might be
//	affected.
//
// Canonical flow:
//  1. tree_overview(scope=<repo>/auth, depth=3) — orient on the auth
//     package's structure only.
//  2. From the tree, every FUNCTION/METHOD/CLASS node is an auth export
//     (the scope already filtered the rest out).
//  3. For each, find_referencing_symbols — produces a caller set.
//  4. Final assertion: at least one auth export has a non-empty
//     caller set (proving the chain produced a useful summary).
//
// ponytail: the canonical flow uses tree_overview's `scope` arg directly
// (added in #26) so the chain doesn't pay for the rest of the repo. If
// `scope` ever regresses, step 1's tree will still be valid (whole-repo
// fallback) but the chain becomes cheaper-when-scoped → drift to assert.
func TestFidelity_Task6_Mixed_MultiStepRefactorSummary(t *testing.T) {
	repo := loadFixtureRepo(t)

	// Step 1: tree_overview scoped to the auth package.
	scope := repo.Root() + "/auth"
	out := driveRaw(t, tool.TreeOverview(reg),
		fmt.Sprintf(`{"depth":3,"scope":%q}`, scope))
	tree, ok := out.(tool.TreeOverviewResult)
	if !ok {
		t.Fatalf("step 1: unexpected TreeOverview type: %T", out)
	}

	// Step 2: every exported symbol in the scoped tree is an auth export.
	authNames := listExportedNames(tree)
	if len(authNames) == 0 {
		t.Fatalf("step 2: expected ≥1 auth export in scoped tree; got 0 (scope=%q)", scope)
	}

	// Step 3: for each auth export, find_referencing_symbols.
	// Assertion: at least one has a non-empty caller set.
	anyHasCallers := false
	for _, name := range authNames {
		step := drive(t, tool.FindReferencingSymbols(reg), `{"symbol":"`+name+`"}`)
		if refs, ok := step["references"].([]any); ok && len(refs) > 0 {
			anyHasCallers = true
			break
		}
	}
	if !anyHasCallers {
		t.Fatalf("step 3: expected at least one auth export to have callers; got 0 across %d exports", len(authNames))
	}
}