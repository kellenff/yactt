package fidelity_test

import (
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
	sc := drive(t, tool.SearchCode(repo),
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
//   1. tree_overview(depth=2) — orient on the repo's structure.
//   2. From the tree, extract every FUNCTION/METHOD/CLASS node whose
//      ID begins with `auth.` (the auth package's surface).
//   3. For each, find_referencing_symbols — produces a caller set.
//   4. Final assertion: at least one auth export has a non-empty
//      caller set (proving the chain produced a useful summary).
//
// ponytail: tree_overview has no `scope` arg (it always roots at
// repo.Root), so step 1 doesn't constrain to auth — the chain walks
// the full tree and filters client-side. This mirrors how a real
// harness without scope support would solve the prompt.
func TestFidelity_Task6_Mixed_MultiStepRefactorSummary(t *testing.T) {
	repo := loadFixtureRepo(t)

	// Step 1: tree_overview — get the full tree.
	out := driveRaw(t, tool.TreeOverview(repo), `{"depth":3}`)
	tree, ok := out.(tool.TreeOverviewResult)
	if !ok {
		t.Fatalf("step 1: unexpected TreeOverview type: %T", out)
	}

	// Step 2: extract the auth-package surface from the tree.
	authNames := []string{}
	for _, name := range listExportedNames(tree) {
		if strings.HasPrefix(name, "auth.") || strings.HasPrefix(name, "fn:auth.") || strings.HasPrefix(name, "meth:auth.") || strings.HasPrefix(name, "class:auth.") {
			authNames = append(authNames, name)
		}
	}
	if len(authNames) == 0 {
		t.Fatalf("step 2: expected ≥1 auth export in tree_overview; got 0")
	}

	// Step 3: for each auth export, find_referencing_symbols.
	// Assertion: at least one has a non-empty caller set.
	anyHasCallers := false
	for _, name := range authNames {
		step := drive(t, tool.FindReferencingSymbols(repo), `{"symbol":"`+name+`"}`)
		if refs, ok := step["references"].([]any); ok && len(refs) > 0 {
			anyHasCallers = true
			break
		}
	}
	if !anyHasCallers {
		t.Fatalf("step 3: expected at least one auth export to have callers; got 0 across %d exports", len(authNames))
	}
}