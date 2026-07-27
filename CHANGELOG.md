# Changelog

## Unreleased

## 0.0.14 — 2026-07-26

### Added

- **`Dockerfile` and `.containerignore`** for building the yactt MCP
  HTTP daemon into a container image (Apple `container` / Docker;
  linux/arm64). Defaults to the latest GitHub release; override with
  `--build-arg YACTT_VERSION=vX.Y.Z` to pin a specific tag.

## 0.0.12 — 2026-07-20

### Added

- **Persistent in-process project indexing.** `RegisterAllTools` binds a
  `project.Index` to the registry so `project.Resolve` reuses a pinned
  `*store.Repo` (symbol table, call-edge graph, LSP clients) across tool
  calls. `index_repository` primes the Index; `delete_project` evicts it.
  Callers keep `defer repo.Close()` — Close is a no-op on pinned repos;
  ForceClose runs on eviction / daemon shutdown. This is the parser-warm
  path for `yactt mcp serve` / `mcp serve-http` that the file:// migration
  deferred when it moved to per-call Resolve.
- **Persistent macOS MCP service (Claude Code plugin).** SessionStart
  installs/reconciles a user LaunchAgent that runs `yactt mcp serve-http`,
  points Claude at the warm HTTP endpoint, and only restarts when the
  binary or plist changes.
- HTTP MCP benchmarks for small/medium projects, external Go HTTP clients,
  and a fastify fixture.

### Changed

- Default LRU cache caps raised for monorepo-scale workloads.
- `CachedFile` no longer reparses already-warm files.

### Fixed

- Plugin release detection uses the absolute install path so LaunchAgent
  reconciliation stays stable across upgrades.

## 0.0.11 — 2026-07-14

### Breaking changes (MCP project-reference migration)

The optional positional path argument on `yactt mcp serve` is
**removed**. The target project is now identified by a `file://`
URI that every targeting tool takes in its args.

**Migration:**

| Before | After |
|---|---|
| `yactt mcp serve /abs/path` | `yactt mcp serve` |
| `{"path": "/abs/path"}` in `index_repository` / `index_status` / `delete_project` | `{"project": "file:///abs/path"}` |
| (no `project` field on code-intel tools) | `{"project": "file:///abs/path", ...}` (required on every code-intel tool) |
| `{"repo": "/abs/path"}` on `tree_overview` (decorative) | `{"project": "file:///abs/path"}` (deprecated `repo` accepted for one release, stderr notice emitted) |
| `{"id": "onboarding"}` on `persisted_query` | `{"op": "onboarding", "project": "file:///abs/path"}` |

**Other changes:**

- The startup audit line and TOFU check now run on the first
  successful `index_repository` per process (memoised), not on
  `yactt mcp serve` boot. Servers that don't call `index_repository`
  produce no audit output — same as the pre-migration registry mode.
- `junie-extension/scripts/yactt-launcher.sh` no longer resolves
  the project root via `git rev-parse`. Agents call
  `index_repository` explicitly with their chosen `file://` URI.
- `get_graph_schema` no longer takes a `*store.Repo` constructor
  argument (it was always unused; drop simplifies the tool map).
- `tree_overview`'s `repo` field is a deprecated alias kept
  through **v0.2.0** (one release). Use `project`; a stderr
  notice fires on every use. The alias will be removed in v0.2.0.

**Why now:** unifies the wire shape across the registry tools
(`index_repository`, `index_status`, `delete_project`) and the
code-intel tools, instead of leaving a long-tail of mixed shapes
that agents have to learn.

See `docs/snowball/specs/2026-07-12-mcp-cli-project-reference-design.md`
for the full design and rationale.
