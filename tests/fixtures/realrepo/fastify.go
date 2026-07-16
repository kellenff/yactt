// Package realrepo materialises pinned third-party repositories for
// real-world benchmarks. Clones land under tests/fixtures/.cache/
// (gitignored) so default CI stays offline after the first fetch.
package realrepo

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Fastify pin — github.com/fastify/fastify @ v5.9.0.
// Bump Tag + SHA together; the cache directory name includes the SHA
// so an old checkout is never silently reused after a pin change.
const (
	FastifyURL = "https://github.com/fastify/fastify.git"
	FastifyTag = "v5.9.0"
	FastifySHA = "2e45a4472fc83d1342661f4ec8a92334f46277c9"
)

// Fastify returns the absolute path of a local checkout of the pinned
// fastify/fastify commit. On first use it shallow-clones the tagged
// release into tests/fixtures/.cache/fastify-<sha>/; subsequent calls
// reuse the cache (offline-safe).
//
// Skips the calling test/benchmark when git is missing or the clone
// cannot be fetched (no network, firewall, etc.) and no cache exists.
func Fastify(t testing.TB) string {
	t.Helper()
	root, err := ensureGitClone(FastifyURL, FastifyTag, FastifySHA)
	if err != nil {
		t.Skipf("fastify real-repo fixture unavailable: %v", err)
	}
	return root
}

// ensureGitClone shallow-clones url at tag into .cache/<name>-<sha>
// and verifies HEAD matches wantSHA.
func ensureGitClone(url, tag, wantSHA string) (string, error) {
	cacheDir, err := cacheDir()
	if err != nil {
		return "", err
	}
	dest := filepath.Join(cacheDir, "fastify-"+wantSHA)

	if head, err := gitHEAD(dest); err == nil && head == wantSHA {
		return dest, nil
	}
	// Stale or partial checkout — remove and reclone.
	_ = os.RemoveAll(dest)
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir cache: %w", err)
	}
	if _, err := exec.LookPath("git"); err != nil {
		return "", fmt.Errorf("git not on PATH: %w", err)
	}
	cmd := exec.Command("git", "clone", "--depth", "1", "--branch", tag, url, dest)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		_ = os.RemoveAll(dest)
		return "", fmt.Errorf("git clone: %w\n%s", err, out)
	}
	head, err := gitHEAD(dest)
	if err != nil {
		_ = os.RemoveAll(dest)
		return "", err
	}
	if head != wantSHA {
		_ = os.RemoveAll(dest)
		return "", fmt.Errorf("cloned HEAD %s != pinned SHA %s (tag %s drifted?)", head, wantSHA, tag)
	}
	return dest, nil
}

func gitHEAD(repo string) (string, error) {
	cmd := exec.Command("git", "-C", repo, "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// cacheDir is tests/fixtures/.cache next to this package.
func cacheDir() (string, error) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("runtime.Caller failed")
	}
	// realrepo/ → fixtures/ → .cache/
	return filepath.Join(filepath.Dir(thisFile), "..", ".cache"), nil
}
