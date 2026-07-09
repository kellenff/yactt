package config_test

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/kellenff/yactt/internal/config"
)

// captureStderr redirects os.Stderr to a buffer for the duration of f,
// then restores the original. Tests use it to assert on the warning
// emitted for unparseable values.
func captureStderr(t *testing.T, f func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = orig })

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	f()
	_ = w.Close()
	return <-done
}

func TestDiskCacheMaxBytes_Default(t *testing.T) {
	// No t.Setenv: relies on the env not being set in the test runner.
	// CI environments don't set this var; if a developer's shell does,
	// the test isolates via t.Setenv("YACTT_DISK_CACHE_MAX_BYTES", "").
	t.Setenv("YACTT_DISK_CACHE_MAX_BYTES", "")
	if got, want := config.DiskCacheMaxBytes(), config.DefaultDiskCacheMaxBytes; got != want {
		t.Errorf("DiskCacheMaxBytes() = %d, want default %d", got, want)
	}
}

func TestDiskCacheMaxBytes_ValidValue(t *testing.T) {
	t.Setenv("YACTT_DISK_CACHE_MAX_BYTES", "1073741824") // 1 GiB
	if got, want := config.DiskCacheMaxBytes(), int64(1<<30); got != want {
		t.Errorf("DiskCacheMaxBytes() = %d, want %d", got, want)
	}
}

func TestDiskCacheMaxBytes_ZeroMeansUnlimited(t *testing.T) {
	t.Setenv("YACTT_DISK_CACHE_MAX_BYTES", "0")
	if got, want := config.DiskCacheMaxBytes(), int64(0); got != want {
		t.Errorf("DiskCacheMaxBytes() = %d, want 0 (unlimited)", got)
	}
}

func TestDiskCacheMaxBytes_InvalidFallsBackWithWarning(t *testing.T) {
	t.Setenv("YACTT_DISK_CACHE_MAX_BYTES", "512mb") // strconv.ParseInt rejects
	warned := captureStderr(t, func() {
		got := config.DiskCacheMaxBytes()
		if got != config.DefaultDiskCacheMaxBytes {
			t.Errorf("DiskCacheMaxBytes() = %d, want default %d after invalid input", got, config.DefaultDiskCacheMaxBytes)
		}
	})
	if !strings.Contains(warned, "YACTT_DISK_CACHE_MAX_BYTES") {
		t.Errorf("warning missing env var name; got stderr=%q", warned)
	}
	if !strings.Contains(warned, "512mb") {
		t.Errorf("warning missing the bad value; got stderr=%q", warned)
	}
}

func TestDiskCacheMaxBytes_NegativeFallsBackWithWarning(t *testing.T) {
	t.Setenv("YACTT_DISK_CACHE_MAX_BYTES", "-1")
	warned := captureStderr(t, func() {
		got := config.DiskCacheMaxBytes()
		if got != config.DefaultDiskCacheMaxBytes {
			t.Errorf("DiskCacheMaxBytes() = %d, want default %d after negative input", got, config.DefaultDiskCacheMaxBytes)
		}
	})
	if !strings.Contains(warned, "-1") {
		t.Errorf("warning missing the bad value; got stderr=%q", warned)
	}
}

func TestDiskCacheMaxBytes_ValidValueNoWarning(t *testing.T) {
	t.Setenv("YACTT_DISK_CACHE_MAX_BYTES", "0")
	warned := captureStderr(t, func() {
		_ = config.DiskCacheMaxBytes()
	})
	if warned != "" {
		t.Errorf("unexpected stderr on valid input: %q", warned)
	}
}
