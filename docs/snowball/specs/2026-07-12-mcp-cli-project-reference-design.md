# Plan: yactt MCP CLI project reference (deprecate the positional path)

> **Status:** proposed (pre-implementation). Branch: `mcp-cli-project-reference`.
> Branched from main at `cc2c5e4` (hybrid: attach chunk payloads to
> structural channel hits).

## 1. Goal

Replace the optional positional path argument on `yactt mcp serve` with a
`file://` URI that every targeting tool takes in its args. The server
becomes stateless across tool calls: no repo loaded at boot, every
targeting tool call resolves the URI to a path, looks it up in the
registry, loads (or hits the disk cache for), and serves the result.

After the change, the MCP server runs in what used to be called
"registry mode" by default. The single-repo boot path is gone. The
file:// URI is the canonical project reference on the wire; the
on-disk registry keyed by absolute path stays the source of truth.

## 2. Decisions (locked in)

1. **Wire shape:** tools that target a project take
   `{"project": "<file:// URI>", ...}`. The URI must decode to an
   absolute filesystem path.
2. **Lifecycle:** resolve-and-load on every call. No in-memory repo
   cache; the existing per-project disk cache is the warm path.
3. **Scope:** full unification — all 15 code-intel tools + 3 targeting
   registry tools take `project`. `list_projects` and `get_graph_schema`
   are registry-style (no project target).
4. **LSP client lifetime on cold paths:** deferred to a future daemon /
   HTTP server mode. The MVP ships with the cold-call latency cost on
   the first `index_repository` after a repo changes; warm cache hits
   pay only the disk-cache deserialization cost.
5. **`tree_overview` legacy `repo` field:** accepted as a deprecated
   alias for `project` for one release. A per-tool stderr notice fires
   when the alias is used. Removed in the next release.

## 3. What already exists (no new parsing needed)

| Need | Source today | Notes |
|---|---|---|
| Per-project disk cache | `internal/registry/cache.go::LoadOptsWithDiskCache` | key is `sha256(repoRoot)[:16]`; the resolver returns the cleaned path so the key stays stable |
| Project book | `internal/registry/registry.go` | canonical key is `Entry.Path` (absolute path) |
| Tool factory pattern | `internal/tool/*.go::Foo(repo *store.Repo) Handler` | factories change to take `*registry.Registry` instead of `*store.Repo` |
| Audit + TOFU emit | `cmd/yactt/main.go::runMCPServe` | boot-time today; moves to first `index_repository` per session |

## 4. New package: `internal/project`

The seam between MCP dispatch and repo loading. Lives at
`internal/project/` with `project.go` (surface) and `project_test.go`
(unit tests).

```go
// Package project parses the on-wire project reference (a file://
// URI) and resolves it to a loaded *store.Repo via the registry and
// the per-project disk cache.
package project

import (
    "github.com/kellenff/yactt/internal/registry"
    "github.com/kellenff/yactt/internal/store"
)

// Ref is a parsed file:// URI. The wrapper exists so future schemes
// (git://, https://) can grow without a v2 wire shape; today the
// only scheme accepted is file://.
type Ref struct {
    Path string // decoded, cleaned, absolute filesystem path
}

// ParseRef parses a file:// URI. Rejects:
//   - empty / whitespace-only input
//   - non-file:// schemes (e.g. git://, https://)
//   - non-empty authority (e.g. file://host/path)
//   - relative paths (the URI must encode an absolute path)
//   - percent-encoded path-traversal segments (e.g. %2e%2e)
func ParseRef(raw string) (Ref, error)

// Resolve parses `raw`, looks the decoded path up in `reg`, and
// loads the repo via the per-project disk cache. The caller owns
// repo.Close() (use defer).
//
// Errors:
//   - ParseRef errors (see above)
//   - ErrNotIndexed when the path is absent from the registry
//   - any error from store.Load
//
// ponytail: no context.Context today because store.Load is sync.
// When the future daemon mode (§12 Risk 1) lands, add a ctx
// parameter so cancellation propagates from the server loop.
func Resolve(reg *registry.Registry, raw string) (*store.Repo, error)

// Sentinel errors. Handlers wrap them with the offending URI for
// machine-readable error envelopes.
var (
    ErrEmpty             = errors.New("project: empty reference")
    ErrUnsupportedScheme = errors.New("project: only file:// URIs are accepted")
    ErrNonLocal          = errors.New("project: file:// URI must have empty authority (local files only)")
    ErrNotAbsolute       = errors.New("project: file:// URI must encode an absolute path")
    ErrNotIndexed        = errors.New("project: not in registry; call index_repository first")
)
```

`ParseRef` implementation notes:
- Use `net/url.Parse` to handle percent-decoding and authority parsing.
- Reject any URL whose `Scheme != "file"`.
- Reject any URL whose `Host != ""` (non-local authority).
- After `filepath.Clean(filepath.Join("/", url.Path))` on the
  percent-decoded path, reject any path containing `..` segments
  (defence in depth against percent-encoded traversal).
- Return `Ref{Path: filepath.Clean(decoded)}`.

`Resolve` implementation notes:
- Calls `ParseRef(raw)`.
- Calls `reg.GetByPath(parsed.Path)` — returns `ErrNotIndexed` if absent.
- Calls `store.Load(parsed.Path, registry.LoadOptsWithDiskCache(parsed.Path)...)`.

## 5. Data flow

```
yactt mcp serve                          (no args)
   │
   ├─ open registry file at $XDG_CACHE_HOME/yactt/projects.json
   ├─ register 17 tools + persisted_query (each tool factory takes *registry.Registry)
   └─ start stdio JSON-RPC loop

... tools/call arrives:

mcp.Server dispatches to tool.TreeOverview(reg)(ctx, args)
   │
   ├─ handler unmarshals args into TreeOverviewArgs{Project, Scope, Depth, ...}
   │     (or {Repo deprecated alias} → wrapped to Project, stderr notice)
   ├─ calls project.Resolve(reg, a.Project)
   │     │
   │     ├─ ParseRef("file:///abs/path") → Ref{Path: "/abs/path"}
   │     │     rejects: empty, non-file scheme, non-empty authority,
   │     │              relative path, percent-encoded traversal
   │     │
   │     ├─ reg.GetByPath("/abs/path") → Entry
   │     │     missing → ErrNotIndexed("call index_repository first")
   │     │
   │     └─ store.Load("/abs/path", LoadOptsWithDiskCache("/abs/path")...)
   │           └─ hits disk cache for warm repos, full load for cold ones
   │
   ├─ defer repo.Close() — releases LSP clients, disk cache stays warm
   ├─ run the existing logic, unchanged, against the loaded repo
   └─ return result
```

## 6. Schema changes

### 6.1 Tools that gain a `project` field (15 — all repo-bound)

| File | Tool |
|---|---|
| `treeoverview.go` | `tree_overview` (renames existing `repo` → `project`; legacy alias below) |
| `getnode.go` | `node_get` |
| `nodesource.go` | `node_source` |
| `nodeedges.go` | `node_edges` |
| `search_tool.go` | `search` |
| `editimpact.go` | `edit_impact` |
| `findsymbol.go` | `find_symbol` |
| `getsymbolsoverview.go` | `get_symbols_overview` |
| `findcode.go` | `find_code` |
| `searchcode.go` | `search_code` |
| `findreferencingsymbols.go` | `find_referencing_symbols` |
| `getcodesnippet.go` | `get_code_snippet` |
| `architecture.go` | `get_architecture` |
| `querygraph.go` | `query_graph` |
| `detectchanges.go` | `detect_changes` |

Standard fragment added to each schema's `properties`:

```json
"project": {
  "type": "string",
  "description": "Absolute path as a file:// URI (e.g. file:///abs/path). Must be in the registry; call index_repository first."
}
```

Each schema's `required` array gains `"project"` alongside any
existing required fields. `additionalProperties: false` stays.

### 6.2 `tree_overview` legacy alias (one release only)

Schema declares `project` as required and `repo` as an optional
deprecated string alias. When the handler sees `repo` (without
`project`), it emits to stderr:

```
deprecation: tree_overview's 'repo' field is renamed to 'project' (file:// URI); will be removed in the next release
```

…and treats `repo`'s value as a raw absolute path, wrapping it as
`file://<value>` before passing to `project.Resolve`. When both
fields are present, `project` wins and the notice is suppressed.

The `repo` alias is removed in the next release after this design
lands. README and the spec's "Risks" section call this out.

### 6.3 Registry tools that rename `path` → `project` (3)

| File | Tool |
|---|---|
| `index_repository.go` | `index_repository` |
| `index_status.go` | `index_status` |
| `delete_project.go` | `delete_project` |

Description on each becomes "Absolute path as a file:// URI".
`list_projects` output is unchanged (raw `path` field).

### 6.4 `persisted_query` schema

```json
{
  "type": "object",
  "required": ["op", "project"],
  "properties": {
    "op":      { "type": "string", "description": "Registered op id." },
    "project": { "type": "string", "description": "Absolute path as a file:// URI. Injected into the called tool's args before dispatch." }
  },
  "additionalProperties": false
}
```

The runner (in `internal/persisted/registry.go`) gets one small
extension: if the called tool's args declare a `project` field and
the op's static args don't supply one, inject the
`persisted_query` caller's `project` value before dispatch. Same
merge shape as today's op-args merging; one extra layer.

### 6.5 Example ops rewrite (`internal/persisted/example_ops.go`)

| Op id | Old `args` | New `args` |
|---|---|---|
| `onboarding` | `{"repo":"","depth":2}` | `{"depth":2}` (project injected by runner) |
| `repo-map` | `{"repo":"","depth":1}` | `{"depth":1}` |
| `architecture` | unchanged | unchanged (project injected) |
| `graph_rag_demo` | unchanged | unchanged |

### 6.6 `get_graph_schema` becomes registry-style

The factory currently takes `*store.Repo` purely for "registration
uniform" but ignores it. Drop the parameter:

```go
// Before:
func GetGraphSchema(repo *store.Repo) func(ctx context.Context, args json.RawMessage) (any, error)

// After:
func GetGraphSchema() func(ctx context.Context, args json.RawMessage) (any, error)
```

Schema and result shape unchanged.

## 7. CLI

- `yactt mcp serve` — drops the positional path. New usage line:
  ```
  yactt mcp serve [--audit-log=F]   Run the MCP server on stdio. Tools accept
                                    a file:// project URI; call index_repository
                                    first to register a repo.
  ```
- `yactt mcp serve <anything>` — hard error:
  `"mcp serve: no longer takes a positional path; tools accept a file:// project URI in their args"`.
  No silent fallback, no deprecation window.
- `yactt overview <path>`, `yactt chunk --repo <path>`,
  `yactt hybrid --repo <path>` — unchanged. These are CLI subcommands,
  not MCP.

## 8. Integration updates

- `junie-extension/scripts/yactt-launcher.sh` — drop the
  `find_project_root` function and the trailing `"$(find_project_root)"`
  arg. Becomes:
  ```bash
  exec yactt mcp serve
  ```
  Comment updates: explain that Junie's agent calls
  `index_repository {project: "file://..."}` first, then any
  code-intel tool with the same URI.
- `junie-extension/test/launcher.test.sh` — the
  `YACTT_LAUNCHER_DRY_RUN=1` mode previously asserted a resolved
  path. After: assert that the launcher no longer emits a resolved
  path (or prints a clear "removed" message). The smoke test's
  purpose shifts to "verifies `yactt mcp serve` is the exec target".
- `pi-extension/index.js` — already passes `args: ["mcp", "serve"]`;
  no change.
- `tests/skill-triggering/drivers/pi-mcp.json` — no change.
- `tests/skill-triggering/drivers/junie.sh` — no change (delegates
  to the launcher).
- `.mcp.json` at repo root — verify; drop the positional path if
  present.

## 9. Audit + TOFU placement

The startup audit line and TOFU check today run at boot in
single-repo mode. After:

- **Startup audit line** — emitted on first `index_repository`
  success per session. Carries the same `repo_root`, `loaded_files`,
  `grammars`, `lsp`, `binary_sha256` fields it does today.
- **TOFU check** — runs once on first `index_repository` call
  (before the load), per process. If the agent never calls
  `index_repository` in a session, neither runs (the binary never
  loaded a repo either).

Both move into `tool.IndexRepository`'s handler, with
`audit.EmitStartup` and `warnInstallTrustChain` called in that
order before `store.Load`. Concretely, `IndexRepository`'s factory
gains two extra parameters (or a small `IndexHooks` struct):
`emitStartup func(audit.Startup) error` and `warnTrust func()`.
The cmd-level wiring in `cmd/yactt/main.go` builds the closures
that bind to `version`, `binaryPath()`, and the running binary's
SHA-256. The `audit.ExtractPaths` extraction for the per-call
audit log continues to look for `project` (URI) in tool args and
unwraps to a path.

## 10. Testing

### 10.1 New unit tests (`internal/project/project_test.go`)

- `TestParseRef_Valid` — canonical local (`file:///abs/path`),
  paths with spaces (`file:///abs/path/with/%20space`), trailing
  slash variants. Asserts `Ref.Path` is decoded + cleaned.
- `TestParseRef_RejectsEmpty` — `""`, whitespace-only → `ErrEmpty`.
- `TestParseRef_RejectsScheme` — `git://...`, `https://...`,
  `/abs/path` (no scheme), `file:/abs/path` (single slash, ambiguous)
  → `ErrUnsupportedScheme` or `ErrNotAbsolute`.
- `TestParseRef_RejectsNonLocalAuthority` — `file://host/path`,
  `file://localhost/abs` → `ErrNonLocal`.
- `TestParseRef_RejectsRelative` — `file://relative` (no leading
  `/`) → `ErrNotAbsolute`.
- `TestParseRef_RejectsTraversal` —
  `file:///foo/%2e%2e/bar` → `ErrNotAbsolute` (or new `ErrPathTraversal`).
- `TestResolve_HitsRegistry` — registry contains path; assert
  `Resolve` returns a non-nil `*store.Repo`.
- `TestResolve_NotIndexed` — path absent from registry; assert
  `ErrNotIndexed` wrapping the URI.
- `TestResolve_UsesDiskCache` — call `Resolve` twice on the same
  path; second call should not re-walk files (asserted via a
  counter or by mutating a file between calls and confirming the
  cached view).
- `TestResolve_Canonicalization` — `file:///foo/` and `file:///foo`
  resolve to the same entry.

### 10.2 Updated tool unit tests (15 code-intel + 3 registry + 1 schema)

Every existing test file that calls a tool factory gets two changes:

1. Factory call switches from `tool.X(repo)` to `tool.X(reg)`.
2. Args map gains a `project` field, populated via a new fixture
   helper `fx.ProjectURI()` (added to `internal/store/repofixture`)
   that returns `"file://" + fx.Root`.

Concrete examples:

- `internal/tool/treeoverview_test.go` — every
  `tool.TreeOverview(repo)(ctx, json.RawMessage(`{"depth":2}`))`
  becomes
  `tool.TreeOverview(reg)(ctx, json.RawMessage(`{"project":fx.ProjectURI(),"depth":2}`))`.
- Same pattern across `getnode_test.go`, `findsymbol_test.go`, …,
  `detectchanges_test.go`.
- `graphschema_test.go` — factory call becomes
  `tool.GetGraphSchema()` (no args). Args unchanged.

### 10.3 Updated acceptance tests (`tests/acceptance/registry_test.go`)

The three targeting tool calls (`index_repository`, `index_status`,
`delete_project`) switch their args keys from `path` to `project`,
with values as `file://` URIs. The `index_status` and
`delete_project` assertions check the round-trip behavior under the
new key.

### 10.4 New end-to-end test (`tests/acceptance/mcp_dispatch_test.go`)

Boots the MCP server in-process (without spawning a subprocess),
sends:

1. `tools/call index_repository {project: file:///path/to/fixture}`
2. `tools/call tree_overview {project: file:///path/to/fixture, depth: 1}`
3. `tools/call find_symbol {project: file:///path/to/fixture, name_path: "main.Func"}`

Asserts each call succeeds and the third returns a non-empty symbol
list. The closest thing to a regression test for the "register then
drill in" workflow the new design promises.

### 10.5 `cmd/yactt/main_test.go` additions

- `TestMCPServe_RejectsPositionalPath` —
  `runMCPServe([]string{"/some/path"})` returns an error matching
  `"no longer takes a positional path"`. Uses the existing direct-
  call pattern from the file.

## 11. Breaking changes

| Surface | Before | After |
|---|---|---|
| CLI | `yactt mcp serve [path]` | `yactt mcp serve` (positional errors) |
| 15 code-intel tool schemas | various required fields; no `project` | new required `project` (file:// URI) |
| `tree_overview` schema | required `repo` (decorative) | required `project`; `repo` accepted as deprecated alias for one release |
| 3 registry tools | required `path` | required `project` (file:// URI) |
| `list_projects` output | raw `path` field | unchanged |
| `persisted_query` schema | required `op` | required `op` + `project` (runner injects) |
| Example op `args` | `{"repo":"","depth":2}` etc. | `{"depth":2}` (project injected) |
| `junie-launcher.sh` | `exec yactt mcp serve "$(find_project_root)"` | `exec yactt mcp serve` |
| README quickstart | `yactt mcp serve /abs/path` | `yactt mcp serve` + `index_repository` first-call pattern |
| Startup audit line | emitted on boot (single-repo mode) | emitted on first `index_repository` success per session |
| TOFU check | once on boot (single-repo mode) | once on first `index_repository` per process |

## 12. Risks

1. **LSP client lifetime on cold paths.** Each cold `Resolve` may
   spin up gopls/tsserver/pyright. Deferred to a future daemon /
   HTTP server mode (locked decision #4). The MVP ships with the
   cold-call latency cost on the first `index_repository` after a
   repo changes; warm cache hits pay only the disk-cache
   deserialization cost.
2. **Disk cache key derivation.** Key is `sha256(repoRoot)[:16]`.
   Today `repoRoot` is whatever string was passed in; after the
   change it's the cleaned, canonicalized path from
   `project.ParseRef`. The key helper in
   `internal/registry/cache.go::diskCacheDir` should call
   `project.ParseRef` (or share a canonicalize helper) so any
   consumer of the cache sees a stable key. Implementation detail;
   flag for the plan.
3. **Path canonicalization on case-insensitive filesystems.**
   macOS (HFS+/APFS default) and Windows are case-insensitive;
   `file:///Foo` and `file:///foo` may refer to the same directory.
   The MVP does case-sensitive matching (matches the existing
   registry, which is case-sensitive on `Path`). Flag for follow-up
   if multi-platform users hit it.
4. **TOFU + audit placement.** Moved from boot to first
   `index_repository`. Document in README; otherwise transparent.
   Note: any user who relied on "yactt is ready" detection by
   tailing stderr needs the new contract.

## 13. Open items (resolved at implementation time, not blocking)

- Confirm `repo.Close()` correctly tears down LSP clients (today in
  single-repo mode they live for the server's lifetime). If not,
  document the cost.
- Confirm `audit.EmitStartup` works when called inside a tool
  handler (it should — it just writes a JSON line to a writer).
- Confirm `buildStartupInfo` still produces the correct snapshot
  shape when called from inside `IndexRepository` rather than from
  `runMCPServe`.

## 14. Documentation updates

- `README.md` — "Getting started" section replaces
  `yactt mcp serve /abs/path` with `yactt mcp serve` + the new
  `index_repository` first-call pattern. Document the `file://`
  URI shape and the tool-by-tool `project` field requirement.
- `docs/design.md` — short addendum: registry mode is now the only
  mode; the design table for "two modes" collapses. Section §5.1
  (tool table) gets a column noting which tools require a `project`
  arg.
- New `CHANGELOG.md` entry (or section in README) listing the
  breaking changes in §11 explicitly so anyone scripting against
  yactt sees the migration in one place.