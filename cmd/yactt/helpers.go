package main

import (
	"fmt"
	"os"

	"github.com/kellenff/yactt/internal/audit"
	"github.com/kellenff/yactt/internal/parser"
	"github.com/kellenff/yactt/internal/registry"
	"github.com/kellenff/yactt/internal/store"
)

// buildStartupInfo snapshots the load-time state into the audit
// startup record. Used by the MCP server's emitStartup hook so the
// per-process startup line carries the same shape regardless of
// whether the run is stdio or HTTP.
//
// ponytail: MaxFiles uses the package default rather than peeking
// the closure-supplied override. WithMaxFiles captures into an
// unexported field, and the only clean alternatives (exposing
// loadOptions or a Probe() interface on LoadOption) are heavier
// than the value: nobody today sets a non-default cap in production
// either. Lift this when the audit needs to faithfully report a
// CLI-supplied cap.
func buildStartupInfo(repo *store.Repo, opts []store.LoadOption, binSHA string) audit.Startup {
	_ = opts
	grammars := make([]string, 0)
	for _, l := range parser.All() {
		grammars = append(grammars, string(l.Name()))
	}
	var lspEntries []audit.LSPEntry
	for _, l := range parser.All() {
		_, toolName, ver := repo.LSPForLang(l.Name())
		lspEntries = append(lspEntries, audit.LSPEntry{
			Language: string(l.Name()),
			Tool:     toolName,
			Version:  ver,
		})
	}
	return audit.Startup{
		Version:      version,
		RepoRoot:     repo.Root(),
		MaxFiles:     store.DefaultMaxFiles,
		LoadedFiles:  len(repo.Files()),
		Grammars:     grammars,
		LSP:          lspEntries,
		BinarySHA256: binSHA,
	}
}

// warnInstallTrustChain reads the TOFU file the install hook writes
// and emits a stderr warning when the running binary's SHA-256
// diverges from the recorded hash at the same version. Missing TOFU
// is a no-op; a version mismatch (legitimate upgrade) is also a no-op.
func warnInstallTrustChain(currentVersion, currentSHA string) {
	if currentVersion == "dev" {
		// Dev build — TOFU is meaningless (no release artifact
		// identity to compare against). Skip silently.
		return
	}
	if currentSHA == "" {
		return
	}
	res, err := audit.CheckKnownGood(audit.KnownGoodPath(), currentVersion, currentSHA)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: install trust chain: %v\n", err)
		return
	}
	switch res.Status {
	case "mismatch":
		fmt.Fprintf(os.Stderr,
			"WARNING: yactt binary SHA-256 does not match the install hook's TOFU record\n"+
				"  recorded: %s\n"+
				"  actual:   %s\n"+
				"  version:  %s\n"+
				"  likely a replay or compromised release — refusing to trust the install\n",
			res.Expected, res.Actual, res.Version)
	}
	// "match", "missing", "unknown_version" all silent — they're
	// legitimate states.
}

// binaryPath returns the absolute path of the running executable.
// Resolves /proc/self/exe on Linux, falls back to os.Args[0] (which
// is fine for our use: we hash the bytes that get executed, not the
// path string).
func binaryPath() string {
	if p, err := os.Executable(); err == nil && p != "" {
		return p
	}
	return os.Args[0]
}

// loadOptsWithDiskCache is a thin wrapper around
// registry.LoadOptsWithDiskCache. The cmd tree keeps its name to
// preserve the existing call sites; the canonical implementation
// lives in internal/registry so the index_repository tool can
// share it.
func loadOptsWithDiskCache(repoRoot string) []store.LoadOption {
	return registry.LoadOptsWithDiskCache(repoRoot)
}
