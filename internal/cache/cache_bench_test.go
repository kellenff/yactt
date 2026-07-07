package cache_test

import (
	"testing"
	"time"

	"github.com/kellenff/yactt/internal/cache"
	"github.com/kellenff/yactt/internal/parser"
	"github.com/kellenff/yactt/internal/source"
)

// benchFile constructs an in-memory source.File for use as cache fodder.
// ponytail: tiny stub tree-sitter root via nil — cache_test.go doesn't
// dereference the root, only round-trips the struct.
func benchFile(path string, mtime int64) *source.File {
	return &source.File{
		Path:    path,
		Bytes:   []byte("package auth\n\n// Login is the auth entrypoint.\nfunc Login() error { return nil }\n"),
		MTime:   mtime,
		Root:    nil,
		Grammar: parser.Go{},
	}
}

// BenchmarkLRU_Put populates the file-tier with N entries under a tight
// cap, forcing the LRU eviction path on every Put once N exceeds cap.
// Captures real-world pressure: a multi-thousand-file Load with a small
// fileCap.
func BenchmarkLRU_Put(b *testing.B) {
	const cap = 1024
	// ponytail: deliberately under sized so we measure insert+evict, not
	// just insert. Add 50% headroom to amortise the eviction fan-out.
	c := cache.NewSized(cap, cap)
	now := time.Now().UnixNano()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f := benchFile(uniquePath(i), now+int64(i))
		c.PutFile(f)
	}
}

// BenchmarkLRU_Get hits the file-tier with a single entry cached and N
// lookups against it — measures lookup+eviction-list-update, not insert.
func BenchmarkLRU_Get(b *testing.B) {
	const cap = 1024
	c := cache.NewSized(cap, cap)
	const hot = "auth/login.go"
	c.PutFile(benchFile(hot, 1))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.GetFile(hot, 1); err != nil {
			b.Fatalf("GetFile: %v", err)
		}
	}
}

// BenchmarkLRU_PutLayer and BenchmarkLRU_GetLayer exercise the layer-tier,
// which is keyed by (nodeID, layer) and stores opaque bytes — the shape
// used by Tier 2 of node_get (signature/body/source hydrated layers).
func BenchmarkLRU_PutLayer(b *testing.B) {
	c := cache.NewSized(1024, 1024)
	payload := []byte(`{"name":"Login","params":[…],"returns":"error"}`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.PutLayer("fn:auth.Login", "signature", payload)
	}
}

func BenchmarkLRU_GetLayer(b *testing.B) {
	c := cache.NewSized(1024, 1024)
	payload := []byte(`{"name":"Login","params":[…],"returns":"error"}`)
	c.PutLayer("fn:auth.Login", "signature", payload)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.GetLayer("fn:auth.Login", "signature"); err != nil {
			b.Fatalf("GetLayer: %v", err)
		}
	}
}

// uniquePath returns a stable-but-distinct path for benchmarking. The
// cache treats paths as map keys, so collisions would skew the bench.
func uniquePath(i int) string {
	return "auth/file_" + itoa(i) + ".go"
}

// itoa avoids importing strconv just for one micro-bench helper. Hot
// enough to matter at -benchtime=10s × millions of iters.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := [20]byte{}
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}
