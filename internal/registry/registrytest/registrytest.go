// Package registrytest provides shared registry seeding helpers
// for tests that need a *registry.Registry bound to a known
// project root.
//
// The helpers used to be duplicated across three layers:
//
//   - internal/tool/helpers_test.go (seedRegFromRepo, seedRegForRoot)
//   - tests/fidelity/fidelity_test.go (seedRegForFidelity)
//   - tests/acceptance/acceptance_test.go (seedRegForProject)
//
// They all built the same shape: a fresh registry on disk under
// t.TempDir() with one entry for the given absolute root. Pulling
// them into a public helper package lets every test seed a
// registry with one line, and keeps the on-disk shape
// (TempDir+projects.json, Files=0, IndexedAt=now) consistent
// across the suite.
//
// ponytail: a public helper package is OK here because nothing in
// the production code paths imports it; it stays out of compiled
// binaries unless a test asks for it.
package registrytest

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/kellenff/yactt/internal/registry"
)

// Seed creates a fresh *registry.Registry under tb.TempDir() with
// one entry for `root`. The returned registry is wired the same
// way the MCP server wires it: a projects.json file under
// TempDir, one entry per absolute path, Files=0 (we don't count
// here because the handler's intent is "this path is indexed"
// not "this path is fresh").
//
// Use SeedFromRepo to skip the explicit Root() lookup when you
// already have a loaded *store.Repo (or anything that exposes a
// Root() string method — the type is structural so a stub is
// accepted in unit tests).
func Seed(tb testing.TB, root string) *registry.Registry {
	tb.Helper()
	dir := tb.TempDir()
	reg := registry.New(filepath.Join(dir, "projects.json"))
	if err := reg.Upsert(registry.Entry{
		Name:      filepath.Base(root),
		Path:      root,
		IndexedAt: time.Now().UTC(),
		Files:     0,
	}); err != nil {
		tb.Fatalf("registrytest.Seed: upsert: %v", err)
	}
	return reg
}

// SeedFromRepo is the convenience wrapper that takes anything
// exposing a Root() string. Use it right after a
// `r, _, err := store.Load(...)` call in tests so callers don't
// have to write repo.Root() twice.
func SeedFromRepo(tb testing.TB, repo interface{ Root() string }) *registry.Registry {
	return Seed(tb, repo.Root())
}