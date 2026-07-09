// Package config is the home for YACTT_* environment-variable knobs.
//
// Scope:
//   - Reads YACTT_-prefixed env vars and returns typed values with
//     sensible defaults.
//   - Writes a one-line warning to stderr when an env var is set but
//     unparseable, then falls back to the default. Silent fallback was
//     the historical behaviour; the warning is strictly additive and
//     catches typos like YACTT_DISK_CACHE_MAX_BYTES=512mb.
//
// Out of scope:
//   - XDG_* vars (XDG_CACHE_HOME, XDG_DATA_HOME). These are freedesktop
//     standards, not yactt configuration; the packages that compute
//     XDG-derived paths own those reads.
//   - Config files (YAML/JSON/TOML). Add when a real caller needs them.
//   - Live reload / fsnotify. Add when a real caller needs it.
//
// Design:
//   - Free functions, lazy reads. Each call re-parses the env var; the
//     cost is a single os.Getenv + strconv.ParseInt (~1µs) and matches
//     the pre-existing call-site pattern (cache.go read the env at
//     LoadOptsWithDiskCache time, not at process start).
//   - No global state, no sync.Once caching. Tests use t.Setenv; any
//     caching would make that fragile without a real performance win.
//   - The default for each knob is exported as a public constant so
//     CLI help text and tests can reference it without re-deriving.
package config

import (
	"fmt"
	"os"
	"strconv"
)

// DefaultDiskCacheMaxBytes is the per-repo disk cache cap when
// $YACTT_DISK_CACHE_MAX_BYTES is unset. 512 MiB fits a small/medium
// repo's parsed file cache comfortably; set the env var to 0 for
// unlimited growth.
const DefaultDiskCacheMaxBytes int64 = 512 * 1024 * 1024

// DiskCacheMaxBytes returns the per-repo disk cache cap.
//
// Honours $YACTT_DISK_CACHE_MAX_BYTES. Returns DefaultDiskCacheMaxBytes
// when the var is unset, and falls back to the default (with a stderr
// warning) when the var is set but not a valid non-negative integer.
// A value of 0 means "unlimited".
func DiskCacheMaxBytes() int64 {
	const env = "YACTT_DISK_CACHE_MAX_BYTES"
	v := os.Getenv(env)
	if v == "" {
		return DefaultDiskCacheMaxBytes
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		fmt.Fprintf(os.Stderr, "config: %s=%q invalid, using default %d\n", env, v, DefaultDiskCacheMaxBytes)
		return DefaultDiskCacheMaxBytes
	}
	return n
}
