package cache

import (
	"errors"
	"testing"
	"time"

	"github.com/kellenff/yactt/internal/source"
)

// fakeClock returns a closure that yields successive time.Time values on each
// call. cache tests use it to drive TTL behavior without time.Sleep.
type fakeClock struct {
	current time.Time
}

func (f *fakeClock) now() time.Time { return f.current }

func (f *fakeClock) advance(d time.Duration) { f.current = f.current.Add(d) }

func newCacheWithClock() (*Cache, *fakeClock) {
	clk := &fakeClock{current: time.Unix(1_700_000_000, 0).UTC()}
	c := NewSized(3, 3)
	c.now = clk.now
	return c, clk
}

func mkFile(path string, mtime int64) *source.File {
	return &source.File{Path: path, Bytes: []byte("data"), MTime: mtime}
}

func TestCachePutGetFile(t *testing.T) {
	c := NewSized(8, 8)
	c.PutFile(mkFile("/a.go", 1))
	got, err := c.GetFile("/a.go", 1)
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if got.Path != "/a.go" || got.MTime != 1 {
		t.Errorf("got %+v, want path=/a.go mtime=1", got)
	}

	if _, err := c.GetFile("/missing.go", 1); !errors.Is(err, ErrMiss) {
		t.Errorf("missing path err = %v, want ErrMiss", err)
	}
}

func TestCacheFileMtimeInvalidation(t *testing.T) {
	c := NewSized(8, 8)
	c.PutFile(mkFile("/a.go", 1))

	// Different mtime -> ErrMiss and the stale entry is dropped.
	if _, err := c.GetFile("/a.go", 2); !errors.Is(err, ErrMiss) {
		t.Fatalf("mtime change err = %v, want ErrMiss", err)
	}
	if snap := c.Snapshot(); snap.Files != 0 {
		t.Errorf("after stale drop, Files = %d, want 0", snap.Files)
	}

	// Re-put with new mtime; original mtime no longer hits.
	c.PutFile(mkFile("/a.go", 2))
	if _, err := c.GetFile("/a.go", 2); err != nil {
		t.Errorf("fresh mtime: %v", err)
	}
	if _, err := c.GetFile("/a.go", 1); !errors.Is(err, ErrMiss) {
		t.Errorf("old mtime hit after update, err = %v", err)
	}
}

func TestCacheFileLRUEviction(t *testing.T) {
	c := NewSized(2, 8)
	c.PutFile(mkFile("/a.go", 1))
	c.PutFile(mkFile("/b.go", 1))
	c.PutFile(mkFile("/c.go", 1)) // evicts /a.go (least-recently-used)

	if _, err := c.GetFile("/a.go", 1); !errors.Is(err, ErrMiss) {
		t.Errorf("a.go should be evicted, err = %v", err)
	}
	if _, err := c.GetFile("/b.go", 1); err != nil {
		t.Errorf("b.go should still be cached: %v", err)
	}
	if _, err := c.GetFile("/c.go", 1); err != nil {
		t.Errorf("c.go should still be cached: %v", err)
	}
}

func TestCacheFileLRUOrder(t *testing.T) {
	c := NewSized(2, 8)
	c.PutFile(mkFile("/a.go", 1))
	c.PutFile(mkFile("/b.go", 1))

	// Touch /a so /b becomes the LRU; next Put should evict /b, not /a.
	if _, err := c.GetFile("/a.go", 1); err != nil {
		t.Fatalf("warm-up: %v", err)
	}
	c.PutFile(mkFile("/c.go", 1))

	if _, err := c.GetFile("/a.go", 1); err != nil {
		t.Errorf("/a.go should still be cached (touched): %v", err)
	}
	if _, err := c.GetFile("/b.go", 1); !errors.Is(err, ErrMiss) {
		t.Errorf("/b.go should be evicted (LRU), err = %v", err)
	}
}

func TestCacheFilePutRefreshesLRU(t *testing.T) {
	c := NewSized(2, 8)
	c.PutFile(mkFile("/a.go", 1))
	c.PutFile(mkFile("/b.go", 1))
	// Re-Put /a with the same mtime; /a is now MRU, /b is LRU.
	c.PutFile(mkFile("/a.go", 1))
	c.PutFile(mkFile("/c.go", 1))

	if _, err := c.GetFile("/b.go", 1); !errors.Is(err, ErrMiss) {
		t.Errorf("/b.go should be evicted after re-Put of /a.go, err = %v", err)
	}
	if _, err := c.GetFile("/a.go", 1); err != nil {
		t.Errorf("/a.go should still be cached: %v", err)
	}
}

func TestCacheLayerRoundTrip(t *testing.T) {
	c, _ := newCacheWithClock()
	c.PutLayer("node1", "summary", []byte("hello"))

	got, err := c.GetLayer("node1", "summary")
	if err != nil {
		t.Fatalf("GetLayer: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

func TestCacheLayerTTLExpirySemantic(t *testing.T) {
	c, clk := newCacheWithClock()
	c.PutLayer("node1", "signature", []byte("payload"))

	// Just under TTL: still live.
	clk.advance(DefaultSemanticTTL - time.Millisecond)
	if _, err := c.GetLayer("node1", "signature"); err != nil {
		t.Fatalf("just-before-TTL: %v", err)
	}

	// Cross the TTL boundary: expired.
	clk.advance(2 * time.Millisecond)
	if _, err := c.GetLayer("node1", "signature"); !errors.Is(err, ErrMiss) {
		t.Errorf("post-TTL err = %v, want ErrMiss", err)
	}
	if snap := c.Snapshot(); snap.Layers != 0 {
		t.Errorf("after expiry, Layers = %d, want 0", snap.Layers)
	}
}

func TestCacheLayerTTLExpirySummary(t *testing.T) {
	c, clk := newCacheWithClock()
	c.PutLayer("node1", "summary", []byte("payload"))

	// Cross semantic TTL but stay below summary TTL.
	clk.advance(DefaultSemanticTTL + time.Second)
	if _, err := c.GetLayer("node1", "summary"); err != nil {
		t.Fatalf("summary layer should still be live: %v", err)
	}

	// Cross the much-larger summary TTL.
	clk.advance(DefaultSummaryTTL)
	if _, err := c.GetLayer("node1", "summary"); !errors.Is(err, ErrMiss) {
		t.Errorf("summary after long wait err = %v, want ErrMiss", err)
	}
}

func TestCacheLayerLRUEviction(t *testing.T) {
	c, _ := newCacheWithClock()
	// Fill to cap exactly.
	for i := 0; i < c.layerCap; i++ {
		c.PutLayer("n", layerName(i), []byte{byte(i)})
	}
	if snap := c.Snapshot(); snap.Layers != c.layerCap {
		t.Fatalf("setup: Layers = %d, want %d", snap.Layers, c.layerCap)
	}
	// One more push evicts the LRU (the earliest inserted).
	c.PutLayer("n", "summary", []byte{0xff})
	if snap := c.Snapshot(); snap.Layers != c.layerCap {
		t.Errorf("Layers = %d, want %d (cap unchanged)", snap.Layers, c.layerCap)
	}
	if _, err := c.GetLayer("n", "0"); !errors.Is(err, ErrMiss) {
		t.Errorf("LRU entry should be evicted, err = %v", err)
	}
}

func TestCacheLayerPutRefreshesEntry(t *testing.T) {
	c, clk := newCacheWithClock()
	c.PutLayer("n", "summary", []byte("v1"))

	// Re-Put at a later time; the layer should still be live after the
	// semantic TTL boundary because PutLayer bumped the timestamp.
	clk.advance(DefaultSemanticTTL + time.Second)
	c.PutLayer("n", "summary", []byte("v2"))

	got, err := c.GetLayer("n", "summary")
	if err != nil {
		t.Fatalf("after re-Put: %v", err)
	}
	if string(got) != "v2" {
		t.Errorf("got %q, want %q", got, "v2")
	}
}

func TestCacheSnapshot(t *testing.T) {
	c := NewSized(4, 4)
	if snap := c.Snapshot(); snap.Files != 0 || snap.Layers != 0 {
		t.Errorf("empty cache snap = %+v, want zero", snap)
	}

	c.PutFile(mkFile("/a.go", 1))
	c.PutFile(mkFile("/b.go", 1))
	c.PutLayer("n1", "summary", []byte("x"))
	c.PutLayer("n1", "signature", []byte("y"))

	snap := c.Snapshot()
	if snap.Files != 2 {
		t.Errorf("Files = %d, want 2", snap.Files)
	}
	if snap.Layers != 2 {
		t.Errorf("Layers = %d, want 2", snap.Layers)
	}
}

func TestCacheMissIsSentinel(t *testing.T) {
	c := NewSized(2, 2)
	if _, err := c.GetFile("/nope", 0); !errors.Is(err, ErrMiss) {
		t.Errorf("GetFile miss: %v, want ErrMiss", err)
	}
	if _, err := c.GetLayer("nope", "summary"); !errors.Is(err, ErrMiss) {
		t.Errorf("GetLayer miss: %v, want ErrMiss", err)
	}
}

// layerName returns a synthetic layer name for the i-th insert in eviction
// tests. Avoids name collisions with the real "summary"/"signature"/etc.
func layerName(i int) string {
	return "filler-" + itoa(i)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}
