# Security & supply-chain trust chain

> **Scope.** This document describes how yactt defends the four trust
> boundaries it actually crosses: Go-module fetches at build time, the
> `SessionStart` install hook at install time, the GitHub Release artifact
> itself, and the LSP subprocess boundary at runtime. It is a counterpart to
> the OWASP Agentic Skills Top 10 review — see Issue #1 for the AST02
> (supply chain) entry that motivated this doc.

## Threat model (one paragraph)

yactt is an MCP server that reads source code. The attacker we plan against
is a third party who has compromised *something* yactt transitively depends
on — a grammar binding, an LSP binary, or the GitHub Releases endpoint — and
wants to turn that compromise into code execution inside the MCP server
(grammar / Go module) or inside the developer's shell (install hook / LSP
subprocess). The trust chain below narrows each of those paths to a single,
auditable anchor.

## 1. Grammar + Go-module supply chain

### What is pinned

`go.mod` requires `github.com/smacker/go-tree-sitter` at the pseudo-version
`v0.0.0-20240827094217-dd81d9e9be82`. That pseudo-version is a literal
commit hash (`dd81d9e9be82`) of the upstream `smacker/go-tree-sitter`
repository, not a floating tag. The three grammar bindings yactt actually
loads — `.../golang`, `.../javascript`, `.../typescript/typescript` — live as
subpackages of that same module, so they share the commit pin and are
equivalently pinned.

### What enforces it in CI

`.github/workflows/ci.yml` runs two checks on every PR:

1. `go mod verify` — re-checks every entry in `go.sum` against the on-disk
   module cache. A tampered `go.sum`, a partial download, or a proxy that
   served a different module than was requested fails this check.
2. A grep that refuses `replace` directives in `go.mod`. A `replace` clause
   can silently re-route a pinned dependency to a fork; the workflow treats
   any `replace` line as a CI failure, regardless of target.

### What it does NOT defend against

A successful `git push` from a maintainer with commit access who has been
socially engineered into loosening the pin. Out of scope for tooling — see
the governance / docs-policy issue (Issue #4) for that surface.

## 2. GitHub Release artifact integrity

The release workflow (`.github/workflows/release.yml`) ships yactt across
five GOOS/GOARCH targets. Each one is gated twice:

1. **SHA256SUMS** — `sha256sum -- *.tar.gz > SHA256SUMS` is generated from
   the *same* tarball artifacts that are uploaded to the release. The
   `install.sh` hook downloads `SHA256SUMS` first and refuses to install a
   tarball whose hash doesn't match an entry. SHA256SUMS catches corruption
   and same-version replay (a swapped binary at the same version).
2. **SLSA Build Provenance Level 3** — the `actions/attest@v4` step signs an
   in-toto statement with the workflow's OIDC token via Fulcio and registers
   it in GitHub's attestation store, indexed by the tarball's SHA-256. This
   proves "this binary was built from this repo at this commit by this
   workflow" — closing the gap that SHA256SUMS alone can't (an attacker who
   compromises the GitHub Releases endpoint could publish a matching
   (binary, hash) pair; the attestation binds the binary to the source).

### Release-time verification

The release job re-verifies every attestation immediately before publishing,
via `gh attestation verify <tarball> --repo kellenff/yactt`. Any mismatch
fails the release job — the artifact is not shipped. This is the read-side
counterpart to the build-time `attest` step and catches the case where an
attestation has been removed or replaced between build and release.

### Install-time verification (the consumer side)

`plugins/yactt/scripts/install.sh` does its own checks on every
`SessionStart`:

- Strict semver allowlist on the tag name returned by the GitHub API (no
  shell-injectable path through `v${version}`).
- SHA256SUMS-first download order — refuses to download a tarball whose
  hash doesn't appear in the manifest.
- TOFU record at `~/.local/share/yactt/known-good` (or `$XDG_DATA_HOME/...`).
  Same version + different sha256 = refused. This catches a same-version
  replay against the releases endpoint.

### What it does NOT defend against

A compromised maintainer pushing a *new* malicious release. The TOFU check
treats a new version with a new sha256 as a legitimate upgrade — that is
intentional, otherwise legitimate upgrades are impossible. The user-facing
mitigation is the explicit `gh attestation verify` command documented in
the plugin README, which lets a downstream user verify provenance out-of-band
before installing.

## 3. LSP subprocess boundary

yactt shells out to `gopls` and `typescript-language-server` opportunistically
(via `internal/lsp/client.go`). Both binaries are invoked with
`os/exec.Command` and inherit the parent's stdin/stdout/stderr plus the
parent's full filesystem privileges.

### Why this is a trust boundary

An attacker who can subvert either LSP server — by replacing the binary on
the user's PATH, by exploiting a flaw in the LSP server that yields code
execution, or by feeding yactt a malicious JSON-RPC message that the server
forwards somewhere unsafe — gains the effective read scope of the user
running yactt.

### What defends it today

- yactt does **not** auto-install LSP servers. If `gopls` /
  `typescript-language-server` are not on PATH, the LSP client returns
  `ErrUnsupported` and yactt falls back to tree-sitter-only extraction. The
  MCP server stays up; only the semantic-depth layer is unavailable.
- The subprocess inherits yactt's UID — it has no privileges yactt itself
  doesn't already have. There is no `sudo`, no `setuid` wrapper, no keychain
  access.
- JSON-RPC framing is strictly typed (`internal/lsp/types.go`); unknown
  fields are ignored, not echoed back. A malicious server cannot induce
  yactt to execute arbitrary `os/exec` calls — the only commands yactt
  itself spawns are the well-known LSP binaries.

### What it does NOT defend against (yet)

- No signature check on the LSP binary itself. `gopls` and
  `typescript-language-server` are trusted on PATH, same as the user trusts
  any other tool on their machine. A `cosign verify` gate on the LSP
  subprocess launch is the next step; tracked outside this issue.
- The subprocess inherits the full environment. If yactt is run with a
  sensitive `*_TOKEN` in env, the LSP server sees it too. Mitigation is
  process-level: don't run yactt under a shell with secrets in env.

## 4. Build-time CVE gate

`.github/workflows/ci.yml` runs `govulncheck ./...` on every PR against the
Go vulnerability database. Any known CVE in a symbol yactt actually
imports fails the build. govulncheck is installed at the pinned module tag
`golang.org/x/vuln/cmd/govulncheck@v1.1.4` — Go module tags are immutable,
so the @tag is itself the audit anchor (no SHA needed).

## Summary table

| Path                         | Integrity gate                          | Where it lives                |
|------------------------------|-----------------------------------------|-------------------------------|
| Tree-sitter grammar bindings | `go mod verify` + no-`replace` grep     | `ci.yml` → `go mod verify`    |
| Go-module CVEs               | `govulncheck ./...`                     | `ci.yml` → `vuln` job         |
| Release tarball (build)      | SHA256SUMS + SLSA L3 attestation        | `release.yml` → `build` job   |
| Release tarball (publish)    | `gh attestation verify` re-check       | `release.yml` → `release` job |
| Install (consumer)           | SHA256SUMS + TOFU + semver allowlist    | `plugins/yactt/scripts/install.sh` |
| LSP subprocess               | No auto-install; trust-on-PATH         | `internal/lsp/client.go`      |