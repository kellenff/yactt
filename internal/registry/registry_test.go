package registry_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/kellenff/yactt/internal/registry"
)

// registryInTemp returns a fresh Registry rooted at a tempdir.
// Setting XDG_CACHE_HOME isn't needed because New takes an explicit
// path; the helper isolates every test from a sibling's writes.
func registryInTemp(t *testing.T) *registry.Registry {
	t.Helper()
	dir := t.TempDir()
	return registry.New(filepath.Join(dir, "projects.json"))
}

func entry(path, name string, files int, indexedAt time.Time) registry.Entry {
	return registry.Entry{
		Name:      name,
		Path:      path,
		IndexedAt: indexedAt,
		Files:     files,
		Languages: []string{"go"},
		Mode:      "full",
	}
}

func TestNew_Path(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "projects.json")
	r := registry.New(p)
	if got := r.Path(); got != p {
		t.Fatalf("Path() = %q, want %q", got, p)
	}
}

func TestList_EmptyFile(t *testing.T) {
	r := registryInTemp(t)
	got, err := r.List()
	if err != nil {
		t.Fatalf("List on missing file: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("List on missing file = %d entries, want 0", len(got))
	}
}

// TestUpsert_CreatesFile ensures the first Upsert creates the
// parent directory and the file. Mirror of disk.DiskCache's
// "MkdirAll + atomic write" pattern.
func TestUpsert_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "deep", "nested", "projects.json")
	r := registry.New(nested)
	if err := r.Upsert(entry("/tmp/a", "alpha", 5, time.Now())); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}
	if _, err := os.Stat(nested); err != nil {
		t.Fatalf("file not created: %v", err)
	}
}

// TestUpsert_MergesByPath is the heart of the contract: a second
// Upsert for the same Path replaces the entry in place (the
// surrounding list is preserved, not duplicated).
func TestUpsert_MergesByPath(t *testing.T) {
	r := registryInTemp(t)
	now := time.Now()
	if err := r.Upsert(entry("/tmp/a", "alpha", 5, now)); err != nil {
		t.Fatal(err)
	}
	if err := r.Upsert(entry("/tmp/b", "beta", 9, now)); err != nil {
		t.Fatal(err)
	}
	got, err := r.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	later := now.Add(time.Hour)
	if err := r.Upsert(entry("/tmp/a", "alpha-renamed", 12, later)); err != nil {
		t.Fatal(err)
	}
	got, _ = r.List()
	if len(got) != 2 {
		t.Fatalf("after merge: len = %d, want 2", len(got))
	}
	var a registry.Entry
	for _, e := range got {
		if e.Path == "/tmp/a" {
			a = e
		}
	}
	if a.Name != "alpha-renamed" || a.Files != 12 {
		t.Fatalf("merged entry not replaced: %+v", a)
	}
}

func TestGetByPath(t *testing.T) {
	r := registryInTemp(t)
	now := time.Now()
	_ = r.Upsert(entry("/tmp/a", "alpha", 5, now))

	got, ok := r.GetByPath("/tmp/a")
	if !ok {
		t.Fatal("GetByPath returned ok=false for known path")
	}
	if got.Name != "alpha" {
		t.Fatalf("Name = %q, want alpha", got.Name)
	}
	if _, ok := r.GetByPath("/tmp/missing"); ok {
		t.Fatal("GetByPath returned ok=true for missing path")
	}
}

func TestDelete(t *testing.T) {
	r := registryInTemp(t)
	_ = r.Upsert(entry("/tmp/a", "alpha", 5, time.Now()))
	_ = r.Upsert(entry("/tmp/b", "beta", 9, time.Now()))

	deleted, err := r.Delete("/tmp/a")
	if err != nil {
		t.Fatal(err)
	}
	if !deleted {
		t.Fatal("Delete returned false for known path")
	}
	deleted, err = r.Delete("/tmp/a")
	if err != nil {
		t.Fatal(err)
	}
	if deleted {
		t.Fatal("Delete returned true on second call (idempotency broken)")
	}
	got, _ := r.List()
	if len(got) != 1 || got[0].Path != "/tmp/b" {
		t.Fatalf("after Delete: %+v, want one entry /tmp/b", got)
	}
}

func TestDelete_OnMissingFile(t *testing.T) {
	r := registryInTemp(t)
	deleted, err := r.Delete("/tmp/none")
	if err != nil {
		t.Fatal(err)
	}
	if deleted {
		t.Fatal("Delete returned true on missing file")
	}
}

func TestUpsert_RejectsEmptyPath(t *testing.T) {
	r := registryInTemp(t)
	if err := r.Upsert(registry.Entry{Name: "anon"}); err == nil {
		t.Fatal("Upsert with empty path: want error")
	}
}

// TestLoad_CorruptFileQuarantined asserts that a malformed
// projects.json is renamed aside and the next call starts fresh.
// The quarantine filename is captured before the rename so we can
// assert its suffix, not the timestamp.
func TestLoad_CorruptFileQuarantined(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "projects.json")
	if err := os.WriteFile(p, []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := registry.New(p)
	_, err := r.List()
	if err == nil {
		t.Fatal("List on corrupt file: want error")
	}
	entries, _ := os.ReadDir(dir)
	var foundQuarantine bool
	const prefix = "projects.json.corrupt-"
	for _, e := range entries {
		if len(e.Name()) > len(prefix) && e.Name()[:len(prefix)] == prefix {
			foundQuarantine = true
			break
		}
	}
	if !foundQuarantine {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("no quarantine file created; dir: %v", names)
	}
	// Next read returns the empty/clean state.
	got, lerr := r.List()
	if lerr != nil {
		t.Fatalf("List after quarantine: %v", lerr)
	}
	if len(got) != 0 {
		t.Fatalf("List after quarantine = %d entries, want 0", len(got))
	}
}

// TestList_SortedByPath guards the "deterministic order" claim
// from the doc comment. Insert out of order, read back.
func TestList_SortedByPath(t *testing.T) {
	r := registryInTemp(t)
	now := time.Now()
	_ = r.Upsert(entry("/tmp/zeta", "z", 1, now))
	_ = r.Upsert(entry("/tmp/alpha", "a", 1, now))
	_ = r.Upsert(entry("/tmp/middle", "m", 1, now))
	got, _ := r.List()
	want := []string{"/tmp/alpha", "/tmp/middle", "/tmp/zeta"}
	for i, e := range got {
		if e.Path != want[i] {
			t.Fatalf("position %d: got %s, want %s", i, e.Path, want[i])
		}
	}
}

// TestUpsert_PreservesUnrelatedFields keeps the on-disk shape
// honest: an entry the test writes by hand round-trips through
// Load → Marshal.
func TestUpsert_PreservesUnrelatedFields(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "projects.json")
	seed := map[string]any{
		"version": float64(registry.Version),
		"projects": []map[string]any{
			{
				"name":      "alpha",
				"path":      "/tmp/alpha",
				"indexedAt": time.Now().UTC().Format(time.RFC3339Nano),
				"files":     float64(7),
				"languages": []string{"go"},
				"mode":      "full",
			},
		},
	}
	data, _ := json.MarshalIndent(seed, "", "  ")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	r := registry.New(p)
	got, err := r.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "alpha" || got[0].Files != 7 {
		t.Fatalf("round-trip failed: %+v", got)
	}
}

// TestStore_AtomicWrite guards that a successful Upsert leaves no
// orphan temp files behind in the directory.
func TestStore_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	r := registry.New(filepath.Join(dir, "projects.json"))
	for i := 0; i < 5; i++ {
		if err := r.Upsert(entry(filepath.Join("/tmp", string(rune('a'+i))), "x", 1, time.Now())); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("orphan tmp file left behind: %s", e.Name())
		}
	}
}

// TestDefaultPath_FallsBackToHome confirms the fallback chain:
// XDG unset → $HOME/.cache/yactt/projects.json.
func TestDefaultPath_FallsBackToHome(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	got := registry.DefaultPath()
	want := filepath.Join(home, ".cache", "yactt", "projects.json")
	if got != want {
		t.Fatalf("DefaultPath() = %q, want %q", got, want)
	}
}

// TestDefaultPath_PrefersXDG mirrors the above but with XDG set.
func TestDefaultPath_PrefersXDG(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", xdg)
	t.Setenv("HOME", "/should/not/be/used")
	got := registry.DefaultPath()
	want := filepath.Join(xdg, "yactt", "projects.json")
	if got != want {
		t.Fatalf("DefaultPath() = %q, want %q", got, want)
	}
}

// TestUpsert_NilEntrySlice guards an edge case: starting from
// "Projects: nil" JSON (rather than "Projects: []"), Upsert
// produces a real slice, not a nil one stamped into the file.
func TestUpsert_NilEntrySlice(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "projects.json")
	if err := os.WriteFile(p, []byte(`{"version":1,"projects":null}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r := registry.New(p)
	if err := r.Upsert(entry("/tmp/x", "x", 1, time.Now())); err != nil {
		t.Fatal(err)
	}
	got, _ := r.List()
	if !reflect.DeepEqual([]string{"/tmp/x"}, paths(got)) {
		t.Fatalf("got %v", paths(got))
	}
}

func paths(in []registry.Entry) []string {
	out := make([]string, len(in))
	for i, e := range in {
		out[i] = e.Path
	}
	return out
}
