package genfixture_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/kellenff/yactt/tests/chunking/genfixture"
)

// TestWrite_Shape pins the fixture's overall counts: the issue-34
// success criterion is "100-file repo chunks in ≤10s" so we want
// roughly 100 files and ~500 declarations. The exact numbers can
// drift as the generator evolves; the test uses generous bounds.
func TestWrite_Shape(t *testing.T) {
	dir := t.TempDir()
	syms, err := genfixture.Write(dir)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if len(syms) < 300 {
		t.Errorf("symbols = %d, want >= 300", len(syms))
	}
	if len(syms) > 1000 {
		t.Errorf("symbols = %d, want <= 1000", len(syms))
	}

	// Count files: each Go source file under the three packages.
	files := map[string]bool{}
	for _, s := range syms {
		files[s.File] = true
	}
	if len(files) < 80 {
		t.Errorf("files = %d, want >= 80", len(files))
	}
	if len(files) > 120 {
		t.Errorf("files = %d, want <= 120", len(files))
	}

	// Sanity: all 3 packages represented.
	pkgs := map[string]bool{}
	for _, s := range syms {
		pkgs[s.Package] = true
	}
	for _, want := range []string{"auth", "payments", "users"} {
		if !pkgs[want] {
			t.Errorf("missing package %q in fixture", want)
		}
	}
}

// TestWrite_Deterministic pins reproducibility. Two Write calls
// with the same root must produce byte-identical source files.
func TestWrite_Deterministic(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	if _, err := genfixture.Write(a); err != nil {
		t.Fatal(err)
	}
	if _, err := genfixture.Write(b); err != nil {
		t.Fatal(err)
	}
	diffTree(t, a, b, "")
}

// TestWrite_GoMod pins the go.mod presence — the chunker uses the
// module name as a package fallback for root-level files, and the
// benchmark's id-resolution depends on it.
func TestWrite_GoMod(t *testing.T) {
	dir := t.TempDir()
	if _, err := genfixture.Write(dir); err != nil {
		t.Fatal(err)
	}
	gm, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	if !strings.Contains(string(gm), "module ") {
		t.Errorf("go.mod missing module directive: %s", gm)
	}
}

// TestWrite_LineRangesPinned pins that every emitted symbol has a
// non-zero StartLine and EndLine. The recall benchmark needs these
// to match baseline chunks to expected AST chunks.
func TestWrite_LineRangesPinned(t *testing.T) {
	dir := t.TempDir()
	syms, err := genfixture.Write(dir)
	if err != nil {
		t.Fatal(err)
	}
	missing := 0
	for _, s := range syms {
		if s.StartLine <= 0 || s.EndLine <= 0 {
			missing++
		}
	}
	if missing > 0 {
		t.Errorf("%d symbols with no line range", missing)
	}
}

// diffTree walks two directories and reports the first mismatch
// (file content or file presence). Used by TestWrite_Deterministic.
func diffTree(t *testing.T, a, b, rel string) {
	t.Helper()
	ad := filepath.Join(a, rel)
	bd := filepath.Join(b, rel)
	aEntries, err := os.ReadDir(ad)
	if err != nil {
		t.Fatalf("readdir %s: %v", ad, err)
	}
	bEntries, err := os.ReadDir(bd)
	if err != nil {
		t.Fatalf("readdir %s: %v", bd, err)
	}
	if len(aEntries) != len(bEntries) {
		t.Errorf("entry count mismatch at %q: %d vs %d", rel, len(aEntries), len(bEntries))
		return
	}
	aNames := make([]string, 0, len(aEntries))
	for _, e := range aEntries {
		aNames = append(aNames, e.Name())
	}
	bNames := make([]string, 0, len(bEntries))
	for _, e := range bEntries {
		bNames = append(bNames, e.Name())
	}
	sort.Strings(aNames)
	sort.Strings(bNames)
	for i := range aNames {
		if aNames[i] != bNames[i] {
			t.Errorf("entry name mismatch at %q: %s vs %s", rel, aNames[i], bNames[i])
			return
		}
		if aEntries[i].IsDir() {
			diffTree(t, a, b, filepath.Join(rel, aNames[i]))
			continue
		}
		ab, _ := os.ReadFile(filepath.Join(ad, aNames[i]))
		bb, _ := os.ReadFile(filepath.Join(bd, bNames[i]))
		if string(ab) != string(bb) {
			t.Errorf("content mismatch at %q: %s", rel, aNames[i])
			return
		}
	}
}
