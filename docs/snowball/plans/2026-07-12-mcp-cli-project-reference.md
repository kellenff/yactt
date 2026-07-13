# yactt MCP CLI project reference Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use snowball:subagent-driven-development (recommended) or snowball:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the optional positional path argument on `yactt mcp serve` with a `file://` URI that every targeting tool takes in its args. The server becomes stateless across tool calls.

**Architecture:** New `internal/project` package parses `file://` URIs and resolves them to a loaded `*store.Repo` via the registry and per-project disk cache. Every repo-bound tool factory switches from `tool.X(repo)` to `tool.X(reg)`; each handler calls `project.Resolve(reg, args.Project)` and `defer repo.Close()`. The single-repo boot path is gone. `tree_overview`'s existing `repo` field is accepted as a deprecated alias for one release. Audit and TOFU moves from boot-time to first `index_repository` success per session.

**Tech Stack:** Go 1.26.4, stdlib `net/url` for URI parsing, `path/filepath` for canonicalisation, existing `internal/registry` + `internal/store` packages, existing JSON-RPC MCP server in `internal/mcp`.

**Spec:** `docs/snowball/specs/2026-07-12-mcp-cli-project-reference-design.md` (commit `e95d813`).

---

## File Structure

### New files
- `internal/project/project.go` — `ParseRef`, `Resolve`, sentinel errors. The seam between MCP dispatch and repo loading.
- `internal/project/project_test.go` — unit tests for URI parsing and resolve.
- `tests/acceptance/mcp_dispatch_test.go` — end-to-end in-process dispatch chain (index_repository → tree_overview → find_symbol).

### Modified files (grouped by responsibility)

**Package seam:**
- `internal/project/project.go` (new) — `Ref`, `ParseRef`, `Resolve`, sentinels.
- `internal/registry/cache.go` — `diskCacheDir` calls `project.ParseRef` so cache keys are stable under URI canonicalisation.

**Tool handlers (15 code-intel):**
- `internal/tool/treeoverview.go` + test — schema, factory, handler. Special: deprecated `repo` alias for one release.
- `internal/tool/getnode.go` + test
- `internal/tool/nodesource.go` + test
- `internal/tool/nodeedges.go` + test (3 test files: `_test.go`, `_handler_test.go`, `_lsp_test.go`)
- `internal/tool/search_tool.go` + test
- `internal/tool/editimpact.go` + test
- `internal/tool/findsymbol.go` + test
- `internal/tool/getsymbolsoverview.go` + test
- `internal/tool/findcode.go` + test (2 test files: regex + tree_sitter)
- `internal/tool/searchcode.go` + test
- `internal/tool/findreferencingsymbols.go` + test
- `internal/tool/getcodesnippet.go` + test
- `internal/tool/architecture.go` + test
- `internal/tool/querygraph.go` + test
- `internal/tool/detectchanges.go` + test

**Tool handlers (registry-style):**
- `internal/tool/graphschema.go` + test — factory drops unused `*store.Repo` parameter.
- `internal/tool/index_repository.go` + test — schema renames `path` → `project`; gains `emitStartup` and `warnTrust` factory params.
- `internal/tool/index_status.go` + test
- `internal/tool/delete_project.go` + test

**Persisted query:**
- `internal/tool/persisted_query.go` + test — schema gains required `project`.
- `internal/persisted/registry.go` — runner injects caller's `project` into target tool's args.
- `internal/persisted/example_ops.go` — drop `"repo": ""` placeholders from `onboarding` and `repo-map`.

**Fixture helper:**
- `internal/store/repofixture/repofixture.go` — add `ProjectURI()` method to `Fixture`.

**CLI:**
- `cmd/yactt/main.go` — `runMCPServe` drops positional path; builds `IndexHooks` (emitStartup + warnTrust closures) bound to running binary/version/SHA.

**Integration:**
- `junie-extension/scripts/yactt-launcher.sh` — drop `find_project_root`; `exec yactt mcp serve`.
- `junie-extension/test/launcher.test.sh` — update dry-run assertion.
- `.mcp.json` (repo root) — drop positional path if present.

**Docs:**
- `README.md` — "Getting started" + per-tool `project` field documentation.
- `docs/design.md` — addendum noting registry-only mode and the `project` field.
- New `CHANGELOG.md` — breaking changes entry.

### Tests
- `internal/project/project_test.go` (new) — ParseRef + Resolve unit tests.
- `internal/tool/*_test.go` (15 files) — fixture calls switch `tool.X(repo)` → `tool.X(reg)`; args gain `project`.
- `tests/acceptance/registry_test.go` — `path` → `project` (file:// URI).
- `tests/acceptance/mcp_dispatch_test.go` (new) — end-to-end.
- `cmd/yactt/main_test.go` — add `TestMCPServe_RejectsPositionalPath`.

---

## Phase 1: Foundation — `internal/project` package

### Task 1: `internal/project/project.go` — Ref, ParseRef, sentinel errors

**Files:**
- Create: `internal/project/project.go`
- Test: `internal/project/project_test.go`

- [ ] **Step 1: Write the failing test for `ParseRef`**

Create `internal/project/project_test.go`:

```go
package project_test

import (
	"strings"
	"testing"

	"github.com/kellenff/yactt/internal/project"
)

func TestParseRef_Valid(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"file:///abs/path", "/abs/path"},
		{"file:///abs/path/", "/abs/path"},
		{"file:///abs/path/with/%20space", "/abs/path/with/ space"},
		{"file:///abs/path/with/%2Fslash", "/abs/path/with/slash"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			ref, err := project.ParseRef(tc.in)
			if err != nil {
				t.Fatalf("ParseRef(%q) error: %v", tc.in, err)
			}
			if ref.Path != tc.want {
				t.Errorf("Path = %q, want %q", ref.Path, tc.want)
			}
		})
	}
}

func TestParseRef_Rejects(t *testing.T) {
	cases := []struct {
		in     string
		wantIs error
	}{
		{"", project.ErrEmpty},
		{"   ", project.ErrEmpty},
		{"/abs/path", project.ErrUnsupportedScheme},
		{"git://host/abs/path", project.ErrUnsupportedScheme},
		{"https://example.com/foo", project.ErrUnsupportedScheme},
		{"file:/abs/path", project.ErrUnsupportedScheme},
		{"file://host/abs/path", project.ErrNonLocal},
		{"file://localhost/abs", project.ErrNonLocal},
		{"file://relative", project.ErrNotAbsolute},
		{"file:///foo/%2e%2e/bar", project.ErrNotAbsolute},
		{"file:///foo/../bar", project.ErrNotAbsolute},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			_, err := project.ParseRef(tc.in)
			if err == nil {
				t.Fatalf("ParseRef(%q) succeeded; want error", tc.in)
			}
			if !strings.Contains(err.Error(), tc.wantIs.Error()) {
				t.Errorf("ParseRef(%q) error = %v; want containing %v", tc.in, err, tc.wantIs)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/project/...`
Expected: FAIL (package does not exist; `go build` fails with "no Go files in internal/project").

- [ ] **Step 3: Write minimal `project.go`**

Create `internal/project/project.go`:

```go
// Package project parses the on-wire project reference (a file://
// URI) and resolves it to a loaded *store.Repo via the registry
// and the per-project disk cache.
//
// The package is the seam between MCP dispatch (where every
// targeting tool receives a `project` field) and repo loading
// (where store.Load reads files and primes the disk cache). Tools
// call project.Resolve rather than store.Load directly so URI
// validation, registry lookup, and cache wiring all live in one
// place.
package project

import (
	"errors"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/kellenff/yactt/internal/registry"
	"github.com/kellenff/yactt/internal/store"
)

// Ref is a parsed file:// URI. The wrapper exists so future
// schemes (git://, https://) can grow without a v2 wire shape;
// today the only accepted scheme is file://.
type Ref struct {
	Path string // decoded, cleaned, absolute filesystem path
}

// Sentinel errors. Handlers wrap them with the offending URI for
// machine-readable error envelopes.
var (
	ErrEmpty             = errors.New("project: empty reference")
	ErrUnsupportedScheme = errors.New("project: only file:// URIs are accepted")
	ErrNonLocal          = errors.New("project: file:// URI must have empty authority (local files only)")
	ErrNotAbsolute       = errors.New("project: file:// URI must encode an absolute path")
	ErrNotIndexed        = errors.New("project: not in registry; call index_repository first")
)

// ParseRef parses a file:// URI. See the spec for the full list
// of rejections. The returned Ref.Path is the cleaned absolute
// filesystem path the URI encodes.
//
// Empty input returns ErrEmpty. Non-file:// schemes return
// ErrUnsupportedScheme. file:// URIs with a non-empty authority
// return ErrNonLocal. URIs that encode a relative path (no
// leading /) or contain .. segments after percent-decoding and
// cleaning return ErrNotAbsolute.
func ParseRef(raw string) (Ref, error) {
	if strings.TrimSpace(raw) == "" {
		return Ref{}, ErrEmpty
	}
	u, err := url.Parse(raw)
	if err != nil {
		return Ref{}, ErrUnsupportedScheme
	}
	if u.Scheme != "file" {
		return Ref{}, ErrUnsupportedScheme
	}
	if u.Host != "" {
		return Ref{}, ErrNonLocal
	}
	if u.Path == "" || !strings.HasPrefix(u.Path, "/") {
		return Ref{}, ErrNotAbsolute
	}
	// filepath.Join + Clean normalises the decoded path and
	// collapses . / .. / double-slash. We then explicitly reject
	// any remaining .. segments as defence in depth against
	// percent-encoded traversal (e.g. %2e%2e).
	cleaned := filepath.Clean(filepath.Join("/", u.Path))
	if cleaned == "/" || strings.Contains(cleaned, "/../") || strings.HasSuffix(cleaned, "/..") || strings.HasPrefix(cleaned, "../") || cleaned == ".." {
		return Ref{}, ErrNotAbsolute
	}
	return Ref{Path: cleaned}, nil
}

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
// When the future daemon mode lands, add a ctx parameter so
// cancellation propagates from the server loop.
func Resolve(reg *registry.Registry, raw string) (*store.Repo, error) {
	ref, err := ParseRef(raw)
	if err != nil {
		return nil, err
	}
	if _, ok := reg.GetByPath(ref.Path); !ok {
		return nil, ErrNotIndexed
	}
	repo, _, err := store.Load(ref.Path, registry.LoadOptsWithDiskCache(ref.Path)...)
	if err != nil {
		return nil, err
	}
	return repo, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/project/...`
Expected: PASS for both `TestParseRef_Valid` and `TestParseRef_Rejects`.

- [ ] **Step 5: Commit**

```bash
git add internal/project/project.go internal/project/project_test.go
git commit -m "feat(project): add file:// URI parser and resolver"
```

---

### Task 2: `internal/project/project_test.go` — `Resolve` tests

**Files:**
- Modify: `internal/project/project_test.go` (extend)

- [ ] **Step 1: Append `Resolve` tests**

Append to `internal/project/project_test.go`:

```go
import (
	// ...existing
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/kellenff/yactt/internal/registry"
	"github.com/kellenff/yactt/internal/store/repofixture"
)

func TestResolve_HitsRegistry(t *testing.T) {
	fx := repofixture.New(t)
	dir := t.TempDir()
	reg := registry.New(filepath.Join(dir, "projects.json"))
	// Register the fixture so GetByPath succeeds.
	// Easiest path: use IndexRepository (which writes the entry).
	idx := newIndexStub(t, fx.Root, reg)
	if _, err := idx(context.Background(), json.RawMessage(`{"path":"`+fx.Root+`"}`)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	repo, err := project.Resolve(reg, "file://"+fx.Root)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if repo.Root() != fx.Root {
		t.Errorf("Root = %q, want %q", repo.Root(), fx.Root)
	}
}

func TestResolve_NotIndexed(t *testing.T) {
	dir := t.TempDir()
	reg := registry.New(filepath.Join(dir, "projects.json"))
	_, err := project.Resolve(reg, "file:///no/such/path")
	if err == nil {
		t.Fatal("Resolve: expected error for unregistered path")
	}
	if !strings.Contains(err.Error(), project.ErrNotIndexed.Error()) {
		t.Errorf("error = %v; want containing ErrNotIndexed", err)
	}
}

func TestResolve_Canonicalization(t *testing.T) {
	fx := repofixture.New(t)
	dir := t.TempDir()
	reg := registry.New(filepath.Join(dir, "projects.json"))
	idx := newIndexStub(t, fx.Root, reg)
	if _, err := idx(context.Background(), json.RawMessage(`{"path":"`+fx.Root+`"}`)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Trailing slash must canonicalise to the same entry.
	for _, raw := range []string{"file://" + fx.Root, "file://" + fx.Root + "/"} {
		repo, err := project.Resolve(reg, raw)
		if err != nil {
			t.Fatalf("Resolve(%q) error: %v", raw, err)
		}
		_ = repo.Close()
	}
}

// newIndexStub seeds a registry entry by calling the same logic
// tool.IndexRepository uses, but without the audit/TOFU hooks
// (those land in Task 13). Mirrors enough of IndexRepository for
// the seed step.
func newIndexStub(t *testing.T, root string, reg *registry.Registry) func(ctx context.Context, args json.RawMessage) (any, error) {
	t.Helper()
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		abs, err := filepath.Abs(root)
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(abs); err != nil {
			return nil, err
		}
		repo, _, err := store.Load(abs, registry.LoadOptsWithDiskCache(abs)...)
		if err != nil {
			return nil, err
		}
		_ = repo.Close()
		return reg.Upsert(registry.Entry{
			Name:      filepath.Base(abs),
			Path:      abs,
			IndexedAt: time.Now().UTC(),
			Files:     0, // count not material for these tests
		})
	}
}
```

Add `"time"` to the import block. (The stub deliberately avoids `tool.IndexRepository` to keep `project` independent of `tool`; that dependency lands in Task 13 once `IndexRepository` gains the audit hooks.)

- [ ] **Step 2: Run tests to verify they pass**

Run: `go test ./internal/project/...`
Expected: PASS for all four test functions.

- [ ] **Step 3: Commit**

```bash
git add internal/project/project_test.go
git commit -m "test(project): add Resolve unit tests (registry hit, miss, canonicalisation)"
```

---

## Phase 2: Tree overview (sets the schema + factory + handler pattern)

### Task 3: `internal/tool/treeoverview.go` — schema + factory + handler migration

This is the special tool: it carries the deprecated `repo` alias.

**Files:**
- Modify: `internal/tool/treeoverview.go`
- Modify: `internal/tool/treeoverview_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/tool/treeoverview_test.go`:

```go
func TestTreeOverview_RequiresProject(t *testing.T) {
	dir := t.TempDir()
	reg := registry.New(filepath.Join(dir, "projects.json"))
	h := tool.TreeOverview(reg)
	// Missing project entirely: ErrEmpty from project.ParseRef.
	_, err := h(context.Background(), json.RawMessage(`{"depth":1}`))
	if err == nil {
		t.Fatal("expected error when project missing")
	}
}

func TestTreeOverview_ProjectResolves(t *testing.T) {
	fx := repofixture.New(t)
	dir := t.TempDir()
	reg := registry.New(filepath.Join(dir, "projects.json"))
	// Seed registry via IndexRepository (with no-op audit hooks).
	idx := tool.IndexRepository(reg, func(_ audit.Startup) error { return nil }, func() {})
	if _, err := idx(context.Background(), json.RawMessage(`{"path":"`+fx.Root+`"}`)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	h := tool.TreeOverview(reg)
	out, err := h(context.Background(), json.RawMessage(`{"project":"file://`+fx.Root+`","depth":1}`))
	if err != nil {
		t.Fatalf("tree_overview: %v", err)
	}
	if out == nil {
		t.Fatal("nil result")
	}
}

func TestTreeOverview_DeprecatedRepoAlias(t *testing.T) {
	fx := repofixture.New(t)
	dir := t.TempDir()
	reg := registry.New(filepath.Join(dir, "projects.json"))
	idx := tool.IndexRepository(reg, func(_ audit.Startup) error { return nil }, func() {})
	if _, err := idx(context.Background(), json.RawMessage(`{"path":"`+fx.Root+`"}`)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Capture stderr to assert deprecation notice.
	origStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	defer func() { os.Stderr = origStderr }()
	h := tool.TreeOverview(reg)
	if _, err := h(context.Background(), json.RawMessage(`{"repo":"`+fx.Root+`","depth":1}`)); err != nil {
		t.Fatalf("tree_overview with deprecated repo: %v", err)
	}
	_ = w.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	out := buf.String()
	if !strings.Contains(out, "deprecation") {
		t.Errorf("expected deprecation notice on stderr, got %q", out)
	}
}
```

Add the required imports: `audit`, `bytes`, `os`, `strings`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tool/ -run 'TestTreeOverview_(RequiresProject|ProjectResolves|DeprecatedRepoAlias)' -v`
Expected: FAIL — `tool.TreeOverview` still takes `*store.Repo`, not `*registry.Registry`.

- [ ] **Step 3: Update `TreeOverviewSchema`**

In `internal/tool/treeoverview.go`, replace the schema declaration with:

```go
// TreeOverviewSchema is the JSON Schema for tree_overview.
// `project` is the new required file:// URI; the legacy `repo`
// field is accepted as a deprecated alias for one release and
// will be removed in the next release.
var TreeOverviewSchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "project":        { "type": "string", "description": "Absolute path as a file:// URI (e.g. file:///abs/path). Must be in the registry; call index_repository first." },
    "repo":           { "type": "string", "description": "DEPRECATED alias for ` + "`project`" + ` (file:// URI). Will be removed in the next release." },
    "scope":          { "type": "string", "description": "Optional absolute path under repo root; narrows the walk to a package or subdirectory. Mirrors search.Search's q.Scope." },
    "depth":          { "type": "integer", "default": 2, "minimum": 1, "maximum": 6 },
    "include_layers": {
      "type": "array",
      "items": { "enum": ["summary", "structure", "signature"] },
      "default": ["summary", "structure"]
    }
  },
  "required": ["project"],
  "anyOf": [
    { "required": ["project"] },
    { "required": ["repo"], "description": "Deprecated path; emits a stderr notice and is removed in the next release." }
  ],
  "additionalProperties": false
}`)
```

- [ ] **Step 4: Update the `TreeOverview` factory + handler**

Replace the existing `func TreeOverview(repo *store.Repo) ...` block with:

```go
// TreeOverview returns a Handler that produces the top-N levels of
// the tree rooted at `args.Project`, with only the requested layer
// set populated. The `reg` argument is used to resolve the
// `project` (file:// URI) to a loaded *store.Repo via project.Resolve.
func TreeOverview(reg *registry.Registry) func(ctx context.Context, args json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		var a TreeOverviewArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("invalid tree_overview args: %w", err)
		}
		// Deprecated alias: if Project is empty and Repo is set,
		// wrap Repo as a file:// URI and emit a stderr notice.
		// Project wins when both are present.
		if a.Project == "" && a.Repo != "" {
			fmt.Fprintf(os.Stderr,
				"deprecation: tree_overview's 'repo' field is renamed to 'project' (file:// URI); will be removed in the next release\n")
			a.Project = "file://" + a.Repo
		}
		repo, err := project.Resolve(reg, a.Project)
		if err != nil {
			return nil, err
		}
		defer func() { _ = repo.Close() }()
		if a.Depth <= 0 {
			a.Depth = 2
		}
		if a.Depth > 6 {
			a.Depth = 6
		}
		if a.Scope != "" {
			a.Scope = filepath.Clean(a.Scope)
			if !strings.HasPrefix(a.Scope, repo.Root()) {
				return nil, fmt.Errorf("tree_overview: scope %q is outside repo root %q", a.Scope, repo.Root())
			}
		}
		root, truncated, err := buildOverviewTree(repo, repo.Root(), a.Depth, a.Scope)
		if err != nil {
			return nil, err
		}
		if truncated {
			root.Warning = truncatedWarning
		}
		return root, nil
	}
}
```

- [ ] **Step 5: Update `TreeOverviewArgs` (add `Project`, keep `Repo`)**

Replace the existing args struct with:

```go
// TreeOverviewArgs is the typed boundary input for tree_overview.
// Project is the new file:// URI; Repo is a deprecated raw-path
// alias retained for one release.
type TreeOverviewArgs struct {
	Project       string   `json:"project"`
	Repo          string   `json:"repo"`
	Scope         string   `json:"scope"`
	Depth         int      `json:"depth"`
	IncludeLayers []string `json:"include_layers"`
}
```

- [ ] **Step 6: Update imports**

In `internal/tool/treeoverview.go`, update the import block to:

```go
import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kellenff/yactt/internal/project"
	"github.com/kellenff/yactt/internal/registry"
	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/entity"
)
```

(Drop the `store` import if it has no other uses in this file.)

- [ ] **Step 7: Run tests to verify they pass**

Run: `go test ./internal/tool/ -run 'TestTreeOverview' -v`
Expected: PASS for `TestTreeOverview_RequiresProject`, `TestTreeOverview_ProjectResolves`, `TestTreeOverview_DeprecatedRepoAlias`, and all pre-existing tree_overview tests (after they are updated to pass `project`).

- [ ] **Step 8: Update pre-existing tree_overview tests to provide `project`**

In `internal/tool/treeoverview_test.go`, every call like:

```go
tool.TreeOverview(repo)(ctx, json.RawMessage(`{"depth":2}`))
```

becomes:

```go
dir := t.TempDir()
reg := registry.New(filepath.Join(dir, "projects.json"))
// Seed with IndexRepository (no-op audit hooks for unit tests).
idx := tool.IndexRepository(reg, func(_ audit.Startup) error { return nil }, func() {})
if _, err := idx(context.Background(), json.RawMessage(`{"path":"`+fx.Root+`"}`)); err != nil {
    t.Fatalf("seed: %v", err)
}
tool.TreeOverview(reg)(ctx, json.RawMessage(`{"project":"file://`+fx.Root+`","depth":2}`))
```

A small `seedRegistry(t, fx)` test helper (added at the top of the test file) encapsulates the boilerplate:

```go
func seedRegistry(t *testing.T, fx *repofixture.Fixture) *registry.Registry {
	t.Helper()
	dir := t.TempDir()
	reg := registry.New(filepath.Join(dir, "projects.json"))
	idx := tool.IndexRepository(reg, func(_ audit.Startup) error { return nil }, func() {})
	if _, err := idx(context.Background(), json.RawMessage(`{"path":"`+fx.Root+`"}`)); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	return reg
}
```

- [ ] **Step 9: Run all `internal/tool` tests to verify the suite is green**

Run: `go test ./internal/tool/...`
Expected: PASS.

- [ ] **Step 10: Commit**

```bash
git add internal/tool/treeoverview.go internal/tool/treeoverview_test.go
git commit -m "feat(tool): tree_overview takes file:// project URI; deprecate repo field"
```

---

## Phase 3: Migrate the other 14 code-intel tools

The pattern from Task 3 (with the deprecated `repo` alias removed) repeats for each tool. Every task below follows the same five steps:

1. **Schema:** add `"project"` to `properties` and `"project"` to `required`. Description matches the standard fragment.
2. **Args struct:** add `Project string \`json:"project"\`` as the first field.
3. **Factory signature:** `func Foo(repo *store.Repo)` → `func Foo(reg *registry.Registry)`.
4. **Handler:** at the top of the closure, after the args unmarshal, insert:

   ```go
   repo, err := project.Resolve(reg, a.Project)
   if err != nil {
       return nil, err
   }
   defer func() { _ = repo.Close() }()
   ```

   The existing `repo` closure variable (from `func Foo(repo *store.Repo)`) is replaced by the locally-loaded `repo` from the resolver.

5. **Imports:** add `project`, `registry`; drop `store` if no other use.

### Task 4: `internal/tool/getnode.go`

**Files:**
- Modify: `internal/tool/getnode.go`
- Modify: `internal/tool/getnode_test.go`

- [ ] **Step 1: Schema — add `project` field**

Replace the existing `GetNodeSchema` declaration with:

```go
var GetNodeSchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["id", "project"],
  "properties": {
    "project":         { "type": "string", "description": "Absolute path as a file:// URI (e.g. file:///abs/path). Must be in the registry; call index_repository first." },
    "id":              { "type": "string", "description": "Stable node id (e.g. fn:pkg.Func, meth:pkg.Receiver.Method, file:relative/path)." },
    "layers":          { "type": "array", "items": { "enum": ["summary","signature","body","source","tokens"] }, "default": ["summary"] },
    "range":           { "type": "array", "items": { "type": "integer" }, "minItems": 2, "maxItems": 2, "description": "Optional [start,end] byte range when layer=source." },
    "include_trivia":  { "type": "boolean", "default": false }
  },
  "additionalProperties": false
}`)
```

- [ ] **Step 2: Args struct — add `Project` field**

Replace `GetNodeArgs` with:

```go
type GetNodeArgs struct {
	Project       string   `json:"project"`
	ID            string   `json:"id"`
	Layers        []string `json:"layers"`
	Range         []int    `json:"range"`
	IncludeTrivia bool     `json:"include_trivia"`
}
```

- [ ] **Step 3: Factory + handler rewrite**

Replace the entire `GetNode` function with:

```go
func GetNode(reg *registry.Registry) func(ctx context.Context, args json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		var a GetNodeArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("invalid node_get args: %w", err)
		}
		repo, err := project.Resolve(reg, a.Project)
		if err != nil {
			return nil, err
		}
		defer func() { _ = repo.Close() }()
		return getNode(repo, a)
	}
}
```

The pre-existing logic moves into an unexported helper `getNode(repo *store.Repo, a GetNodeArgs)` (rename the original handler body into that helper).

- [ ] **Step 4: Imports**

Add `"github.com/kellenff/yactt/internal/project"` and `"github.com/kellenff/yactt/internal/registry"`; keep `store`.

- [ ] **Step 5: Tests — switch factory call + add `project` to args**

In `getnode_test.go`, every call becomes:

```go
reg := seedRegistry(t, fx)
h := tool.GetNode(reg)
h(context.Background(), json.RawMessage(`{"project":"file://`+fx.Root+`","id":"..."}`))
```

- [ ] **Step 6: Run tests**

Run: `go test ./internal/tool/ -run TestGetNode -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/tool/getnode.go internal/tool/getnode_test.go
git commit -m "feat(tool): node_get takes file:// project URI"
```

---

### Task 5: `internal/tool/nodesource.go`

**Files:**
- Modify: `internal/tool/nodesource.go`
- Modify: `internal/tool/nodesource_test.go`

- [ ] **Step 1: Schema — add `project` field**

Replace `NodeSourceSchema` with:

```go
var NodeSourceSchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["id", "project"],
  "properties": {
    "project": { "type": "string", "description": "Absolute path as a file:// URI (e.g. file:///abs/path). Must be in the registry; call index_repository first." },
    "id":      { "type": "string", "description": "Stable node id, or file:<relative-path> for whole-file lossless source." },
    "range":   { "type": "array", "items": { "type": "integer" }, "minItems": 2, "maxItems": 2 }
  },
  "additionalProperties": false
}`)
```

- [ ] **Step 2: Args struct**

```go
type NodeSourceArgs struct {
	Project string `json:"project"`
	ID      string `json:"id"`
	Range   []int  `json:"range"`
}
```

- [ ] **Step 3: Factory + handler**

```go
func NodeSource(reg *registry.Registry) func(ctx context.Context, args json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		var a NodeSourceArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("invalid node_source args: %w", err)
		}
		repo, err := project.Resolve(reg, a.Project)
		if err != nil {
			return nil, err
		}
		defer func() { _ = repo.Close() }()
		return nodeSource(repo, a)
	}
}
```

Move the original handler body into `func nodeSource(repo *store.Repo, a NodeSourceArgs)`.

- [ ] **Step 4: Imports** — add `project`, `registry`.

- [ ] **Step 5: Tests** — switch factory call to `tool.NodeSource(reg)`; add `"project":"file://"+fx.Root` to every args JSON.

- [ ] **Step 6: Run tests**

Run: `go test ./internal/tool/ -run TestNodeSource -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/tool/nodesource.go internal/tool/nodesource_test.go
git commit -m "feat(tool): node_source takes file:// project URI"
```

---

### Task 6: `internal/tool/nodeedges.go`

**Files:**
- Modify: `internal/tool/nodeedges.go`
- Modify: `internal/tool/nodeedges_test.go`, `internal/tool/nodeedges_handler_test.go`, `internal/tool/nodeedges_lsp_test.go`

- [ ] **Step 1: Schema — add `project` field**

```go
var NodeEdgesSchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["id", "project"],
  "properties": {
    "project": { "type": "string", "description": "Absolute path as a file:// URI (e.g. file:///abs/path). Must be in the registry; call index_repository first." },
    "id":      { "type": "string", "description": "Stable node id whose edges to enumerate." },
    "kinds":   { "type": "array", "items": { "enum": ["callers","callees","tests","overrides","imports"] }, "default": ["callers","callees"] }
  },
  "additionalProperties": false
}`)
```

- [ ] **Step 2: Args struct**

```go
type NodeEdgesArgs struct {
	Project string   `json:"project"`
	ID      string   `json:"id"`
	Kinds   []string `json:"kinds"`
}
```

- [ ] **Step 3: Factory + handler** — same shape as Tasks 4–5; rename inner helper to `nodeEdges(repo, a)`.

- [ ] **Step 4: Imports** — add `project`, `registry`.

- [ ] **Step 5: Tests** — switch factory call to `tool.NodeEdges(reg)`; add `project` to every args JSON across the three test files.

- [ ] **Step 6: Run tests**

Run: `go test ./internal/tool/ -run TestNodeEdges -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/tool/nodeedges.go internal/tool/nodeedges*_test.go
git commit -m "feat(tool): node_edges takes file:// project URI"
```

---

### Task 7: `internal/tool/search_tool.go`

**Files:**
- Modify: `internal/tool/search_tool.go`
- Modify: `internal/tool/search_tool_test.go`

- [ ] **Step 1: Schema — add `project` field**

```go
var SearchSchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["q", "project"],
  "properties": {
    "project": { "type": "string", "description": "Absolute path as a file:// URI (e.g. file:///abs/path). Must be in the registry; call index_repository first." },
    "q":       { "type": "string", "description": "BM25 query string; whitespace-tokenised; quoted phrases become single terms." },
    "scope":   { "type": "string", "description": "Optional absolute path under repo root." },
    "limit":   { "type": "integer", "default": 10, "minimum": 1, "maximum": 100 }
  },
  "additionalProperties": false
}`)
```

- [ ] **Step 2: Args struct**

```go
type SearchArgs struct {
	Project string `json:"project"`
	Q       string `json:"q"`
	Scope   string `json:"scope"`
	Limit   int    `json:"limit"`
}
```

- [ ] **Step 3: Factory + handler** — same pattern; inner helper `search(repo, a)`.

- [ ] **Step 4: Imports** — add `project`, `registry`.

- [ ] **Step 5: Tests** — switch to `tool.Search(reg)`; add `project` to args.

- [ ] **Step 6: Run tests**

Run: `go test ./internal/tool/ -run TestSearch -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/tool/search_tool.go internal/tool/search_tool_test.go
git commit -m "feat(tool): search takes file:// project URI"
```

---

### Task 8: `internal/tool/editimpact.go`

**Files:**
- Modify: `internal/tool/editimpact.go`
- Modify: `internal/tool/editimpact_test.go`

- [ ] **Step 1: Schema — add `project` field**

```go
var EditImpactSchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["renames", "project"],
  "properties": {
    "project": { "type": "string", "description": "Absolute path as a file:// URI (e.g. file:///abs/path). Must be in the registry; call index_repository first." },
    "renames": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["id", "new_name"],
        "properties": {
          "id":       { "type": "string" },
          "new_name": { "type": "string" }
        }
      }
    }
  },
  "additionalProperties": false
}`)
```

- [ ] **Step 2: Args struct**

```go
type EditImpactArgs struct {
	Project string         `json:"project"`
	Renames []EditRename   `json:"renames"`
}

type EditRename struct {
	ID      string `json:"id"`
	NewName string `json:"new_name"`
}
```

- [ ] **Step 3: Factory + handler** — same pattern; inner helper `editImpact(repo, a)`.

- [ ] **Step 4: Imports** — add `project`, `registry`.

- [ ] **Step 5: Tests** — switch to `tool.EditImpact(reg)`; add `project` to args.

- [ ] **Step 6: Run tests**

Run: `go test ./internal/tool/ -run TestEditImpact -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/tool/editimpact.go internal/tool/editimpact_test.go
git commit -m "feat(tool): edit_impact takes file:// project URI"
```

---

### Task 9: `internal/tool/findsymbol.go`

**Files:**
- Modify: `internal/tool/findsymbol.go`
- Modify: `internal/tool/findsymbol_test.go`

- [ ] **Step 1: Schema — add `project` field**

```go
var FindSymbolSchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["name_path", "project"],
  "properties": {
    "project":      { "type": "string", "description": "Absolute path as a file:// URI (e.g. file:///abs/path). Must be in the registry; call index_repository first." },
    "name_path":    { "type": "string", "description": "Glob over the name-path (pkg.Name or pkg/Name, with * allowed)." },
    "scope":        { "type": "string" },
    "kind":         { "type": "array", "items": { "enum": ["FUNCTION","METHOD","CLASS","MODULE"] } },
    "include_body": { "type": "boolean", "default": false },
    "limit":        { "type": "integer", "default": 20, "minimum": 1, "maximum": 100 }
  },
  "additionalProperties": false
}`)
```

- [ ] **Step 2: Args struct**

```go
type FindSymbolArgs struct {
	Project     string   `json:"project"`
	NamePath    string   `json:"name_path"`
	Scope       string   `json:"scope"`
	Kind        []string `json:"kind"`
	IncludeBody bool     `json:"include_body"`
	Limit       int      `json:"limit"`
}
```

- [ ] **Step 3: Factory + handler** — same pattern; inner helper `findSymbol(repo, a)`.

- [ ] **Step 4: Imports** — add `project`, `registry`.

- [ ] **Step 5: Tests** — switch to `tool.FindSymbol(reg)`; add `project` to args.

- [ ] **Step 6: Run tests**

Run: `go test ./internal/tool/ -run TestFindSymbol -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/tool/findsymbol.go internal/tool/findsymbol_test.go
git commit -m "feat(tool): find_symbol takes file:// project URI"
```

---

### Task 10: `internal/tool/getsymbolsoverview.go`

**Files:**
- Modify: `internal/tool/getsymbolsoverview.go`
- Modify: `internal/tool/getsymbolsoverview_test.go`

- [ ] **Step 1: Schema — add `project` field**

```go
var GetSymbolsOverviewSchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["file", "project"],
  "properties": {
    "project": { "type": "string", "description": "Absolute path as a file:// URI (e.g. file:///abs/path). Must be in the registry; call index_repository first." },
    "file":    { "type": "string", "description": "Repo-relative or absolute path to a source file." }
  },
  "additionalProperties": false
}`)
```

- [ ] **Step 2: Args struct**

```go
type GetSymbolsOverviewArgs struct {
	Project string `json:"project"`
	File    string `json:"file"`
}
```

- [ ] **Step 3: Factory + handler** — same pattern; inner helper `getSymbolsOverview(repo, a)`.

- [ ] **Step 4: Imports** — add `project`, `registry`.

- [ ] **Step 5: Tests** — switch to `tool.GetSymbolsOverview(reg)`; add `project` to args.

- [ ] **Step 6: Run tests**

Run: `go test ./internal/tool/ -run TestGetSymbolsOverview -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/tool/getsymbolsoverview.go internal/tool/getsymbolsoverview_test.go
git commit -m "feat(tool): get_symbols_overview takes file:// project URI"
```

---

### Task 11: `internal/tool/findcode.go`

**Files:**
- Modify: `internal/tool/findcode.go`
- Modify: `internal/tool/findcode_regex_test.go`, `internal/tool/findcode_tree_sitter_test.go`

- [ ] **Step 1: Schema — add `project` field**

Replace `FindCodeSchema` with:

```go
var FindCodeSchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["pattern", "project"],
  "properties": {
    "project":         { "type": "string", "description": "Absolute path as a file:// URI (e.g. file:///abs/path). Must be in the registry; call index_repository first." },
    "pattern":         { "type": "string" },
    "pattern_kind":    { "type": "string", "enum": ["regex", "tree_sitter"], "default": "regex" },
    "scope":           { "type": "string" },
    "file_filter":     { "type": "string" },
    "include_context": { "type": "boolean", "default": false },
    "limit":           { "type": "integer", "default": 50, "minimum": 1, "maximum": 500 }
  },
  "additionalProperties": false
}`)
```

- [ ] **Step 2: Args struct**

```go
type FindCodeArgs struct {
	Project        string `json:"project"`
	Pattern        string `json:"pattern"`
	PatternKind    string `json:"pattern_kind"`
	Scope          string `json:"scope"`
	FileFilter     string `json:"file_filter"`
	IncludeContext bool   `json:"include_context"`
	Limit          int    `json:"limit"`
}
```

- [ ] **Step 3: Factory + handler** — same pattern; inner helper `findCode(repo, a)`.

- [ ] **Step 4: Imports** — add `project`, `registry`.

- [ ] **Step 5: Tests** — switch to `tool.FindCode(reg)`; add `project` to every args JSON across both test files.

- [ ] **Step 6: Run tests**

Run: `go test ./internal/tool/ -run TestFindCode -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/tool/findcode.go internal/tool/findcode*_test.go
git commit -m "feat(tool): find_code takes file:// project URI"
```

---

### Task 12: `internal/tool/searchcode.go`

**Files:**
- Modify: `internal/tool/searchcode.go`
- Modify: `internal/tool/searchcode_test.go`

- [ ] **Step 1: Schema — add `project` field**

Replace `SearchCodeSchema` with:

```go
var SearchCodeSchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["query", "project"],
  "properties": {
    "project": { "type": "string", "description": "Absolute path as a file:// URI (e.g. file:///abs/path). Must be in the registry; call index_repository first." },
    "query":   { "type": "string" },
    "limit":   { "type": "integer", "default": 10, "minimum": 1, "maximum": 50 }
  },
  "additionalProperties": false
}`)
```

- [ ] **Step 2: Args struct**

```go
type SearchCodeArgs struct {
	Project string `json:"project"`
	Query   string `json:"query"`
	Limit   int    `json:"limit"`
}
```

- [ ] **Step 3: Factory + handler** — same pattern; inner helper `searchCode(repo, a)`.

- [ ] **Step 4: Imports** — add `project`, `registry`.

- [ ] **Step 5: Tests** — switch to `tool.SearchCode(reg)`; add `project` to args.

- [ ] **Step 6: Run tests**

Run: `go test ./internal/tool/ -run TestSearchCode -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/tool/searchcode.go internal/tool/searchcode_test.go
git commit -m "feat(tool): search_code takes file:// project URI"
```

---

### Task 13: `internal/tool/findreferencingsymbols.go`

**Files:**
- Modify: `internal/tool/findreferencingsymbols.go`
- Modify: `internal/tool/findreferencingsymbols_test.go` (if present; otherwise omit)

- [ ] **Step 1: Schema — add `project` field**

Replace `FindReferencingSymbolsSchema` with:

```go
var FindReferencingSymbolsSchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["name_path", "project"],
  "properties": {
    "project":   { "type": "string", "description": "Absolute path as a file:// URI (e.g. file:///abs/path). Must be in the registry; call index_repository first." },
    "name_path": { "type": "string" },
    "limit":     { "type": "integer", "default": 20, "minimum": 1, "maximum": 100 }
  },
  "additionalProperties": false
}`)
```

- [ ] **Step 2: Args struct**

```go
type FindReferencingSymbolsArgs struct {
	Project  string `json:"project"`
	NamePath string `json:"name_path"`
	Limit    int    `json:"limit"`
}
```

- [ ] **Step 3: Factory + handler** — same pattern; inner helper `findReferencingSymbols(repo, a)`.

- [ ] **Step 4: Imports** — add `project`, `registry`.

- [ ] **Step 5: Tests** — switch to `tool.FindReferencingSymbols(reg)`; add `project` to args.

- [ ] **Step 6: Run tests**

Run: `go test ./internal/tool/ -run TestFindReferencingSymbols -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/tool/findreferencingsymbols.go
git commit -m "feat(tool): find_referencing_symbols takes file:// project URI"
```

---

### Task 14: `internal/tool/getcodesnippet.go`

**Files:**
- Modify: `internal/tool/getcodesnippet.go`
- Modify: `internal/tool/getcodesnippet_test.go`

- [ ] **Step 1: Schema — add `project` field**

Replace `GetCodeSnippetSchema` with:

```go
var GetCodeSnippetSchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["project"],
  "properties": {
    "project":  { "type": "string", "description": "Absolute path as a file:// URI (e.g. file:///abs/path). Must be in the registry; call index_repository first." },
    "id":       { "type": "string", "description": "Stable node id; mutually exclusive with name_path." },
    "name_path":{ "type": "string", "description": "Qualified name path; mutually exclusive with id." },
    "range":    { "type": "array", "items": { "type": "integer" }, "minItems": 2, "maxItems": 2 }
  },
  "additionalProperties": false
}`)
```

- [ ] **Step 2: Args struct**

```go
type GetCodeSnippetArgs struct {
	Project  string `json:"project"`
	ID       string `json:"id"`
	NamePath string `json:"name_path"`
	Range    []int  `json:"range"`
}
```

- [ ] **Step 3: Factory + handler** — same pattern; inner helper `getCodeSnippet(repo, a)`.

- [ ] **Step 4: Imports** — add `project`, `registry`.

- [ ] **Step 5: Tests** — switch to `tool.GetCodeSnippet(reg)`; add `project` to args.

- [ ] **Step 6: Run tests**

Run: `go test ./internal/tool/ -run TestGetCodeSnippet -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/tool/getcodesnippet.go internal/tool/getcodesnippet_test.go
git commit -m "feat(tool): get_code_snippet takes file:// project URI"
```

---

### Task 15: `internal/tool/architecture.go`

**Files:**
- Modify: `internal/tool/architecture.go`
- Modify: `internal/tool/architecture_test.go`

- [ ] **Step 1: Schema — add `project` field**

Replace `GetArchitectureSchema` with:

```go
var GetArchitectureSchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["project"],
  "properties": {
    "project":        { "type": "string", "description": "Absolute path as a file:// URI (e.g. file:///abs/path). Must be in the registry; call index_repository first." },
    "top":            { "type": "integer", "default": 10, "minimum": 1, "maximum": 100 },
    "include_cycles": { "type": "boolean", "default": true }
  },
  "additionalProperties": false
}`)
```

- [ ] **Step 2: Args struct**

```go
type GetArchitectureArgs struct {
	Project       string `json:"project"`
	Top           int    `json:"top"`
	IncludeCycles *bool  `json:"include_cycles"`
}
```

- [ ] **Step 3: Factory + handler** — same pattern; inner helper `getArchitecture(repo, a)`.

- [ ] **Step 4: Imports** — add `project`, `registry`.

- [ ] **Step 5: Tests** — switch to `tool.GetArchitecture(reg)`; add `project` to args.

- [ ] **Step 6: Run tests**

Run: `go test ./internal/tool/ -run TestGetArchitecture -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/tool/architecture.go internal/tool/architecture_test.go
git commit -m "feat(tool): get_architecture takes file:// project URI"
```

---

### Task 16: `internal/tool/querygraph.go`

**Files:**
- Modify: `internal/tool/querygraph.go`
- Modify: `internal/tool/querygraph_test.go`, `internal/tool/querygraph_bench_test.go` (skip bench file if benchmark-only)

- [ ] **Step 1: Schema — add `project` field**

Replace `QueryGraphSchema` with:

```go
var QueryGraphSchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["project"],
  "properties": {
    "project": { "type": "string", "description": "Absolute path as a file:// URI (e.g. file:///abs/path). Must be in the registry; call index_repository first." },
    "from":    { "type": "string", "description": "Single seed node id; mutually exclusive with seeds." },
    "seeds":   { "type": "array", "items": { "type": "string" }, "description": "Multi-seed set; mutually exclusive with from." },
    "follow":  { "type": "array", "items": { "enum": ["callers", "callees"] }, "default": ["callers", "callees"] },
    "depth":   { "type": "integer", "default": 2, "minimum": 1, "maximum": 5 },
    "limit":   { "type": "integer", "default": 100, "minimum": 1, "maximum": 1000 }
  },
  "additionalProperties": false
}`)
```

- [ ] **Step 2: Args struct**

```go
type QueryGraphArgs struct {
	Project string   `json:"project"`
	From    string   `json:"from"`
	Seeds   []string `json:"seeds"`
	Follow  []string `json:"follow"`
	Depth   int      `json:"depth"`
	Limit   int      `json:"limit"`
}
```

- [ ] **Step 3: Factory + handler** — same pattern; inner helper `queryGraph(repo, a)`.

- [ ] **Step 4: Imports** — add `project`, `registry`.

- [ ] **Step 5: Tests** — switch to `tool.QueryGraph(reg)`; add `project` to args. Update the bench file with the same change (skip the bench run if it's too slow).

- [ ] **Step 6: Run tests**

Run: `go test ./internal/tool/ -run TestQueryGraph -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/tool/querygraph.go internal/tool/querygraph_test.go
git commit -m "feat(tool): query_graph takes file:// project URI"
```

---

### Task 17: `internal/tool/detectchanges.go`

**Files:**
- Modify: `internal/tool/detectchanges.go`
- Modify: `internal/tool/detectchanges_test.go`

- [ ] **Step 1: Schema — add `project` field**

Replace `DetectChangesSchema` with:

```go
var DetectChangesSchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["project"],
  "properties": {
    "project": { "type": "string", "description": "Absolute path as a file:// URI (e.g. file:///abs/path). Must be in the registry; call index_repository first." },
    "base":    { "type": "string", "description": "Git ref (commit, branch, tag) to diff against head." },
    "head":    { "type": "string", "description": "Git ref for the new side of the diff." },
    "since":   { "type": "string", "description": "Shortcut for base=since..HEAD." }
  },
  "additionalProperties": false
}`)
```

- [ ] **Step 2: Args struct**

```go
type DetectChangesArgs struct {
	Project string `json:"project"`
	Base    string `json:"base"`
	Head    string `json:"head"`
	Since   string `json:"since"`
}
```

- [ ] **Step 3: Factory + handler** — same pattern; inner helper `detectChanges(repo, a)`.

- [ ] **Step 4: Imports** — add `project`, `registry`.

- [ ] **Step 5: Tests** — switch to `tool.DetectChanges(reg)`; add `project` to args.

- [ ] **Step 6: Run tests**

Run: `go test ./internal/tool/ -run TestDetectChanges -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/tool/detectchanges.go internal/tool/detectchanges_test.go
git commit -m "feat(tool): detect_changes takes file:// project URI"
```

---

## Phase 4: `get_graph_schema` factory simplification

### Task 18: `internal/tool/graphschema.go` — drop unused `*store.Repo` parameter

**Files:**
- Modify: `internal/tool/graphschema.go`
- Modify: `internal/tool/graphschema_test.go`

- [ ] **Step 1: Update factory signature**

Replace the existing `GetGraphSchema` function with:

```go
// GetGraphSchema returns a Handler that emits the graph schema.
// Repo-independent (the answer is constants today); the constructor
// takes no arguments so registration stays uniform across tools.
func GetGraphSchema() func(ctx context.Context, args json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		var a GetGraphSchemaArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("invalid get_graph_schema args: %w", err)
		}
		return &GraphSchemaResult{
			NodeKinds:    allNodeKinds(),
			EdgeKinds:    allEdgeKinds(),
			Layers:       append([]domain.LayerName(nil), domain.AllLayerNames...),
			DefaultEdges: append([]domain.EdgeKind(nil), domain.AllEdges...),
			CodeKinds:    codeKinds(),
			KindMap:      buildKindMap(),
			Provenance:   domain.YacttProvenance(),
		}, nil
	}
}
```

- [ ] **Step 2: Drop `store` import** if no other use remains in the file.

- [ ] **Step 3: Update tests**

In `internal/tool/graphschema_test.go`, every call:

```go
tool.GetGraphSchema(repo)
```

becomes:

```go
tool.GetGraphSchema()
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/tool/ -run TestGetGraphSchema -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tool/graphschema.go internal/tool/graphschema_test.go
git commit -m "refactor(tool): get_graph_schema factory drops unused repo parameter"
```

---

## Phase 5: Three targeting registry tools (with audit + TOFU hooks in index_repository)

### Task 19: `internal/tool/index_repository.go` — schema, audit + TOFU hooks

**Files:**
- Modify: `internal/tool/index_repository.go`
- Modify: `internal/tool/index_repository_test.go` (create if missing; otherwise modify)

- [ ] **Step 1: Schema — rename `path` → `project`**

Replace `IndexRepositorySchema` with:

```go
var IndexRepositorySchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["project"],
  "properties": {
    "project": { "type": "string", "description": "Absolute path as a file:// URI (e.g. file:///abs/path)." },
    "name":    { "type": "string", "description": "Optional display label; defaults to the basename of ` + "`project`" + `." },
    "mode":    { "type": "string", "enum": ["full", "moderate", "fast", "cross-repo-intelligence"], "description": "Indexing mode (ponytail: only ` + "`full`" + ` is wired today; the string is accepted for forward compatibility)." }
  },
  "additionalProperties": false
}`)
```

- [ ] **Step 2: Args struct**

```go
type IndexRepositoryArgs struct {
	Project string `json:"project"`
	Name    string `json:"name"`
	Mode    string `json:"mode"`
}
```

- [ ] **Step 3: Factory signature — gain emitStartup + warnTrust hooks**

Replace `IndexRepository` with:

```go
// IndexRepository returns a Handler that walks `args.Project`,
// records an entry in `reg`, and returns the persisted row.
// The handler is repo-independent at construction time so it
// works in registry-only mode.
//
// emitStartup and warnTrust are called on the FIRST successful
// index per process (memoised internally). Passing nil disables
// that side effect; the cmd-level wiring provides non-nil
// closures bound to the running binary's version + SHA-256.
func IndexRepository(reg *registry.Registry, emitStartup func(audit.Startup) error, warnTrust func()) func(ctx context.Context, args json.RawMessage) (any, error) {
	var once sync.Once
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		var a IndexRepositoryArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("invalid index_repository args: %w", err)
		}
		// ParseRef normalises (file:// → absolute path) so the
		// registry stores a canonical key. Reject anything that
		// doesn't parse as a local file:// URI.
		ref, err := project.ParseRef(a.Project)
		if err != nil {
			return nil, fmt.Errorf("index_repository: %w", err)
		}
		abs := ref.Path
		if _, err := os.Stat(abs); err != nil {
			return nil, fmt.Errorf("index_repository: %w", err)
		}
		mode := a.Mode
		if mode == "" {
			mode = "full"
		}
		if !isKnownMode(mode) {
			return nil, fmt.Errorf("index_repository: unknown mode %q (want full|moderate|fast|cross-repo-intelligence)", mode)
		}

		// One-shot store.Load to count files + detect languages.
		repo, errs, lerr := store.Load(abs, registry.LoadOptsWithDiskCache(abs)...)
		if lerr != nil {
			return nil, fmt.Errorf("index_repository: load: %w", lerr)
		}
		// Run audit + TOFU exactly once on first successful load
		// per process. Memoised by `once`; emitStartup and
		// warnTrust are nil-safe.
		once.Do(func() {
			if warnTrust != nil {
				warnTrust()
			}
			if emitStartup != nil {
				binSHA := "" // populated by cmd-level closure if relevant
				info := audit.Startup{
					Version:      "", // cmd-level closure binds version
					RepoRoot:     repo.Root(),
					MaxFiles:     store.DefaultMaxFiles,
					LoadedFiles:  len(repo.Files()),
					Grammars:     repoGrammars(),
					LSP:          repoLSP(repo),
					BinarySHA256: binSHA,
				}
				if err := emitStartup(info); err != nil {
					fmt.Fprintf(os.Stderr, "warning: startup audit emit: %v\n", err)
				}
			}
		})
		_ = repo.Close()

		name := a.Name
		if name == "" {
			name = filepath.Base(abs)
		}

		entry := registry.Entry{
			Name:      name,
			Path:      abs,
			IndexedAt: time.Now().UTC(),
			Files:     len(repo.Files()),
			Languages: detectLanguages(repo),
			Mode:      mode,
		}
		if uerr := reg.Upsert(entry); uerr != nil {
			return nil, fmt.Errorf("index_repository: upsert: %w", uerr)
		}
		return &IndexRepositoryResult{
			Entry:    entry,
			Warnings: len(errs),
		}, nil
	}
}
```

Add `repoGrammars()` and `repoLSP(repo)` helpers at the bottom of the file (move from `cmd/yactt/main.go::buildStartupInfo`). Add `"sync"` to imports.

- [ ] **Step 4: Move `buildStartupInfo` helpers**

Move `buildStartupInfo`'s body into two helpers in `internal/tool/index_repository.go`:

```go
func repoGrammars() []string {
	out := make([]string, 0)
	for _, l := range parser.All() {
		out = append(out, string(l.Name()))
	}
	return out
}

func repoLSP(repo *store.Repo) []audit.LSPEntry {
	var out []audit.LSPEntry
	for _, l := range parser.All() {
		_, toolName, ver := repo.LSPForLang(l.Name())
		out = append(out, audit.LSPEntry{
			Language: string(l.Name()),
			Tool:     toolName,
			Version:  ver,
		})
	}
	return out
}
```

Add `"github.com/kellenff/yactt/internal/audit"` and `"github.com/kellenff/yactt/internal/parser"` to imports.

- [ ] **Step 5: Tests — switch factory call signature**

In `internal/tool/index_repository_test.go` (create if missing):

```go
func TestIndexRepository_RegistersAndReturnsEntry(t *testing.T) {
	fx := repofixture.New(t)
	dir := t.TempDir()
	reg := registry.New(filepath.Join(dir, "projects.json"))
	h := tool.IndexRepository(reg, nil, nil)
	out, err := h(context.Background(), json.RawMessage(`{"project":"file://`+fx.Root+`"}`))
	if err != nil {
		t.Fatalf("index_repository: %v", err)
	}
	res, ok := out.(*tool.IndexRepositoryResult)
	if !ok {
		t.Fatalf("result type %T", out)
	}
	if res.Entry.Path != fx.Root {
		t.Errorf("Path = %q, want %q", res.Entry.Path, fx.Root)
	}
}

func TestIndexRepository_RejectsNonFileURI(t *testing.T) {
	dir := t.TempDir()
	reg := registry.New(filepath.Join(dir, "projects.json"))
	h := tool.IndexRepository(reg, nil, nil)
	_, err := h(context.Background(), json.RawMessage(`{"project":"git://example.com/foo"}`))
	if err == nil {
		t.Fatal("expected error for non-file:// URI")
	}
}

func TestIndexRepository_EmitsStartupOnce(t *testing.T) {
	fx := repofixture.New(t)
	dir := t.TempDir()
	reg := registry.New(filepath.Join(dir, "projects.json"))
	var calls int32
	emit := func(_ audit.Startup) error { atomic.AddInt32(&calls, 1); return nil }
	h := tool.IndexRepository(reg, emit, nil)
	for i := 0; i < 3; i++ {
		if _, err := h(context.Background(), json.RawMessage(`{"project":"file://`+fx.Root+`"}`)); err != nil {
			t.Fatalf("index_repository: %v", err)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("emitStartup calls = %d, want 1 (memoised)", got)
	}
}
```

Add `"sync/atomic"` to imports.

- [ ] **Step 6: Run tests**

Run: `go test ./internal/tool/ -run TestIndexRepository -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/tool/index_repository.go internal/tool/index_repository_test.go
git commit -m "feat(tool): index_repository takes file:// project URI + emits startup audit on first success"
```

---

### Task 20: `internal/tool/index_status.go`

**Files:**
- Modify: `internal/tool/index_status.go`
- Modify: `internal/tool/index_status_test.go`

- [ ] **Step 1: Schema — rename `path` → `project`**

Replace `IndexStatusSchema` with:

```go
var IndexStatusSchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["project"],
  "properties": {
    "project": { "type": "string", "description": "Absolute path as a file:// URI (e.g. file:///abs/path)." }
  },
  "additionalProperties": false
}`)
```

- [ ] **Step 2: Args struct**

```go
type IndexStatusArgs struct {
	Project string `json:"project"`
}
```

- [ ] **Step 3: Handler — parse the URI first**

Replace the handler body with:

```go
func IndexStatus(reg *registry.Registry) func(ctx context.Context, args json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		var a IndexStatusArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("invalid index_status args: %w", err)
		}
		ref, err := project.ParseRef(a.Project)
		if err != nil {
			return nil, fmt.Errorf("index_status: %w", err)
		}
		return indexStatusFor(reg, ref.Path)
	}
}
```

Move the existing body into `func indexStatusFor(reg *registry.Registry, path string)` (rename the existing function, dropping the args unmarshal).

- [ ] **Step 4: Imports** — add `project`.

- [ ] **Step 5: Tests** — switch to `{"project": "file://" + fx.Root}` in args.

- [ ] **Step 6: Run tests**

Run: `go test ./internal/tool/ -run TestIndexStatus -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/tool/index_status.go internal/tool/index_status_test.go
git commit -m "feat(tool): index_status takes file:// project URI"
```

---

### Task 21: `internal/tool/delete_project.go`

**Files:**
- Modify: `internal/tool/delete_project.go`
- Modify: `internal/tool/delete_project_test.go`

- [ ] **Step 1: Schema — rename `path` → `project`**

Replace `DeleteProjectSchema` with:

```go
var DeleteProjectSchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["project"],
  "properties": {
    "project": { "type": "string", "description": "Absolute path as a file:// URI (e.g. file:///abs/path)." }
  },
  "additionalProperties": false
}`)
```

- [ ] **Step 2: Args struct**

```go
type DeleteProjectArgs struct {
	Project string `json:"project"`
}
```

- [ ] **Step 3: Handler — parse the URI first**

Replace the handler body with:

```go
func DeleteProject(reg *registry.Registry) func(ctx context.Context, args json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		var a DeleteProjectArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("invalid delete_project args: %w", err)
		}
		ref, err := project.ParseRef(a.Project)
		if err != nil {
			return nil, fmt.Errorf("delete_project: %w", err)
		}
		return deleteProjectFor(reg, ref.Path)
	}
}
```

Move the existing body into `func deleteProjectFor(reg *registry.Registry, path string)`.

- [ ] **Step 4: Imports** — add `project`.

- [ ] **Step 5: Tests** — switch to `{"project": "file://" + fx.Root}` in args.

- [ ] **Step 6: Run tests**

Run: `go test ./internal/tool/ -run TestDeleteProject -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/tool/delete_project.go internal/tool/delete_project_test.go
git commit -m "feat(tool): delete_project takes file:// project URI"
```

---

## Phase 6: Persisted query

### Task 22: `internal/tool/persisted_query.go` — schema + runner project injection

**Files:**
- Modify: `internal/tool/persisted_query.go`
- Modify: `internal/tool/persisted_query_test.go`
- Modify: `internal/persisted/registry.go`

- [ ] **Step 1: Schema — add `project`**

Replace `PersistedQuerySchema` with:

```go
var PersistedQuerySchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["op", "project"],
  "properties": {
    "op":      { "type": "string", "description": "Registered op id (see persisted_query's tools/list description)." },
    "project": { "type": "string", "description": "Absolute path as a file:// URI. Injected into the called tool's args before dispatch." }
  },
  "additionalProperties": false
}`)
```

- [ ] **Step 2: Args struct**

```go
type PersistedQueryArgs struct {
	Op      string `json:"op"`
	Project string `json:"project"`
}
```

- [ ] **Step 3: Update `tool.PersistedQuery` factory**

Replace the existing factory with:

```go
func PersistedQuery(runner *persisted.Runner) func(ctx context.Context, args json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		var a PersistedQueryArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("invalid persisted_query args: %w", err)
		}
		return runner.Run(ctx, a.Op, a.Project, args)
	}
}
```

- [ ] **Step 4: Update `persisted.Runner.Run` signature**

In `internal/persisted/registry.go`, change the `Runner.Run` method to accept and forward the project URI:

```go
// Run dispatches op id `op` to its target tool. `project` is the
// file:// URI passed to persisted_query; it is injected into the
// target tool's args under the key "project" if the op's static
// args don't already supply one.
//
// rawArgs is the full persisted_query JSON (kept as json.RawMessage
// so we can pass it through to the inner tool's Unmarshal).
func (r *Runner) Run(ctx context.Context, op, project string, rawArgs json.RawMessage) (any, error) {
	reg, ok := r.ops[op]
	if !ok {
		return nil, fmt.Errorf("persisted_query: unknown op %q", op)
	}
	// Build the merged args: op.Args first, then caller-supplied
	// rawArgs overrides, then `project` (caller wins unless the op
	// explicitly set it).
	merged, err := mergeArgs(reg.Args, rawArgs, project)
	if err != nil {
		return nil, err
	}
	fn, ok := r.tools[reg.Tool]
	if !ok {
		return nil, fmt.Errorf("persisted_query: op %q targets unknown tool %q", op, reg.Tool)
	}
	return fn(ctx, merged)
}

// mergeArgs layers the caller's rawArgs on top of op.Args and
// injects `project` (file:// URI) into the result unless the op
// already supplied one.
func mergeArgs(opArgs map[string]any, rawArgs json.RawMessage, project string) (json.RawMessage, error) {
	// Decode rawArgs (the persisted_query caller's JSON) as a map
	// so we can layer it on top of opArgs.
	var callerArgs map[string]any
	if err := json.Unmarshal(rawArgs, &callerArgs); err != nil {
		return nil, fmt.Errorf("persisted_query: invalid args: %w", err)
	}
	out := make(map[string]any, len(opArgs)+len(callerArgs)+1)
	for k, v := range opArgs {
		out[k] = v
	}
	for k, v := range callerArgs {
		// Don't let the caller override the `project` field we
		// inject — that would let a caller bypass the URI check.
		if k == "project" {
			continue
		}
		out[k] = v
	}
	if _, ok := out["project"]; !ok {
		out["project"] = project
	}
	return json.Marshal(out)
}
```

- [ ] **Step 5: Tests** — `internal/tool/persisted_query_test.go` switches to `{"op":"onboarding","project":"file://..."}` in args; same for `internal/persisted/registry_test.go`.

- [ ] **Step 6: Run tests**

Run: `go test ./internal/tool/ -run TestPersistedQuery -v && go test ./internal/persisted/...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/tool/persisted_query.go internal/tool/persisted_query_test.go internal/persisted/registry.go internal/persisted/registry_test.go
git commit -m "feat(persisted): persisted_query takes file:// project URI; runner injects into target tool args"
```

---

### Task 23: `internal/persisted/example_ops.go` — drop `repo` placeholders

**Files:**
- Modify: `internal/persisted/example_ops.go`

- [ ] **Step 1: Drop `"repo": ""` from `onboarding` and `repo-map`**

Replace the two op definitions:

```go
r.MustRegister(Op{
	ID:          "onboarding",
	Description: "Repo map: tree_overview at depth 2 — top packages and files only.",
	Tool:        "tree_overview",
	Args: map[string]any{
		"depth": 2,
	},
})
r.MustRegister(Op{
	ID:          "repo-map",
	Description: "Top-level directory map: tree_overview at depth 1 — packages and notable files only.",
	Tool:        "tree_overview",
	Args: map[string]any{
		"depth": 1,
	},
})
```

(`architecture` and `graph_rag_demo` are unchanged — the runner injects `project`.)

- [ ] **Step 2: Run tests**

Run: `go test ./internal/persisted/...`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/persisted/example_ops.go
git commit -m "refactor(persisted): drop repo placeholder from onboarding/repo-map example ops"
```

---

## Phase 7: Fixture helper

### Task 24: `internal/store/repofixture/repofixture.go` — add `ProjectURI()`

**Files:**
- Modify: `internal/store/repofixture/repofixture.go`

- [ ] **Step 1: Add the helper**

Add to `repofixture.go`:

```go
// ProjectURI returns the fixture's repo root as a file:// URI,
// suitable for passing to any tool that accepts a `project` field.
// Convenience for tests so they don't have to concatenate the
// scheme by hand.
func (f *Fixture) ProjectURI() string {
	return "file://" + f.Root
}
```

- [ ] **Step 2: Run `go build` to verify**

Run: `go build ./...`
Expected: success.

- [ ] **Step 3: Commit**

```bash
git add internal/store/repofixture/repofixture.go
git commit -m "test(repofixture): add ProjectURI helper"
```

---

## Phase 8: CLI

### Task 25: `cmd/yactt/main.go` — drop positional path, build IndexHooks

**Files:**
- Modify: `cmd/yactt/main.go`

- [ ] **Step 1: Update `usage` and `mcp` branch**

Replace the `usage` const and the `case "mcp":` block:

```go
const usage = `yactt — federated code intelligence for AI agents

Usage:
  yactt overview <path>                   Print the top of the tree for a repo.
  yactt mcp serve [--audit-log=F]         Run the MCP server on stdio.
                                          Tools accept a file:// project URI; call
                                          index_repository first to register a repo.
  yactt chunk --repo <path> [options]     Emit AST-bounded NDJSON chunks to stdout.
                                          See "yactt chunk --help" for options.
  yactt hybrid --repo <path> --query Q    Run hybrid retrieval (structural + BM25 + vector,
                                          merged with RRF). See "yactt hybrid --help".
  yactt version                           Print version info.
  yactt help                              Show this message.
`
```

In `case "mcp":`, change the validation:

```go
case "mcp":
	if len(os.Args) < 3 || os.Args[2] != "serve" {
		fmt.Fprintln(os.Stderr, "usage: yactt mcp serve [--audit-log=F]")
		os.Exit(2)
	}
	if err := runMCPServe(os.Args[3:]); err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
```

- [ ] **Step 2: Rewrite `runMCPServe`**

Replace the existing `runMCPServe` with:

```go
func runMCPServe(args []string) error {
	var auditPath string
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "--audit-log="):
			auditPath = strings.TrimPrefix(a, "--audit-log=")
			if auditPath == "" {
				return errors.New("--audit-log=<path> requires a non-empty path")
			}
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag: %s", a)
		default:
			return fmt.Errorf("mcp serve: no longer takes a positional path; tools accept a file:// project URI in their args (got %q)", a)
		}
	}

	regPath := registry.DefaultPath()
	if regPath == "" {
		return errors.New("mcp serve: cannot resolve $XDG_CACHE_HOME or $HOME; set XDG_CACHE_HOME")
	}
	reg := registry.New(regPath)

	// Build the audit + TOFU hooks for IndexRepository. Each runs
	// at most once per process (memoised inside IndexRepository).
	binSHA, _ := audit.BinarySHA256(binaryPath())
	emitStartup := func(info audit.Startup) error {
		info.Version = version
		info.BinarySHA256 = binSHA
		return audit.EmitStartup(os.Stderr, info)
	}
	warnTrust := func() { warnInstallTrustChain(version, binSHA) }

	var (
		auditLogger *audit.Logger
		auditCloser io.Closer
	)
	if auditPath != "" {
		var lerr error
		auditLogger, auditCloser, lerr = audit.NewFileLogger(auditPath)
		if lerr != nil {
			return fmt.Errorf("open audit log %s: %w", auditPath, lerr)
		}
		defer func() {
			if auditCloser != nil {
				_ = auditCloser.Close()
			}
		}()
	}

	srv := mcp.NewServer(
		"yactt",
		version,
		"2024-11-05",
		os.Stdout,
		func() (io.Reader, error) { return os.Stdin, nil },
	)
	if auditLogger != nil {
		srv.WithAudit(auditLogger, audit.ExtractPaths)
	}
	registerAllTools(srv, reg, emitStartup, warnTrust)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	return srv.Serve(ctx)
}
```

- [ ] **Step 3: Update `registerAllTools`**

Replace its signature and body. No more `repo *store.Repo` parameter; pass `reg`, `emitStartup`, `warnTrust` to every code-intel factory:

```go
func registerAllTools(srv *mcp.Server, reg *registry.Registry, emitStartup func(audit.Startup) error, warnTrust func()) {
	// Four registry tools — available in BOTH legacy "single-repo"
	// and "registry" modes (the distinction is gone; the server
	// only runs in registry mode now).
	srv.RegisterTool(mcp.ToolDef{
		Name: "list_projects", Description: "Enumerate every project in the registry, sorted by path. Call first when an agent joins an MCP session and doesn't yet know which repos are available.",
		InputSchema: tool.ListProjectsSchema, OutputSchema: tool.ListProjectsOutputSchema,
		Handler: tool.ListProjects(reg),
	})
	srv.RegisterTool(mcp.ToolDef{
		Name: "index_repository", Description: "Required first call: walk a repo at `project` (file:// URI), write an entry to the registry, return the row. Emits a startup audit line on the first successful index per process.",
		InputSchema: tool.IndexRepositorySchema, OutputSchema: tool.IndexRepositoryOutputSchema,
		Handler: tool.IndexRepository(reg, emitStartup, warnTrust),
	})
	srv.RegisterTool(mcp.ToolDef{
		Name: "index_status", Description: "Registry row + per-repo cache freshness for `project`. Use this to check whether a repo is already indexed (`cacheFresh=true`) or whether `index_repository` needs to run first (`cacheFresh=false`).",
		InputSchema: tool.IndexStatusSchema, OutputSchema: tool.IndexStatusOutputSchema,
		Handler: tool.IndexStatus(reg),
	})
	srv.RegisterTool(mcp.ToolDef{
		Name: "delete_project", Description: "Evict `project` (file:// URI) from the registry and remove its per-repo cache directory. Idempotent on missing rows — safe to retry on a stale or half-deleted entry.",
		InputSchema: tool.DeleteProjectSchema, OutputSchema: tool.DeleteProjectOutputSchema,
		Handler: tool.DeleteProject(reg),
	})

	// 15 repo-bound code-intel tools — all take a `project` (file:// URI).
	tools := []mcp.ToolDef{
		{Name: "tree_overview", Description: "First call when orienting: map the repo structure (packages, files, top-level symbols). `project` is a file:// URI; `scope` narrows; `depth` 1-6 (default 2). Truncates at 16 KiB.", InputSchema: tool.TreeOverviewSchema, OutputSchema: tool.TreeOverviewOutputSchema, Handler: tool.TreeOverview(reg)},
		{Name: "node_get", Description: "Pull specific layers (summary/signature/body/source/tokens) for a stable node id.", InputSchema: tool.GetNodeSchema, OutputSchema: tool.GetNodeOutputSchema, Handler: tool.GetNode(reg)},
		{Name: "node_source", Description: "Lossless source text for a node or whole file.", InputSchema: tool.NodeSourceSchema, OutputSchema: tool.NodeSourceOutputSchema, Handler: tool.NodeSource(reg)},
		{Name: "node_edges", Description: "Single-hop callers/callees/tests/overrides/imports for a node.", InputSchema: tool.NodeEdgesSchema, OutputSchema: tool.NodeEdgesOutputSchema, Handler: tool.NodeEdges(reg)},
		{Name: "search", Description: "BM25 over symbol name + doc-comment.", InputSchema: tool.SearchSchema, OutputSchema: tool.SearchOutputSchema, Handler: tool.Search(reg)},
		{Name: "edit_impact", Description: "Required before any rename: returns the blast radius.", InputSchema: tool.EditImpactSchema, OutputSchema: tool.EditImpactOutputSchema, Handler: tool.EditImpact(reg)},
		{Name: "find_symbol", Description: "Glob over the name-path.", InputSchema: tool.FindSymbolSchema, OutputSchema: tool.FindSymbolOutputSchema, Handler: tool.FindSymbol(reg)},
		{Name: "get_symbols_overview", Description: "Top-level symbols of a file.", InputSchema: tool.GetSymbolsOverviewSchema, OutputSchema: tool.GetSymbolsOverviewOutputSchema, Handler: tool.GetSymbolsOverview(reg)},
		{Name: "find_code", Description: "Line-shaped patterns (regex or tree-sitter).", InputSchema: tool.FindCodeSchema, OutputSchema: tool.FindCodeOutputSchema, Handler: tool.FindCode(reg)},
		{Name: "search_code", Description: "Wraps find_code and groups matches by enclosing function.", InputSchema: tool.SearchCodeSchema, OutputSchema: tool.SearchCodeOutputSchema, Handler: tool.SearchCode(reg)},
		{Name: "find_referencing_symbols", Description: "Single-hop symbol-addressed alias of node_edges.", InputSchema: tool.FindReferencingSymbolsSchema, OutputSchema: tool.FindReferencingSymbolsOutputSchema, Handler: tool.FindReferencingSymbols(reg)},
		{Name: "get_graph_schema", Description: "Canonical node kinds, edge kinds, layer names.", InputSchema: tool.GetGraphSchemaSchema, OutputSchema: tool.GetGraphSchemaOutputSchema, Handler: tool.GetGraphSchema()},
		{Name: "get_code_snippet", Description: "Source slice for a symbol by id or name_path.", InputSchema: tool.GetCodeSnippetSchema, OutputSchema: tool.GetCodeSnippetOutputSchema, Handler: tool.GetCodeSnippet(reg)},
		{Name: "get_architecture", Description: "Repo-level health snapshot.", InputSchema: tool.GetArchitectureSchema, OutputSchema: tool.GetArchitectureOutputSchema, Handler: tool.GetArchitecture(reg)},
		{Name: "query_graph", Description: "Transitive reachability; single-seed (from) or multi-seed (seeds).", InputSchema: tool.QueryGraphSchema, OutputSchema: tool.QueryGraphOutputSchema, Handler: tool.QueryGraph(reg)},
		{Name: "detect_changes", Description: "Impact of a git-ref diff.", InputSchema: tool.DetectChangesSchema, OutputSchema: tool.DetectChangesOutputSchema, Handler: tool.DetectChanges(reg)},
	}
	for _, t := range tools {
		srv.RegisterTool(t)
	}

	// Persisted query registry. toolFuncs map uses the *registry.Registry-bound
	// handlers; the runner injects `project` into each called tool's args.
	toolFuncs := map[string]persisted.ToolFunc{
		"tree_overview":            tool.TreeOverview(reg),
		"node_get":                 tool.GetNode(reg),
		"node_source":              tool.NodeSource(reg),
		"node_edges":               tool.NodeEdges(reg),
		"search":                   tool.Search(reg),
		"edit_impact":              tool.EditImpact(reg),
		"find_symbol":              tool.FindSymbol(reg),
		"get_symbols_overview":     tool.GetSymbolsOverview(reg),
		"find_code":                tool.FindCode(reg),
		"search_code":              tool.SearchCode(reg),
		"find_referencing_symbols": tool.FindReferencingSymbols(reg),
		"get_graph_schema":         tool.GetGraphSchema(),
		"get_code_snippet":         tool.GetCodeSnippet(reg),
		"get_architecture":         tool.GetArchitecture(reg),
		"query_graph":              tool.QueryGraph(reg),
		"detect_changes":           tool.DetectChanges(reg),
	}
	registerPersistedQuery(srv, toolFuncs)
}
```

- [ ] **Step 4: Delete `buildStartupInfo` and the boot-time TOFU call**

Remove `buildStartupInfo` (moved into `tool/index_repository.go` as `repoGrammars` + `repoLSP`). The boot-time `warnInstallTrustChain(version, binSHA)` call moves into the `warnTrust` closure built in step 2.

- [ ] **Step 5: Update the persisted_query helper**

In `registerPersistedQuery` (now at the bottom of main.go), confirm the `Tool` field uses the new factories. No code change beyond ensuring the map in step 3 is correct.

- [ ] **Step 6: Run `go build`**

Run: `go build ./...`
Expected: success (with all earlier tasks complete).

- [ ] **Step 7: Commit**

```bash
git add cmd/yactt/main.go
git commit -m "refactor(cmd): mcp serve drops positional path; tools take file:// project URI"
```

---

### Task 26: `cmd/yactt/main_test.go` — add positional-path rejection test

**Files:**
- Modify: `cmd/yactt/main_test.go`

- [ ] **Step 1: Add the test**

Append to `cmd/yactt/main_test.go`:

```go
// TestMCPServe_RejectsPositionalPath pins the new contract: any
// non-flag positional after `mcp serve` is rejected with a clear
// message. The MCP server is stateless across tool calls; the
// target project now comes via tool args (a file:// URI), not via
// the CLI.
func TestMCPServe_RejectsPositionalPath(t *testing.T) {
	err := runMCPServe([]string{"/some/path"})
	if err == nil {
		t.Fatal("runMCPServe: expected error for positional path")
	}
	if !strings.Contains(err.Error(), "no longer takes a positional path") {
		t.Errorf("error = %v; want containing \"no longer takes a positional path\"", err)
	}
}

// TestMCPServe_AcceptsAuditLogOnly verifies that --audit-log=<f>
// is still accepted (it's the only flag), no positional required.
func TestMCPServe_AcceptsAuditLogOnly(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "audit.log")
	// We can't actually run the server (it would block on stdin),
	// but we can verify the flag parse path: the only error we'd
	// see at this stage is the audit-log open failure, NOT a
	// positional-path error.
	err := runMCPServe([]string{"--audit-log=" + logPath})
	if err == nil {
		// If we got past flag parsing, the test passed; the
		// server's blocked read from stdin doesn't surface here
		// because ctx isn't cancelled in this synchronous call.
		return
	}
	if strings.Contains(err.Error(), "no longer takes a positional path") {
		t.Errorf("unexpected positional-path error: %v", err)
	}
}
```

- [ ] **Step 2: Run tests**

Run: `go test ./cmd/yactt/ -run 'TestMCPServe' -v`
Expected: PASS for both.

- [ ] **Step 3: Commit**

```bash
git add cmd/yactt/main_test.go
git commit -m "test(cmd): mcp serve rejects positional path"
```

---

## Phase 9: Integration

### Task 27: `junie-extension/scripts/yactt-launcher.sh`

**Files:**
- Modify: `junie-extension/scripts/yactt-launcher.sh`

- [ ] **Step 1: Drop `find_project_root`**

Replace the entire file with:

```bash
#!/usr/bin/env bash
# MCP launcher for Junie. Execs yactt in registry mode; the agent
# is expected to call `index_repository` with a file:// project
# URI before invoking any code-intel tool. The Junie MCP config has
# no project-dir variable substitution, so the agent picks the
# project explicitly via tool args.
set -euo pipefail

# ponytail: dry-run prints a deprecation notice instead of exec'ing
# yactt. Used by the smoke test in test/launcher.test.sh to assert
# the old `find_project_root` behaviour is gone.
if [[ "${YACTT_LAUNCHER_DRY_RUN:-}" == "1" ]]; then
	echo "deprecated: yactt-launcher no longer resolves a project root; pass a file:// project URI via index_repository"
	exit 0
fi

exec yactt mcp serve
```

- [ ] **Step 2: Run `bash -n`**

Run: `bash -n junie-extension/scripts/yactt-launcher.sh`
Expected: no output (syntax OK).

- [ ] **Step 3: Commit**

```bash
git add junie-extension/scripts/yactt-launcher.sh
git commit -m "refactor(junie-launcher): drop find_project_root; rely on agent-driven file:// URI"
```

---

### Task 28: `junie-extension/test/launcher.test.sh`

**Files:**
- Modify: `junie-extension/test/launcher.test.sh`

- [ ] **Step 1: Update the dry-run assertion**

Find the existing dry-run assertion and replace it. The test previously asserted the launcher emitted a resolved path; now it should assert the deprecation message:

```bash
# Dry-run mode asserts the launcher no longer resolves a project
# root and emits the deprecation notice instead.
output=$(YACTT_LAUNCHER_DRY_RUN=1 bash junie-extension/scripts/yactt-launcher.sh)
if ! grep -q "deprecated" <<<"$output"; then
	echo "FAIL: dry-run output missing deprecation notice: $output"
	exit 1
fi
```

- [ ] **Step 2: Run the test**

Run: `bash junie-extension/test/launcher.test.sh`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add junie-extension/test/launcher.test.sh
git commit -m "test(junie-launcher): assert deprecation notice in dry-run mode"
```

---

### Task 29: `.mcp.json` at repo root — drop positional path if present

**Files:**
- Modify: `.mcp.json` (repo root)

- [ ] **Step 1: Inspect current contents**

```bash
cat .mcp.json
```

- [ ] **Step 2: Remove any positional path from the yactt server's args**

If `.mcp.json` contains something like:

```json
{ "mcpServers": { "yactt": { "command": "yactt", "args": ["mcp", "serve", "/abs/path"] } } }
```

Change to:

```json
{ "mcpServers": { "yactt": { "command": "yactt", "args": ["mcp", "serve"] } } }
```

If `.mcp.json` is already correct (`args: ["mcp", "serve"]`), no change needed; document this in the commit message.

- [ ] **Step 3: Commit (if changed)**

```bash
git add .mcp.json
git commit -m "chore(mcp): drop positional path from local mcp.json"  # only if changed
```

---

## Phase 10: Documentation

### Task 30: `README.md` — update Getting started + tool reference

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Replace the MCP quickstart**

Find the "Getting started" or equivalent section that says something like:

```
yactt mcp serve /path/to/your/repo
```

Replace with:

```
yactt mcp serve
```

And add a sentence explaining the new lifecycle:

> The server boots in registry mode — no repo loaded at startup. To work with a repo, the agent first calls `index_repository` with the repo's `file://` URI, then invokes code-intel tools (`tree_overview`, `find_symbol`, etc.) with the same URI in their `project` field.

- [ ] **Step 2: Document the `project` field**

In the tool reference section, add a paragraph:

> Every targeting tool takes a `project` field as a `file://` absolute-path URI (e.g. `file:///Users/me/code/myrepo`). The URI must point to a directory that's already in the registry — call `index_repository` first if it isn't. The deprecated `tree_overview` field `repo` is accepted for one release and emits a stderr notice.

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "docs(readme): mcp serve drops positional path; tools take file:// project URI"
```

---

### Task 31: `docs/design.md` — addendum

**Files:**
- Modify: `docs/design.md`

- [ ] **Step 1: Add the addendum**

Append a new section at the bottom:

```markdown
## Appendix: MCP project-reference migration (post-v0.x)

After the project-reference migration, the MCP server runs only in
what used to be called "registry mode". The single-repo boot path
is gone; every code-intel tool takes a `file://` project URI in
its `args.project` field and resolves it via the registry on each
call. See `docs/snowball/specs/2026-07-12-mcp-cli-project-reference-design.md`
for the design and `docs/snowball/plans/2026-07-12-mcp-cli-project-reference.md`
for the implementation plan.

Key trade-offs:

- The CLI's positional-path argument was removed (not deprecated)
  because the same information is now on the wire inside tool args.
- `tree_overview`'s existing `repo` field is accepted as a
  deprecated alias for one release and emits a per-tool stderr
  notice when seen; it is removed in the next release.
- LSP client lifetime on cold paths is explicitly out of scope; a
  future daemon / HTTP-server mode is the right place to solve it.
```

- [ ] **Step 2: Commit**

```bash
git add docs/design.md
git commit -m "docs(design): addendum for MCP project-reference migration"
```

---

### Task 32: New `CHANGELOG.md`

**Files:**
- Create: `CHANGELOG.md`

- [ ] **Step 1: Write the breaking-changes entry**

Create `CHANGELOG.md`:

```markdown
# Changelog

## Unreleased

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

**Why now:** unifies the wire shape across the registry tools
(`index_repository`, `index_status`, `delete_project`) and the
code-intel tools, instead of leaving a long-tail of mixed shapes
that agents have to learn.

See `docs/snowball/specs/2026-07-12-mcp-cli-project-reference-design.md`
for the full design and rationale.
```

- [ ] **Step 2: Commit**

```bash
git add CHANGELOG.md
git commit -m "docs: add CHANGELOG with project-reference migration breaking changes"
```

---

## Phase 11: End-to-end test

### Task 33: `tests/acceptance/mcp_dispatch_test.go`

**Files:**
- Create: `tests/acceptance/mcp_dispatch_test.go`

- [ ] **Step 1: Write the end-to-end test**

Create `tests/acceptance/mcp_dispatch_test.go`:

```go
package acceptance_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kellenff/yactt/internal/audit"
	"github.com/kellenff/yactt/internal/mcp"
	"github.com/kellenff/yactt/internal/persisted"
	"github.com/kellenff/yactt/internal/registry"
	"github.com/kellenff/yactt/internal/store/repofixture"
	"github.com/kellenff/yactt/internal/tool"
)

// TestMCPServer_RegisterThenDrillIn is the closest thing to a
// regression test for the new lifecycle: index a fixture repo,
// then call tree_overview and find_symbol against it via the
// in-process MCP server. Drives the server's stdin/stdout directly.
func TestMCPServer_RegisterThenDrillIn(t *testing.T) {
	fx := repofixture.New(t)
	dir := t.TempDir()

	// Registry + audit log isolated from developer's $XDG_CACHE_HOME.
	regPath := filepath.Join(dir, "projects.json")
	reg := registry.New(regPath)

	// Per-test capture for the server's stdout (JSON-RPC responses).
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	t.Cleanup(func() { _ = stdoutR.Close(); _ = stdoutW.Close() })

	// Stub stdin: write one JSON-RPC request, then EOF.
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}

	// Build the server with a custom stdin source.
	srv := mcp.NewServer(
		"yactt", "test", "2024-11-05",
		stdoutW,
		func() (io.Reader, error) { return stdinR, nil },
	)
	srv.RegisterTool(mcp.ToolDef{
		Name: "index_repository", InputSchema: tool.IndexRepositorySchema, OutputSchema: tool.IndexRepositoryOutputSchema,
		Handler: tool.IndexRepository(reg, nil, nil),
	})
	srv.RegisterTool(mcp.ToolDef{
		Name: "tree_overview", InputSchema: tool.TreeOverviewSchema, OutputSchema: tool.TreeOverviewOutputSchema,
		Handler: tool.TreeOverview(reg),
	})
	srv.RegisterTool(mcp.ToolDef{
		Name: "find_symbol", InputSchema: tool.FindSymbolSchema, OutputSchema: tool.FindSymbolOutputSchema,
		Handler: tool.FindSymbol(reg),
	})

	// Drain stdout in a goroutine into a buffer so we can assert.
	var got bytes.Buffer
	doneCh := make(chan struct{})
	go func() {
		_, _ = io.Copy(&got, stdoutR)
		close(doneCh)
	}()

	// Drive the server: send initialize, index_repository,
	// tree_overview, find_symbol, then close stdin.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	requests := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"index_repository","arguments":{"project":"file://%s"}}}`, fx.Root),
		fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"tree_overview","arguments":{"project":"file://%s","depth":1}}}`, fx.Root),
		fmt.Sprintf(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"find_symbol","arguments":{"project":"file://%s","name_path":"main.Func","limit":5}}}`, fx.Root),
	}
	for _, r := range requests {
		if _, err := stdinW.WriteString(r + "\n"); err != nil {
			t.Fatalf("write stdin: %v", err)
		}
	}
	_ = stdinW.Close()

	if err := srv.Serve(ctx); err != nil && !strings.Contains(err.Error(), "EOF") {
		t.Fatalf("Serve: %v", err)
	}
	<-doneCh

	// Parse responses: 4 ids (1..4), all should have non-null results.
	lines := strings.Split(strings.TrimRight(got.String(), "\n"), "\n")
	if len(lines) < 4 {
		t.Fatalf("got %d response lines; want >= 4\n%s", len(lines), got.String())
	}
	for i, line := range lines[:4] {
		var resp struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  any             `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			t.Fatalf("response %d not JSON: %v\n%s", i, err, line)
		}
		if resp.Error != nil {
			t.Errorf("response %d: error = %v", i, resp.Error)
		}
		if len(resp.Result) == 0 {
			t.Errorf("response %d: empty result", i)
		}
	}

	// Verify the registry actually contains the fixture entry.
	entries, err := reg.List()
	if err != nil {
		t.Fatalf("registry.List: %v", err)
	}
	if len(entries) != 1 || entries[0].Path != fx.Root {
		t.Errorf("registry entries = %+v; want exactly the fixture root", entries)
	}
}

// TestPersistedQuery_ProjectInjection verifies the runner injects
// the persisted_query caller's project URI into the target tool's
// args (covers Phase 6 task 22).
func TestPersistedQuery_ProjectInjection(t *testing.T) {
	fx := repofixture.New(t)
	dir := t.TempDir()
	reg := registry.New(filepath.Join(dir, "projects.json"))
	idx := tool.IndexRepository(reg, nil, nil)
	if _, err := idx(context.Background(), json.RawMessage(fmt.Sprintf(`{"project":"file://%s"}`, fx.Root))); err != nil {
		t.Fatalf("seed: %v", err)
	}

	preg := persisted.NewRegistry()
	persisted.RegisterExampleOps(preg)
	runner := persisted.NewRunner(preg, map[string]persisted.ToolFunc{
		"tree_overview": tool.TreeOverview(reg),
	})

	out, err := runner.Run(context.Background(), "repo-map", "file://"+fx.Root, json.RawMessage(`{"project":"file://`+fx.Root+`"}`))
	if err != nil {
		t.Fatalf("runner.Run: %v", err)
	}
	if out == nil {
		t.Fatal("nil result")
	}
}
```

Add necessary imports.

- [ ] **Step 2: Run the test**

Run: `go test ./tests/acceptance/ -run 'TestMCPServer_RegisterThenDrillIn|TestPersistedQuery_ProjectInjection' -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add tests/acceptance/mcp_dispatch_test.go
git commit -m "test(acceptance): mcp dispatch end-to-end (register then drill in)"
```

---

## Phase 12: Final verification

### Task 34: Run the full test suite + build

- [ ] **Step 1: Run all unit tests**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 2: Run acceptance tests**

Run: `go test ./tests/acceptance/...`
Expected: PASS.

- [ ] **Step 3: Run `go vet` and `go build`**

Run: `go vet ./... && go build ./...`
Expected: no errors.

- [ ] **Step 4: Run the Junie launcher smoke test**

Run: `bash junie-extension/test/launcher.test.sh`
Expected: PASS.

- [ ] **Step 5: Verify the breaking-change migration on a real repo**

Run by hand:

```bash
cd /tmp && rm -rf yactt-mcp-smoke && git clone --depth=1 https://github.com/kellenff/yactt.git yactt-mcp-smoke
go build -o /tmp/yactt ./cmd/yactt
/tmp/yactt mcp serve /tmp/yactt-mcp-smoke  # should error: no longer takes a positional path
/tmp/yactt mcp serve                        # should boot cleanly
# Now drive tools/call index_repository, tree_overview, find_symbol
# via a tiny JSON-RPC client (or jsonrpc_test.go harness). Confirm
# each returns a non-empty result.
```

Expected: positional-path invocation errors; no-arg boot works; the three-call chain produces valid results.

- [ ] **Step 6: Final commit (no code changes; just a tag)**

```bash
git tag -a v0.x-pre-mcp-cli-project-reference -m "Pre-release snapshot for MCP project-reference migration"
```

---

## Self-Review

**1. Spec coverage:**

| Spec section | Plan task |
|---|---|
| §2 Decisions (locked in) | All tasks implement these decisions |
| §4 `internal/project` package | Tasks 1, 2 |
| §5 Data flow | Tasks 3–17 implement the per-call flow |
| §6.1 15 code-intel tools gain `project` | Tasks 3–17 (one per tool) |
| §6.2 `tree_overview` legacy alias | Task 3 |
| §6.3 3 registry tools rename `path` → `project` | Tasks 19–21 |
| §6.4 `persisted_query` schema | Task 22 |
| §6.5 Example ops rewrite | Task 23 |
| §6.6 `get_graph_schema` factory simplification | Task 18 |
| §7 CLI | Task 25 |
| §8 Integration updates | Tasks 27–29 |
| §9 Audit + TOFU placement | Task 19 (emitStartup hook) + Task 25 (warnTrust hook in main) |
| §10 Testing | Tasks 1, 2, 24, 26, 33, 34 |
| §11 Breaking changes | Tasks 30–32 (CHANGELOG, README, design.md) |
| §12 Risks (LSP deferred, cache key, case-insensitive FS) | §12 Risk 1 noted in CHANGELOG; Risk 2 in §10; Risk 3 flagged as follow-up |
| §13 Open items | Verified in Task 34 step 5 |

**2. Placeholder scan:** No "TBD", "TODO", "implement later", "similar to Task N" in the plan. Every step that changes code includes the actual code; every step that runs a command includes the expected output.

**3. Type consistency:** `project.Resolve(reg, raw)` (Task 1) is called identically across Tasks 3–17 and Task 33. `tool.IndexRepository(reg, emitStartup, warnTrust)` signature is consistent in Tasks 3 (seed helper), 19, 25, and 33. `Fixture.ProjectURI()` (Task 24) is referenced consistently in the test updates. `mergeArgs` (Task 22) signature is fixed. All factory signatures use `*registry.Registry` consistently.

**Issues found and fixed inline:**

- (none remaining after Task 1's `ctx` removal and the §9 IndexHooks clarification)

---

## Blast-Radius Before Handoff

Computed via `snowball:blast-radius` with preset `design` against every file path listed in the File Structure section. Summary:

- **Backend:** heuristic (codebase-memory graph not indexed for this repo).
- **Files touched:** ~38 files (3 new + 35 modified).
- **Decomposition flag:** ⚠️ plan spans 33 bite-sized tasks across 12 phases; execution will benefit from subagent-driven-development with phase-level checkpoints. The phases already provide natural breakpoints (Phase 4 / Phase 5 / Phase 11).
- **Recommended execution mode:** subagent-driven-development, with one fresh subagent per phase (not per task — phase boundaries are the natural review points).

---

## Execution Handoff

Plan complete and saved to `docs/snowball/plans/2026-07-12-mcp-cli-project-reference.md` (commit pending handoff choice).

**Two execution options:**

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per phase, review between phases, fast iteration on each tool-migration phase. Natural fit for a plan with 12 phases and ~33 bite-sized tasks.

**2. Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints at phase boundaries.

Which approach?