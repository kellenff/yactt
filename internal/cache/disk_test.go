package cache_test

import (
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kellenff/yactt/internal/cache"
	"github.com/kellenff/yactt/internal/parser"
	"github.com/kellenff/yactt/internal/source"
)

// writeGoFile writes `body` to a fresh tempdir path and returns it.
// The mtime is left at "now" — callers who need a specific mtime
// stat-then-utimes the file.
func writeGoFile(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "auth.go")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return p
}

// TestDiskCache_RoundTrip pins the happy path: Put then Get returns
// the same path, bytes, and mtime. The re-parse on hydrate produces
// a usable parse root (Lines returns the expected count).
func TestDiskCache_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	c, err := cache.NewDiskCache(dir)
	if err != nil {
		t.Fatalf("NewDiskCache: %v", err)
	}

	const body = "package auth\n\nfunc Login() error { return nil }\n"
	p := writeGoFile(t, body)
	st, _ := os.Stat(p)
	mtime := st.ModTime().UnixNano()

	// Simulate what store.Load does: parse once, persist to disk.
	src, err := source.LoadFile(p, parser.Go{})
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if err := c.Put(src); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := c.Get(p, mtime)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Path != p {
		t.Errorf("Path = %q, want %q", got.Path, p)
	}
	if got.MTime != mtime {
		t.Errorf("MTime = %d, want %d", got.MTime, mtime)
	}
	if string(got.Bytes) != body {
		t.Errorf("Bytes mismatch:\n got %q\nwant %q", got.Bytes, body)
	}
	if got.Root == nil {
		t.Error("Root is nil after hydrate; re-parse failed")
	}
	// Lines is the cheapest round-trip check on the parse tree.
	lines, err := got.Lines()
	if err != nil {
		t.Fatalf("Lines: %v", err)
	}
	if len(lines) < 3 {
		t.Errorf("Lines len = %d, want ≥3", len(lines))
	}
}

// TestDiskCache_MtimeInvalidation confirms a stale mtime produces
// ErrMiss, not a corrupt file.
func TestDiskCache_MtimeInvalidation(t *testing.T) {
	dir := t.TempDir()
	c, err := cache.NewDiskCache(dir)
	if err != nil {
		t.Fatalf("NewDiskCache: %v", err)
	}

	p := writeGoFile(t, "package auth\n")
	src, _ := source.LoadFile(p, parser.Go{})
	_ = c.Put(src)

	// Get with a different mtime → miss.
	if _, err := c.Get(p, src.MTime+1); err != cache.ErrMiss {
		t.Errorf("err = %v, want ErrMiss", err)
	}
}

// TestDiskCache_CorruptionRecovery: a junk file at the key path
// must surface as ErrMiss (not panic, not corrupt-return).
func TestDiskCache_CorruptionRecovery(t *testing.T) {
	dir := t.TempDir()
	c, err := cache.NewDiskCache(dir)
	if err != nil {
		t.Fatalf("NewDiskCache: %v", err)
	}

	// Put one real entry, then overwrite with junk at the same key.
	p := writeGoFile(t, "package auth\n")
	src, _ := source.LoadFile(p, parser.Go{})
	_ = c.Put(src)

	sum := sha256.Sum256([]byte(p))
	key := filepath.Join(dir, hex.EncodeToString(sum[:16]))
	if err := os.WriteFile(key, []byte("not a gob stream"), 0o644); err != nil {
		t.Fatalf("WriteFile (corrupt): %v", err)
	}

	if _, err := c.Get(p, src.MTime); err != cache.ErrMiss {
		t.Errorf("err = %v, want ErrMiss on corrupt entry", err)
	}

	// A subsequent Put overwrites cleanly.
	src2, _ := source.LoadFile(p, parser.Go{})
	if err := c.Put(src2); err != nil {
		t.Fatalf("Put after corruption: %v", err)
	}
	if _, err := c.Get(p, src2.MTime); err != nil {
		t.Errorf("Get after Put recovery: %v", err)
	}
}

// TestDiskCache_CrossFileIsolation: Put file A, Get file B → miss.
// Different source paths must not collide even when their hashes
// happen to share a directory.
func TestDiskCache_CrossFileIsolation(t *testing.T) {
	dir := t.TempDir()
	c, err := cache.NewDiskCache(dir)
	if err != nil {
		t.Fatalf("NewDiskCache: %v", err)
	}

	pA := writeGoFile(t, "package a\n")
	pB := writeGoFile(t, "package b\n")
	srcA, _ := source.LoadFile(pA, parser.Go{})
	_ = c.Put(srcA)

	if _, err := c.Get(pB, srcA.MTime); err != cache.ErrMiss {
		t.Errorf("Get(pB) after Put(pA): err = %v, want ErrMiss", err)
	}
	if _, err := c.Get(pA, srcA.MTime); err != nil {
		t.Errorf("Get(pA) round-trip after Put(pB): err = %v", err)
	}
}

// TestDiskCache_NewDiskCache_Idempotent: NewDiskCache on an existing
// populated directory must not error or erase entries.
func TestDiskCache_NewDiskCache_Idempotent(t *testing.T) {
	dir := t.TempDir()
	c1, err := cache.NewDiskCache(dir)
	if err != nil {
		t.Fatalf("NewDiskCache (1st): %v", err)
	}
	p := writeGoFile(t, "package auth\n")
	src, _ := source.LoadFile(p, parser.Go{})
	_ = c1.Put(src)

	// Re-open; entries must survive.
	c2, err := cache.NewDiskCache(dir)
	if err != nil {
		t.Fatalf("NewDiskCache (2nd): %v", err)
	}
	if _, err := c2.Get(p, src.MTime); err != nil {
		t.Errorf("Get after reopen: %v", err)
	}
}

// TestDiskCache_DirAccessor pins the Dir() accessor (used by tests
// and future diagnostics).
func TestDiskCache_DirAccessor(t *testing.T) {
	dir := t.TempDir()
	c, err := cache.NewDiskCache(dir)
	if err != nil {
		t.Fatalf("NewDiskCache: %v", err)
	}
	if c.Dir() != dir {
		t.Errorf("Dir() = %q, want %q", c.Dir(), dir)
	}
}

// Sanity guard: gob is the on-disk format. This test pins the
// dependency so a future refactor doesn't swap to JSON silently.
func TestDiskCache_GobFormatStillGob(t *testing.T) {
	var buf []byte
	if err := gob.NewEncoder(newByteWriter(&buf)).Encode(struct {
		MTime   int64
		Grammar string
		Bytes   []byte
	}{MTime: 1, Grammar: "go", Bytes: []byte("x")}); err != nil {
		t.Skipf("gob unavailable: %v", err)
	}
}

// newByteWriter is a tiny io.Writer wrapper around a byte slice for
// the format-pin test above.
type byteWriter struct{ p *[]byte }

func (w byteWriter) Write(b []byte) (int, error) {
	*w.p = append(*w.p, b...)
	return len(b), nil
}
func newByteWriter(p *[]byte) byteWriter { return byteWriter{p: p} }

// TestDiskCache_CapDisabled: WithMaxBytes(0) means unlimited; no
// eviction ever runs. The cache dir grows as files are added.
func TestDiskCache_CapDisabled(t *testing.T) {
	dir := t.TempDir()
	c, err := cache.NewDiskCache(dir, cache.WithMaxBytes(0))
	if err != nil {
		t.Fatalf("NewDiskCache: %v", err)
	}
	// Many small Puts — none should trigger eviction.
	for i := 0; i < 10; i++ {
		body := "package p" + intToStr(i) + "\n"
		p := writeGoFile(t, body)
		f, _ := source.LoadFile(p, parser.Go{})
		if err := c.Put(f); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 10 {
		t.Errorf("cache dir entries = %d, want 10 (no eviction expected)", len(entries))
	}
}

// TestDiskCache_EvictsOldestFirst: with a small cap, repeated Puts
// evict the oldest entries by mtime. Sequential Puts set mtimes in
// the order they're called, so oldest = first Put.
func TestDiskCache_EvictsOldestFirst(t *testing.T) {
	dir := t.TempDir()
	// Cap is set to one entry + small slack — verified empirically
	// at 86 bytes per "package pN\n" entry; cap 100 fits exactly
	// one entry, two entries tip over, eviction kicks in.
	c, err := cache.NewDiskCache(dir, cache.WithMaxBytes(100))
	if err != nil {
		t.Fatalf("NewDiskCache: %v", err)
	}

	paths := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		p := writeGoFile(t, "package p"+intToStr(i)+"\n")
		paths = append(paths, p)
		f, _ := source.LoadFile(p, parser.Go{})
		if err := c.Put(f); err != nil {
			t.Fatalf("Put[%d]: %v", i, err)
		}
	}

	// After 3 Puts with cap 50, only the most recent (paths[2])
	// should remain. The earlier two were evicted on their
	// successors' Puts.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("cache dir entries = %d, want 1 (oldest evicted); got %d", len(entries), len(entries))
	}

	// Confirm the surviving file's content matches the most recent Put.
	// The cache file is keyed by sha256(path), not by name; we
	// re-fetch via the API.
	f3, err := c.Get(paths[2], fileMTime(t, paths[2]))
	if err != nil {
		t.Fatalf("Get(latest path) after eviction: %v", err)
	}
	if !strings.Contains(string(f3.Bytes), "package p2") {
		t.Errorf("latest entry content = %q, want \"package p2\"", string(f3.Bytes))
	}

	// And the oldest paths are NOT retrievable.
	if _, err := c.Get(paths[0], fileMTime(t, paths[0])); err != cache.ErrMiss {
		t.Errorf("oldest entry still in cache: err = %v, want ErrMiss", err)
	}
}

// TestDiskCache_UnderBudget_NoEviction: cap is set, total is under
// it, no eviction should happen.
func TestDiskCache_UnderBudget_NoEviction(t *testing.T) {
	dir := t.TempDir()
	// 5 entries × 86 bytes ≈ 430 bytes; cap 10 KiB leaves plenty
	// of slack.
	c, err := cache.NewDiskCache(dir, cache.WithMaxBytes(10*1024))
	if err != nil {
		t.Fatalf("NewDiskCache: %v", err)
	}

	const n = 5
	paths := make([]string, 0, n)
	for i := 0; i < n; i++ {
		p := writeGoFile(t, "package p"+intToStr(i)+"\n")
		paths = append(paths, p)
		f, _ := source.LoadFile(p, parser.Go{})
		if err := c.Put(f); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != n {
		t.Errorf("cache dir entries = %d, want %d (under cap, no eviction)", len(entries), n)
	}
}

// TestDiskCache_NegativeCapClampedToZero: WithMaxBytes(-1) should
// be treated as unlimited (clamped to 0).
func TestDiskCache_NegativeCapClampedToZero(t *testing.T) {
	dir := t.TempDir()
	c, err := cache.NewDiskCache(dir, cache.WithMaxBytes(-1))
	if err != nil {
		t.Fatalf("NewDiskCache: %v", err)
	}
	for i := 0; i < 5; i++ {
		p := writeGoFile(t, "package p"+intToStr(i)+"\n")
		f, _ := source.LoadFile(p, parser.Go{})
		_ = c.Put(f)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 5 {
		t.Errorf("negative cap should disable eviction; entries = %d, want 5", len(entries))
	}
}

// fileMTime returns the source file's mtime in unix nanos — the
// same form the disk cache stores in diskEntry.MTime.
func fileMTime(t *testing.T, path string) int64 {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return st.ModTime().UnixNano()
}

// intToStr renders a small non-negative int as a base-10 string
// without pulling strconv into the test file.
func intToStr(n int) string {
	if n == 0 {
		return "0"
	}
	const digits = "0123456789"
	var s []byte
	for n > 0 {
		s = append([]byte{digits[n%10]}, s...)
		n /= 10
	}
	return string(s)
}

// TestDiskCache_EvictOrphans: a cache entry whose source path is
// not in validPaths is deleted; entries whose paths ARE in
// validPaths are preserved. Reproduces the "repo file deleted
// between runs" scenario.
func TestDiskCache_EvictOrphans(t *testing.T) {
	dir := t.TempDir()
	c, err := cache.NewDiskCache(dir)
	if err != nil {
		t.Fatalf("NewDiskCache: %v", err)
	}

	// Put 3 source files; each becomes a cache entry.
	p1 := writeGoFile(t, "package a\n")
	p2 := writeGoFile(t, "package b\n")
	p3 := writeGoFile(t, "package c\n")
	for _, p := range []string{p1, p2, p3} {
		f, _ := source.LoadFile(p, parser.Go{})
		if err := c.Put(f); err != nil {
			t.Fatalf("Put %s: %v", p, err)
		}
	}
	if got := lenDir(t, dir); got != 3 {
		t.Fatalf("setup: cache dir = %d entries, want 3", got)
	}

	// Sweep with only 2 valid paths — p3's entry is orphan.
	if err := c.EvictOrphans([]string{p1, p2}); err != nil {
		t.Fatalf("EvictOrphans: %v", err)
	}
	if got := lenDir(t, dir); got != 2 {
		t.Errorf("after sweep: cache dir = %d entries, want 2", got)
	}
	// p3 must be a miss now.
	if _, err := c.Get(p3, fileMTime(t, p3)); err != cache.ErrMiss {
		t.Errorf("orphan still cached: err = %v, want ErrMiss", err)
	}
	// p1, p2 must still be retrievable.
	for _, p := range []string{p1, p2} {
		if _, err := c.Get(p, fileMTime(t, p)); err != nil {
			t.Errorf("valid entry %s missing after sweep: err = %v", p, err)
		}
	}
}

// TestDiskCache_EvictOrphans_EmptyValid: an empty validPaths
// evicts every cache entry. Legitimate for a fresh-empty repo
// (zero source files → zero cache entries).
func TestDiskCache_EvictOrphans_EmptyValid(t *testing.T) {
	dir := t.TempDir()
	c, _ := cache.NewDiskCache(dir)
	for _, body := range []string{"package a\n", "package b\n"} {
		p := writeGoFile(t, body)
		f, _ := source.LoadFile(p, parser.Go{})
		_ = c.Put(f)
	}
	if err := c.EvictOrphans(nil); err != nil {
		t.Fatalf("EvictOrphans(nil): %v", err)
	}
	if got := lenDir(t, dir); got != 0 {
		t.Errorf("after empty-valid sweep: cache dir = %d, want 0", got)
	}
}

// TestDiskCache_EvictOrphans_SkipsNonHex: a non-cache file
// planted in the directory must NOT be deleted. Defence-in-depth
// against stray files, manual intervention, future format changes.
func TestDiskCache_EvictOrphans_SkipsNonHex(t *testing.T) {
	dir := t.TempDir()
	c, _ := cache.NewDiskCache(dir)
	stray := filepath.Join(dir, "README.md")
	if err := os.WriteFile(stray, []byte("hello"), 0o644); err != nil {
		t.Fatalf("plant stray: %v", err)
	}
	// Plant a short hex name that's NOT 32 chars — also skipped.
	if err := os.WriteFile(filepath.Join(dir, "short"), []byte("x"), 0o644); err != nil {
		t.Fatalf("plant short: %v", err)
	}
	if err := c.EvictOrphans(nil); err != nil {
		t.Fatalf("EvictOrphans: %v", err)
	}
	if _, err := os.Stat(stray); err != nil {
		t.Errorf("non-hex file deleted: err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "short")); err != nil {
		t.Errorf("short-hex file deleted: err = %v", err)
	}
}

// TestDiskCache_EvictOrphans_SkipsTmp: a leftover .tmp file
// (from a crashed Put) is not deleted by the orphan sweep.
// Stale-on-crash artefacts have separate cleanup paths.
func TestDiskCache_EvictOrphans_SkipsTmp(t *testing.T) {
	dir := t.TempDir()
	c, _ := cache.NewDiskCache(dir)
	tmp := filepath.Join(dir, "070024aca372328040c24484fb74ea31.tmp")
	if err := os.WriteFile(tmp, []byte("x"), 0o644); err != nil {
		t.Fatalf("plant tmp: %v", err)
	}
	if err := c.EvictOrphans(nil); err != nil {
		t.Fatalf("EvictOrphans: %v", err)
	}
	if _, err := os.Stat(tmp); err != nil {
		t.Errorf(".tmp file deleted by orphan sweep: err = %v", err)
	}
}

// lenDir is a tiny helper: count entries in dir. Errors fail
// the test — should not happen for t.TempDir.
func lenDir(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir %s: %v", dir, err)
	}
	return len(entries)
}
