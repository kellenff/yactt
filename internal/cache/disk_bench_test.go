package cache_test

import (
	"testing"

	"github.com/kellenff/yactt/internal/cache"
	"github.com/kellenff/yactt/internal/parser"
	"github.com/kellenff/yactt/internal/source"
)

// BenchmarkDiskCache_Put measures the encode + write+rename + (optional)
// evict path used on every Load's first-cache-miss.
//
// ponytail: reuses benchFile from cache_bench_test.go so the LRU and
// disk benches share the same fixture shape; that file is in the same
// package so the symbols are visible.
func BenchmarkDiskCache_Put(b *testing.B) {
	dir := b.TempDir()
	dc, err := cache.NewDiskCache(dir)
	if err != nil {
		b.Fatalf("NewDiskCache: %v", err)
	}
	f := benchFile("auth/login.go", 1)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := dc.Put(f); err != nil {
			b.Fatalf("Put: %v", err)
		}
	}
}

// BenchmarkDiskCache_Get measures the gob decode + mtime check + reparse
// path that fires on every Tier-1 hit. Hot path on second-and-subsequent
// Load of the same repo.
func BenchmarkDiskCache_Get(b *testing.B) {
	dir := b.TempDir()
	dc, err := cache.NewDiskCache(dir)
	if err != nil {
		b.Fatalf("NewDiskCache: %v", err)
	}
	const hot = "auth/login.go"
	f := benchFile(hot, 1)
	if err := dc.Put(f); err != nil {
		b.Fatalf("Put: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := dc.Get(hot, 1); err != nil {
			b.Fatalf("Get: %v", err)
		}
	}
}

// BenchmarkDiskCache_PutWithEviction stresses the evictIfOverBudget
// path. We put a million small entries under a tight cap and let the
// eviction loop run as it would on a real long-running yactt session.
//
// ponytail: drops the iteration count by 100× relative to the no-evict
// bench — the eviction cost is the interesting axis, not the put cost.
func BenchmarkDiskCache_PutWithEviction(b *testing.B) {
	dir := b.TempDir()
	// 8 MiB cap → with ~8 KB per entry, roughly 1000 entries fit. The
	// per-Put eviction scan walks the whole directory, so this bench
	// captures the steady-state cost of "always-on" LRU on disk.
	dc, err := cache.NewDiskCache(dir, cache.WithMaxBytes(8*1024*1024))
	if err != nil {
		b.Fatalf("NewDiskCache: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f := &source.File{
			Path:    uniquePath(i),
			Bytes:   benchFile("x", int64(i)).Bytes,
			MTime:   int64(i),
			Grammar: parser.Go{},
		}
		if err := dc.Put(f); err != nil {
			b.Fatalf("Put: %v", err)
		}
	}
}