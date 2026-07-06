package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kellenff/yactt/internal/store"
)

// TestGetArchitecture_HappyPath checks the envelope shape and that counts
// are non-zero on the repofixture (which has files, packages, functions,
// methods, classes).
func TestGetArchitecture_HappyPath(t *testing.T) {
	r := loadTestRepo(t)
	out := callArch(t, r, `{}`)

	if out.Summary.FileCount == 0 {
		t.Error("summary.fileCount is zero")
	}
	if out.Summary.FunctionCount == 0 {
		t.Error("summary.functionCount is zero")
	}
	if out.Summary.PackageCount == 0 {
		t.Error("summary.packageCount is zero")
	}
	if len(out.Languages) == 0 {
		t.Error("languages is empty")
	}
	// Spot-check: Go should be among the detected languages.
	hasGo := false
	for _, l := range out.Languages {
		if l.Name == "go" {
			hasGo = true
		}
	}
	if !hasGo {
		t.Errorf("expected 'go' in languages; got %+v", out.Languages)
	}
	if len(out.TopPackages) == 0 {
		t.Error("topPackages is empty")
	}
	if out.Provenance.Tool == "" {
		t.Error("provenance.tool is empty")
	}
}

// TestGetArchitecture_NoCyclesOnFixture confirms the fixture has no
// import cycles. The Tarjan SCC pass must report zero cycles here.
func TestGetArchitecture_NoCyclesOnFixture(t *testing.T) {
	r := loadTestRepo(t)
	out := callArch(t, r, `{}`)

	if len(out.ImportCycles) != 0 {
		t.Errorf("fixture has no cycles; got %d: %+v", len(out.ImportCycles), out.ImportCycles)
	}
}

// TestGetArchitecture_DetectsCycle writes a tiny repo on disk where two
// files in different packages import each other (a cycle at the package
// level). Loads it, runs the architecture tool, asserts the cycle is
// surfaced.
func TestGetArchitecture_DetectsCycle(t *testing.T) {
	dir := t.TempDir()

	// package a — file a/a.go imports "b"
	aSrc := `package a

import "b"

func Hello() { Greet() }
`
	// package b — file b/b.go imports "a"
	bSrc := `package b

import "a"

func Greet() { Hello() }
`
	if err := os.MkdirAll(filepath.Join(dir, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a", "a.go"), []byte(aSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b", "b.go"), []byte(bSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	r, _, err := store.Load(dir)
	if err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	defer func() { _ = r.Close() }()

	out := callArch(t, r, `{}`)

	if len(out.ImportCycles) == 0 {
		t.Fatal("expected at least one import cycle; got none")
	}
	// The cycle should mention both files.
	gotBoth := false
	for _, c := range out.ImportCycles {
		hasA, hasB := false, false
		for _, f := range c.Files {
			if strings.HasSuffix(f, "/a/a.go") {
				hasA = true
			}
			if strings.HasSuffix(f, "/b/b.go") {
				hasB = true
			}
		}
		if hasA && hasB {
			gotBoth = true
		}
	}
	if !gotBoth {
		t.Errorf("expected a cycle containing both a/a.go and b/b.go; got %+v", out.ImportCycles)
	}
}

// TestGetArchitecture_DeadCodeFiltering verifies the exported-name filter
// on Go symbols. The fixture's `Login` is exported and has zero callers
// inside the fixture (the cross-package call in Authenticate is unresolved
// from the fixture's perspective). Without the filter, Login would land
// in dead code; with the filter, it must not.
//
// TS / JS symbols are not filtered — we don't track TS export status per
// symbol and would rather surface a noisy "maybe dead" hint than miss a
// real one. The Exported field still gets set so the caller can judge.
func TestGetArchitecture_DeadCodeFiltering(t *testing.T) {
	r := loadTestRepo(t)
	out := callArch(t, r, `{}`)

	for _, d := range out.DeadCode {
		isGo := strings.HasSuffix(d.File, ".go")
		if isGo && d.Exported {
			t.Errorf("exported Go symbol %s leaked into dead code", d.ID)
		}
	}
}

// TestGetArchitecture_TopArg verifies the `top` knob caps the hotspot
// and topPackages lists.
func TestGetArchitecture_TopArg(t *testing.T) {
	r := loadTestRepo(t)
	out := callArch(t, r, `{"top":2}`)

	if len(out.TopPackages) > 2 {
		t.Errorf("topPackages has %d entries; cap is 2", len(out.TopPackages))
	}
	if len(out.Hotspots) > 2 {
		t.Errorf("hotspots has %d entries; cap is 2", len(out.Hotspots))
	}
}

// TestGetArchitecture_IncludeCyclesFalse confirms the flag suppresses
// the SCC pass entirely.
func TestGetArchitecture_IncludeCyclesFalse(t *testing.T) {
	r := loadTestRepo(t)
	out := callArch(t, r, `{"include_cycles":false}`)
	// Even with cycles false, the slice must be present (non-nil empty)
	// — clients shouldn't need a nil check.
	if out.ImportCycles == nil {
		t.Error("importCycles is nil; expected empty slice")
	}
}

// TestTarjanSCC_TrivialSanity pins the SCC implementation against a known
// graph so a future refactor doesn't silently break cycle detection.
// Nodes: a→b, b→a (cycle); c→d, d→c (cycle); e (isolated).
func TestTarjanSCC_TrivialSanity(t *testing.T) {
	nodes := []string{"a", "b", "c", "d", "e"}
	adj := map[string]map[string]bool{
		"a": {"b": true},
		"b": {"a": true},
		"c": {"d": true},
		"d": {"c": true},
		"e": {},
	}
	sccs := tarjanSCC(nodes, adj)

	// Expect two SCCs of size 2 and one of size 1.
	sizeBuckets := map[int]int{}
	for _, c := range sccs {
		sizeBuckets[len(c)]++
	}
	if sizeBuckets[2] != 2 {
		t.Errorf("expected 2 SCCs of size 2; got %d (all sizes: %v)", sizeBuckets[2], sizeBuckets)
	}
	if sizeBuckets[1] != 1 {
		t.Errorf("expected 1 SCC of size 1 (isolated e); got %d", sizeBuckets[1])
	}

	// The cycles must include {a,b} and {c,d} as members.
	mustContainPair := func(x, y string) {
		for _, c := range sccs {
			hasX, hasY := false, false
			for _, n := range c {
				if n == x {
					hasX = true
				}
				if n == y {
					hasY = true
				}
			}
			if hasX && hasY {
				return
			}
		}
		t.Errorf("no SCC contains both %q and %q", x, y)
	}
	mustContainPair("a", "b")
	mustContainPair("c", "d")
}

// --- helpers --------------------------------------------------------------

func callArch(t *testing.T, r *store.Repo, args string) *ArchitectureResult {
	t.Helper()
	out, err := GetArchitecture(r)(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("handler: %v (args=%s)", err, args)
	}
	a, ok := out.(*ArchitectureResult)
	if !ok {
		t.Fatalf("result type: got %T", out)
	}
	return a
}