---
sessionId: session-260704-113606-1a1j
---

# Requirements

### Overview & Goals

Add focused unit tests for every internal package that currently has none. The project has end-to-end coverage via `tests/acceptance` (slow, full repo) and per-tool boundary coverage via `internal/contract` (fast, no repo), but the internal packages themselves — the building blocks — are untested in isolation. A regression in the LRU eviction policy, the symbol lookup, or the slice boundary handling would only surface through the slow acceptance suite, and a regression in something the acceptance suite does not exercise (e.g. multi-line signature materialization) would not surface at all.

The deliverable is fast, deterministic, per-package unit + collaboration tests for the nine internal packages that currently lack them.

### Scope

**In scope**
- New `*_test.go` files for: `cache`, `domain`, `id`, `summarizer`, `parser`, `source`, `store`, `search`, `mcp`.
- A tiny refactor to `internal/cache` exposing an injectable clock so TTL behavior can be tested without `time.Sleep`. Public API and behavior unchanged.
- Use of `t.TempDir()` for any file-system setup; inline string snippets for tree-sitter fixtures where possible.
- Matching the existing testing style: `t.Fatalf` / `t.Errorf` and `reflect.DeepEqual` — no third-party assertion library.

**Out of scope**
- `internal/tool/*` — already covered end-to-end by `tests/acceptance` and at the boundary by `internal/contract`. Adding per-handler unit tests would duplicate without adding coverage.
- `cmd/yactt/main.go` — main entry point, not directly unit-testable.
- Integration of new tests into the existing fixtures (`tests/fixtures/sample-go`) — new fixtures stay local to each package.
- Switching to testify or any third-party assertion library.

### Non-Functional Requirements

- Each new per-package test file runs in under 100 ms (no full repo loads; collaboration tests use `t.TempDir` + small Go snippets).
- No external services, network, or globals required to run the new tests.
- New tests run under the standard `go test ./...` invocation.
- No new runtime dependencies.

# Technical Design

### Current State

 Package | Has tests? | What's there | What's missing |
---|---|---|---|
 `cache` | ❌ | LRU + TTL logic | Unit tests |
 `domain` | ❌ | Value types (LineRange, Provenance, Symbol, Node, …) | Boundary-case tests |
 `id` | ❌ | Parse/format/helpers | All parsing, error sentinels, helpers |
 `mcp` | ❌ | JSON-RPC + dispatch | Wire-format round-trip, dispatch branches |
 `parser` | ❌ | tree-sitter dispatch + Go extractor | Detect/ByName/ExtractSymbols on real grammar |
 `search` | ❌ | Scoring + ranking | Scoring edges, kind filter, scope filter |
 `source` | ❌ | File load, Slice, Tokens | Boundary cases, tempfile round-trip |
 `store` | ❌ | Load, LocateSymbol, MaterializeNode | Walk, mtime invalidation, layer set, lookup semantics |
 `summarizer` | ❌ | FirstLine extraction | Doc-block edge cases |

`tool` has boundary + acceptance coverage; `cmd/yactt` is the entry point.

### Key Decisions

1. **No external assertion library.** Match the existing `t.Fatalf` / `t.Errorf` style. Use `reflect.DeepEqual` for struct comparison where needed. Keeps the dependency surface unchanged.
2. **Inline string snippets, not testdata files.** Tree-sitter parses byte slices; a 10-line Go function fits inline in a test. Avoids `testdata/` proliferation and keeps each test self-contained.
3. **`t.TempDir()` for any FS setup.** Auto-cleaned, no `os.RemoveAll` defer dance.
4. **Collaboration tests, not mocks.** For tree-sitter (parser, source, store) use the *real* grammar and the *real* file system. The point of the unit tests is to verify the *contract* — "extract these symbols from this Go file" — not to test mocked behavior. Mocks hide bugs the grammar would catch.
5. **Tiny cache refactor for clock injection.** `internal/cache/cache.go` hardcodes `time.Now()` in `GetLayer`. Replace with an unexported `now func() time.Time` field defaulting to `time.Now`. Tests in the same package set it to a controllable value. Public API and behavior unchanged.
6. **MCP tests via `bytes.Buffer` + stub `Handler`.** The Server writes JSON-RPC to an `io.Writer` and reads from an `io.Reader`. `bytes.Buffer` is a valid `io.Reader`/`io.Writer`; tests drive `Serve(ctx)` against a buffer and assert on the marshalled responses. Handlers are simple in-test stubs (no mock library).
7. **Skip `internal/tool/*` unit tests.** Already covered by `contract_test.go` (boundary, no repo) and `acceptance_test.go` (end-to-end on a real repo). Adding per-handler unit tests would duplicate without adding coverage.

### Proposed Changes

**`internal/cache`** (with small refactor)

- Add `now func() time.Time` to the `Cache` struct, default `time.Now` in `New`/`NewSized`.
- Replace the `time.Since(entry.ts) > ttl` call with `c.now().Sub(entry.ts) > ttl`.
- Add `internal/cache/cache_test.go`:
  - `TestCachePutGetFile` — round-trip; `GetFile` returns `ErrMiss` for unknown paths.
  - `TestCacheFileMRUInvalidation` — `PutFile` then `GetFile` with a different mtime → `ErrMiss` and the stale entry is dropped.
  - `TestCacheFileLRUEviction` — at fileCap, the least-recently-used is evicted; `MoveToFront` order is respected.
  - `TestCacheLayerRoundTrip` — `PutLayer`/`GetLayer` returns the bytes.
  - `TestCacheLayerTTL` — pin `c.now` to a fixed time, advance it past `semanticTTL`, expect `ErrMiss`.
  - `TestCacheLayerSummaryTTL` — same for summary layer using its longer TTL.
  - `TestCacheSnapshot` — counts files + layers.

**`internal/domain`**

- Add `internal/domain/types_test.go`:
  - `TestLineRangeContains` — inclusive lower, exclusive upper, edge cases.
  - `TestLineRangeLength` — empty, normal, inverted (zero).
  - `TestProvenanceNew` — `FetchedAt` is RFC3339 UTC; `Tool`/`Version` preserved.
  - `TestProvenanceWithFallback` — returns a copy with `FallbackUsed` set.
  - `TestProvenancePtr` — pointer round-trip.
  - `TestNodeKindIsCode` — covers FUNCTION, METHOD, CLASS, MODULE; FILE/PACKAGE/REPO return false.
  - `TestAllEdgesContains` — sanity: AllEdges contains callers/callees/tests.

**`internal/id`**

- Add `internal/id/id_test.go`:
  - `TestParseOK` — one table per `Kind` (repo, pkg, file, fn, meth, class, module).
  - `TestParseErrors` — `ErrEmpty`, `ErrUnknownKind`, `ErrBadFormat` via `errors.Is`.
  - `TestConstructors` — `id.Function("a", "", "B")` vs `id.Function("a", "r", "B")` round-trip via `Parse`.
  - `TestFunctionParts` — three-segment (pkg.receiver.name), two-segment (pkg.name), error cases.
  - `TestMethodParts` — three-segment, error on fewer segments, error on empty parts.
  - `TestClassParts` — two-segment, error cases.
  - `TestMustParse` — panics on bad input.

**`internal/summarizer`**

- Add `internal/summarizer/summarizer_test.go`:
  - `TestSummarizeDocComment` — `// foo` becomes `"Kind: foo"`.
  - `TestSummarizeFallback` — no doc, uses fallback line.
  - `TestSummarizeEmpty` — both empty returns `"Kind:"`.
  - `TestSummarizeKindFallback` — empty kind → `"Symbol: ..."`.
  - `TestFirstLineMultiLine` — picks first non-empty, ignoring leading blank lines and `//` markers.
  - `TestFirstLineBlockComment` — handles `/* ... */` and multi-line block comments (first line only).
  - `TestStripCommentMarkers` — `//`, `/*`, plain line, edge cases.

**`internal/parser`**

- Add `internal/parser/parser_test.go`:
  - `TestDetectGo` — `path.go` returns the Go language.
  - `TestDetectUnknownExtension` — `.xyz` returns `ErrUnsupported`.
  - `TestByName` — Go returns its impl, unknown returns `ErrUnsupported`.
  - `TestModulePath` — parses a tiny Go file `package auth` and asserts `"auth"`.
  - `TestExtractSymbolsGoFunction` — `function_declaration` produces a Symbol with the right name + line range.
  - `TestExtractSymbolsGoMethod` — `method_declaration` kind is `method_declaration`.
  - `TestExtractSymbolsGoType` — `type_declaration` (struct) produces a Symbol with the type name.
  - `TestExtractSymbolsMultiple` — a multi-decl file yields all three kinds.
  - `TestExtractSymbolsUnsupported` — non-Go language returns `ErrUnsupported`.
  - `TestSymbolKindMapping` — every parser kind maps to the expected domain kind.
  - `TestSymbolSummaryFormat` — format is `"<KindLabel>: <Name>"`.

**`internal/source`**

- Add `internal/source/source_test.go`:
  - `TestLoadFileOK` — tempfile with small Go content, `LoadFile` returns a `*File` with non-nil `Root`, correct `MTime`, and parsed bytes.
  - `TestLoadFileMissing` — nonexistent path returns `ErrReadFailed` (via `errors.Is`).
  - `TestLoadFileBinary` — random bytes parse without panic; assert `Root != nil` (tree-sitter is permissive).
  - `TestSliceBoundaries` — empty range → `ErrEmptyRange`; start<0 → `ErrRangeOOB`; end>lineCount → `ErrRangeOOB`; happy path returns joined lines.
  - `TestLines` — trailing newline is dropped; `LineCount` matches.
  - `TestSliceWithTrivia` — extends above/below to contiguous whitespace lines; preserves body content.
  - `TestTokens` — walks CST and emits named terminals; ranges fall inside the requested window; an empty leaf is dropped.

**`internal/store`**

- Add `internal/store/store_test.go`:
  - `TestLoadWalksGoFiles` — tempdir with two `.go` files + one `.txt`; the `.txt` is skipped, two files indexed.
  - `TestLoadSkipsHiddenDirs` — `.git/` and `.foo/` are skipped via `filepath.SkipDir`.
  - `TestCachedFileReReadsOnMtimeChange` — `t.TempDir`, write file, `CachedFile` returns it; modify the file (touch), `CachedFile` re-reads.
  - `TestFilesAndSymbols` — `Files()` lists every parsed file; `Symbols(file)` returns the top-level declarations.
  - `TestProvenance` — provenance is set with the `tree-sitter` tool and the version pin.
- Add `internal/store/resolver_test.go`:
  - `TestLookupByName` — name-only lookup hits the `("", name)` bucket.
  - `TestLookupByPkg` — pkg-only lookup walks every entry with that pkg.
  - `TestLookupByBoth` — most specific bucket wins.
  - `TestLookupSortedStable` — sorted by file path.
  - `TestLocateSymbolFile` — `id.File(...)` resolves to the file with lineCount endRow.
  - `TestLocateSymbolFunction` — `id.Function(pkg, "", name)` resolves to the file/sym.
  - `TestLocateSymbolClass` — `id.Class(pkg, name)` resolves to the type_declaration.
  - `TestLocateSymbolNotFound` — unknown returns `ErrNotFound`.
- Add `internal/store/node_test.go`:
  - `TestMaterializeNodeSummaryOnly` — only summary layer is populated.
  - `TestMaterializeNodeAllLayers` — request every layer; each is populated with non-zero content.
  - `TestMaterializeNodeSignatureMultiLine` — multi-line signature takes the full slice.
  - `TestMaterializeNodeBodyControlFlow` — branching vs. linear classification.
  - `TestMaterializeNodeTokens` — tokens populated for the line range.
  - `TestQuickNode` — summary-only materialization.
  - `TestBodyFor` — body layer populated.
  - `TestProvenanceOnLayers` — every layer carries the tree-sitter provenance with fallback marker.

**`internal/search`**

- Add `internal/search/search_test.go`:
  - `TestSearchExactNameMatch` — exact-name match scores 0.9; lands at top.
  - `TestSearchSubstringMatch` — substring is in `[0.6, 0.9)`; longer substring → higher score.
  - `TestSearchDocMatch` — match in doc-comment line scores 0.4; below name matches.
  - `TestSearchPathProximity` — same-directory file scores higher than different-directory.
  - `TestSearchKindFilter` — only functions matched when kind=function.
  - `TestSearchScopeFilter` — out-of-scope files excluded.
  - `TestSearchRegex` — regex pattern matches symbol names.
  - `TestSearchLimitTruncates` — results truncated to limit.
  - `TestSearchMiss` — unmatched query returns empty.
  - Uses a small tempdir-based repo fixture (3 Go files, distinct symbols) so the test is self-contained.

**`internal/mcp`**

- Add `internal/mcp/jsonrpc_test.go`:
  - `TestEncodeRequestRoundTrip` — `EncodeRequest` → JSON → `Request` → `EncodeRequest` is stable.
  - `TestEncodeErrorCodes` — every code in the package renders the right wire value.
  - `TestTextContentShape` — `TextContent(s)` yields `{type:"text", text:s}` JSON.
- Add `internal/mcp/server_test.go`:
  - `TestServerInitialize` — `initialize` request returns the registered protocol version + server info.
  - `TestServerToolsList` — registered tools appear with their schemas.
  - `TestServerToolsCallOK` — stub handler returns a result; the response includes `structuredContent` + the marshalled text.
  - `TestServerToolsCallHandlerError` — handler returning an error → `isError=true` and the error text.
  - `TestServerToolsCallUnknownTool` — `methodNotFound` error.
  - `TestServerMalformedJSON` — parse error response, server keeps running.
  - `TestServerWrongJSONRPCVersion` — invalid request error.
  - `TestServerNotificationNotWritten` — `notifications/initialized` produces no wire output.
  - Uses `bytes.Buffer` as the writer and an in-test stub `Handler` that records its input.

### File Structure

```
internal/cache/cache_test.go            (new)
internal/cache/cache.go                 (modified — clock injection, ~3 lines)
internal/domain/types_test.go           (new)
internal/id/id_test.go                  (new)
internal/summarizer/summarizer_test.go  (new)
internal/parser/parser_test.go          (new)
internal/source/source_test.go          (new)
internal/store/store_test.go            (new)
internal/store/resolver_test.go         (new)
internal/store/node_test.go             (new)
internal/search/search_test.go          (new)
internal/mcp/jsonrpc_test.go            (new)
internal/mcp/server_test.go             (new)
```

### Risks

- **Cache refactor.** Injecting `now` is the cleanest way to test TTL behavior. Production behavior is unchanged (default = `time.Now`). The risk is over-fitting the abstraction; mitigated by keeping `now` unexported and only used in the TTL check.
- **Test flakiness on `t.TempDir`.** Mac and Linux differ on tmpfs placement; `t.TempDir` is reliable across both. No mitigation needed beyond using `t.TempDir` consistently.
- **Tree-sitter permissiveness on edge-case input.** Tree-sitter is permissive — random bytes parse without error. Tests must not assume `LoadFile` rejects malformed input; assert `Root != nil` instead of expecting an error.
- **Existing test breakage.** The acceptance test relies on the sample-go fixture which has known structure (Login, ValidateToken, SetSession, Session, User). New package tests use small inline Go snippets and do not touch the fixture.

# Delivery Steps

### ✓ Step 1: Stage 1: Unit tests for pure foundation packages (domain, id, summarizer, cache)
Pure unit tests pass for domain, id, summarizer, and cache (including TTL behavior via the clock injection refactor).

- Added `internal/domain/types_test.go` covering `LineRange.Contains`/`Length`, `Provenance` constructors/`WithFallback`/`Ptr`, `NodeKind.IsCode`, and the `AllEdges`/`AllLayerNames` constants.
- Added `internal/id/id_test.go` covering `Parse` for every kind, error sentinels via `errors.Is`, the constructor round-trip, and the per-kind helper functions including all error branches.
- Added `internal/summarizer/summarizer_test.go` covering `Summarize` (doc comment, fallback, empty, kind fallback), `FirstLine` (multi-line, block comments), and edge cases.
- Refactored `internal/cache/cache.go` to expose an unexported `now func() time.Time` field defaulting to `time.Now`; replaced `time.Since(entry.ts) > ttl` with `c.now().Sub(entry.ts) > ttl`. Public API unchanged.
- Added `internal/cache/cache_test.go` covering `GetFile`/`PutFile` (round-trip, mtime invalidation, LRU eviction at `fileCap`), `GetLayer`/`PutLayer` (round-trip, semantic TTL expiry via injected clock, summary TTL expiry), and `Snapshot`.
- `go test ./internal/domain/... ./internal/id/... ./internal/summarizer/... ./internal/cache/...` passes (also under `-race`).

### ✓ Step 2: Stage 2: Parser collaboration tests with real tree-sitter

- Added `internal/parser/parser_test.go` covering `Detect` (Go via `.go`, unknown extension → `ErrUnsupported`), `ByName` (Go → impl, unknown → `ErrUnsupported`), `ModulePath` on small Go fixtures, `ExtractSymbols` on three small Go fixtures (function-only, method-only, type-only, mixed), `SymbolKind` mapping, and `SymbolSummary` format.
- All fixtures are inline `const` string literals parsed at test time via `sitter.ParseCtx` against the real Go grammar; no `testdata/` files.
- Fixed a real production bug in `Go.ModulePath`: `return ch.Child(j + 1).Content(source)` dereferenced past the package_identifier child; corrected to `return gc.Content(source)`. The function was exported but unused by any caller, so the bug was silent until the test exposed it.
- `go test ./internal/parser/...` passes; full module `go test ./...` still green (including `tests/acceptance`).

### ✓ Step 3: Stage 3: Source collaboration tests with tempfile + tree-sitter

- Added `internal/source/source_test.go` covering `LoadFile` (OK with tempfile + non-nil `Root`, missing path → `ErrReadFailed` via `errors.Is`, binary garbage parses without panic, empty file works), `Slice` (empty range → `ErrEmptyRange`, start<0 → `ErrRangeOOB`, start≥lineCount → `ErrRangeOOB`, end>lineCount → `ErrRangeOOB`, happy path returns joined lines, whole-file slice), `Lines`/`LineCount` (trailing newline dropped, interior empty lines preserved), `SliceWithTrivia` (extends to contiguous whitespace above/below, propagates empty-range error), and `Tokens` (emits named terminals with valid ranges; empty leaf dropped; restricted range falls inside window; invalid range falls back to whole file).
- Used `t.TempDir()` for every FS setup; auto-cleaned.
- `go test ./internal/source/...` passes.

### ✓ Step 4: Store + search collaboration tests with tempdir-based mini repo

- Added `internal/store/repofixture/repofixture.go` — a small tempdir-based Go repo builder shared by store + search tests (auth/login.go, auth/user.go, payments/pay.go, plus hidden `.git/`, `.foo/`, and a `notes.txt`).
- Added `internal/store/store_test.go` covering `Load` (walks `.go` files, skips `.txt` and hidden dirs `.git`/`.foo`), `CachedFile` (round-trip; mtime change triggers re-read), `Files`/`Symbols` cardinality, `Provenance`, `PackagePath`, and invalid-root cases.
- Added `internal/store/resolver_test.go` covering `Lookup` (by-name, by-pkg, by-both, pkg-only, sorted by file path, miss), `LocateSymbol` for file/function/class/method kinds, `ErrNotFound` for unknown IDs, and malformed-fn propagation.
- Added `internal/store/node_test.go` covering `MaterializeNode` (summary-only, all-layers, signature multi-line, body linear vs. branching control flow, tokens populated, doc-comment summary derivation), `QuickNode`, `BodyFor`, and per-layer provenance.
- Added `internal/search/search_test.go` covering exact-name match (0.9 floor), substring scoring, length-proximity bonus, doc-comment match, kind filter, scope filter, regex matching, default limit, miss, and sort-by-score.
- Fixed two production bugs surfaced by the new tests:
  - `LocateSymbol(method)` rejected every Go method because the bounds check (`m.EndRow <= e.Sym.EndRow`) assumed methods were nested in their type_declaration. Tree-sitter lifts Go methods to file scope, so the check never fires. Replaced with a name+file match.
  - `scoreSymbol` ignored doc comments (`doc := ""` was never populated) and refused to run when `Terms` was empty (regex-only queries fell through). Fixed both: added `Repo.DocComment(path, sym)` and a regex-only short-circuit.
- `go test ./internal/store/... ./internal/search/...` passes; full module `go test ./...` still green.

### ✓ Step 5: MCP collaboration tests with bytes.Buffer and stub handlers

- Added `internal/mcp/jsonrpc_test.go` covering `Request` round-trip, `EncodeError` for every standard code, `TextContent` shape, `EncodeResponse` shape, and `InitializeResult` wire layout.
- Added `internal/mcp/server_test.go` driving `Serve(ctx)` against a `bytes.Buffer` reader/writer with an in-test stub `Handler` that records its argument bytes. Cases covered: `initialize` returns registered protocol version + server info; `tools/list` returns registered tools with their schemas (semantically compared since JSON key order isn't stable); `tools/call` OK returns `Content` + `StructuredContent`; handler error → `isError=true` with the error text; unknown tool → `CodeMethodNotFound`; missing tool name → `CodeInvalidParams`; non-object params → `CodeInvalidParams`; malformed JSON → parse error then a successful next request (server keeps running); wrong `jsonrpc` version → `CodeInvalidRequest`; `notifications/initialized` produces no wire output; unsupported method → `CodeMethodNotFound`; multiple requests in order each get exactly one frame; duplicate tool registration panics; stdin-reader error surfaces from `Serve`.
- `go test -race ./...` is green across the whole module: cache, contract, domain, id, mcp, parser, search, source, store, summarizer, and the acceptance suite.
- `gofmt -l` is clean; `go vet ./...` is clean.

### ✓ Step 6: Address code review — method receiver disambiguation + bulk doc-comment fetch
The code review surfaced two follow-ups against the changes in Stages 1-5:

- `internal/search/search.go:62` calls `r.DocComment(path, sym)` once per symbol, which hits `CachedFile` (one `os.Stat`) per call. For N symbols across M files that's N syscalls where M would do.
- `internal/store/resolver.go:176` (in `LocateSymbol(method)`) matches any `method_declaration` with the requested name in the file, so two types with same-named methods in the same file collide. Fix requires capturing the receiver type at parse time.

#### Sub-steps

- Add `Receiver string` field to `parser.Symbol`. Populated only for `method_declaration` nodes; holds the receiver type's local name (e.g. `"Server"` for `func (s *Server) M()`).
- Add `goMethodReceiver(node, source)` helper in `internal/parser/lang_golang.go` that walks the receiver `parameter_list` → first `parameter_declaration` → `type` field, recursing through `pointer_type` and taking the rightmost `type_identifier` from `qualified_type`. Wired into `goFunctionSymbol` when `isMethod` is true.
- Tighten `LocateSymbol(method)` in `internal/store/resolver.go` to require `m.Receiver == e.Sym.Name` so methods on different types with the same name resolve to the correct owner.
- Add `Repo.DocComments(path string, decls []parser.Symbol) map[string]string` in `internal/store/store.go`. One `CachedFile` call per path; iterates `decls` once, keying by symbol name. Empty map if the file can't be loaded.
- Switch `search.Search` to call `DocComments` once per path (outside the per-symbol inner loop) and read from the map.
- Extend `internal/store/repofixture` with an `auth/multi.go` that defines two types (`Alpha`, `Beta`) each with a same-named method (`Ping`) so the disambiguation test has a real disambiguation case.
- Tests to add:
  - `internal/parser/parser_test.go`: receiver extraction for value, pointer, and qualified-type receivers.
  - `internal/store/resolver_test.go`: `meth:auth.Alpha.Ping` returns Alpha.Ping (not Beta.Ping); `meth:auth.Beta.Ping` returns Beta.Ping.
  - `internal/store/store_test.go`: `DocComments` returns the right doc per symbol.
  - `internal/search/search_test.go`: existing tests still pass; the scoreSymbol doc branch still scores correctly.
- Run `go test -race ./...` and `go vet ./...`. Confirm `gofmt -l` is clean.

#### Outcome

- Added `Receiver string` to `parser.Symbol`. Wired `goMethodReceiver` + `baseTypeName` into the Go driver; the receiver is the FIRST `parameter_list` child of the `method_declaration` (tree-sitter-go doesn't tag it with a field name). `pointer_type` indirection is followed via named-children iteration; `qualified_type` is reduced via the `name` field.
- Tightened `LocateSymbol(method)` to require `m.Receiver == e.Sym.Name`.
- Added `Repo.DocComments(path, decls)`; one `CachedFile` per path, keyed by symbol name. Switched `search.Search` to use it (one stat per file, not per symbol). Also switched `buildID` to use the captured receiver so method IDs in search hits reflect the right class.
- Extended `repofixture` with `auth/multi.go` declaring two types with same-named methods.
- New tests: `TestExtractSymbolsGoMethodReceiver` (value, pointer, qualified, function-without-receiver), `TestExtractSymbolsTwoMethodsSameNameDisambiguatedByReceiver`, `TestLocateSymbolMethodDisambiguatesSameName`, `TestLocateSymbolMethodUnknownClass`, `TestDocComments`, `TestDocCommentsUnknownPath`, `TestDocCommentsEmptyDecls`, `TestDocCommentMatchesSingleCall`. Updated `TestLoadWalksGoFiles`, `TestSymbolsByPathSnapshot`, `TestLookupByPkgOnly`, `TestLookupEmpty` to account for the new fixture file.
- `go test -race ./...` is green across every package including `tests/acceptance`. `gofmt -l` and `go vet ./...` are clean.