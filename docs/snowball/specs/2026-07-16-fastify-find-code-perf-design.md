# Fastify `find_code` warm-path performance

## Problem

`BenchmarkHTTP_ToolsCall/large/find_code` (pinned `fastify/fastify` @ v5.9.0,
~298 source files) measures **~200 ms/op** after `index_repository` warm-up.
Sibling large tools (`find_symbol`, `tree_overview`, `list_projects`) stay in
the **0.3–0.5 ms** band. Medium `find_code` is ~1 ms. The large fixture is not
"intrinsically slow" — `find_code` pays a repeated parse tax.

## Evidence

CPU profile of the large `find_code` bench (`-cpuprofile`):

| Hot frame | Share |
|---|---|
| `runtime.cgocall` → `ts_parser_parse_string` | ~80% |
| `store.(*Repo).CachedFile` → `source.LoadFile` | dominant caller |
| `tool.findCodeRegex` | ~40% of total samples (timed loop) |

Repo shape: **298** indexed source files. In-memory LRU file cap at the
time of the bug: `cache.DefaultFileCap = 256` (since raised to 50_000).

## Root cause

1. `store.Load` populates `Repo.files` (unbounded map) for every source file
   but **never seeds** the LRU (`Repo.cache`).
2. `Repo.CachedFile` consults **only** the LRU, then on miss always calls
   `source.LoadFile` (full tree-sitter reparse + disk read).
3. `find_code` (regex) walks `repo.Files()` and calls `CachedFile` per path.
   With N=298 > cap=256, every call thrash-reparses the overflow set; the
   first post-Load call reparses the entire corpus because the LRU starts
   empty.

`project.Index` pinning is working — the slowdown is not cold `store.Load`
per tool call. It is per-file reparse inside an already-warm `*Repo`.

## Approaches

### A. Teach `CachedFile` to hit `Repo.files` (recommended)

On LRU miss, if `r.files[path]` exists and `f.MTime` matches `os.Stat`, return
that pointer (and refresh the LRU). Reparse only on true miss or mtime change.

- **Pros:** One-line semantic fix at the right layer; every CachedFile caller
  benefits (`find_code`, tree-sitter path, doc comments, materializers).
- **Cons:** None material — preserves mtime invalidation contract.
- **Blast:** `internal/store/store.go` + one regression test. Low risk.

### B. Seed the LRU during `Load`

`PutFile` every loaded file into the LRU.

- **Pros:** Minimal change to `CachedFile`.
- **Cons:** Still caps at 256 — Fastify keeps thrashing. Does not fix N>cap.

### C. Regex `find_code` bypasses parse trees

Scan `f.Bytes` / `os.ReadFile` without requiring a CST; context from
`symbolsByPath`.

- **Pros:** Regex path never needs tree-sitter at query time.
- **Cons:** Narrower win; tree-sitter `find_code` and other CachedFile
  callers still thrash. Complementary to A, not a substitute.

**Recommendation: A now.** Optionally follow with C if regex allocs
(`bytes.Split` per file) still dominate after A.

## Design (A)

### Behaviour

```
CachedFile(path):
  1. reject paths outside root
  2. Stat → mtime
  3. LRU GetFile(path, mtime) → hit? return
  4. RLock r.files[path]; if present && MTime==mtime → PutFile LRU; return
  5. else LoadFile + PutFile + update r.files/symbols (existing path)
```

### Invariants preserved

- mtime change forces reparse (existing `TestCachedFileRereadsOnMtimeChange`)
- paths outside root → `ErrNotFound`
- pinned Index / `Close` semantics unchanged

### Test

`TestCachedFile_UsesLoadResidentFileWithoutReread`: after `Load`, `chmod 0`
a resident source file. `CachedFile` must still succeed (reads from
`r.files`). Today it fails on `ReadFile` during reparse — RED before the
fix, GREEN after.

### Success criterion

`go test -bench='BenchmarkHTTP_ToolsCall/large/find_code' -benchtime=5x
./internal/mcp/transport/http/...` drops from ~200 ms/op to the same order of
magnitude as medium `find_code` (single-digit ms or better on this host),
with allocs/op falling well below the current ~79k.

### Measured (this host, `-benchtime=10x`)

| Size | Before | After |
|---|---|---|
| large/find_code | ~208 ms/op, ~79k allocs | **~6.2 ms/op**, ~66k allocs |
| medium/find_code | ~1.1 ms/op | ~0.75 ms/op |

~33× wall-clock win from eliminating tree-sitter reparse thrash. Remaining
allocs are mostly per-file `bytes.Split` in the regex scanner (approach C) —
follow-up, not required to clear the bench cliff.

## Follow-up (done)

- Raised `DefaultFileCap` 256 → 50_000 and `DefaultLayerCap` 1024 → 200_000
  so a monorepo-sized working set fits the hot LRU (memory is cheap vs
  reparse). Resident-map hit remains the correctness backstop.

## Out of scope

- Streaming HTTP responses for long tool calls
- Changing the Fastify pin or bench fixture size
- Regex-path line-scan alloc reduction (approach C)
