# Plan: Fix #50 - Attach chunk payload to structural hits

## Issue Summary
The `structural` channel in `yactt hybrid` returns hits with `chunk: null`, preventing RRF merging with vector/BM25 hits. The structural index already has the data (`file`, `start_line`, `end_line`, etc.) but isn't attaching it to output Hits.

## Current Code Analysis
Need to understand:
1. `internal/hybrid/hybrid.go` - How structural channel builds Hit objects
2. `internal/entity/` package - Entity structure with file/line data
3. `internal/chunker/` package - Chunk type definition

## Implementation Plan

### Phase 1: Understand the codebase
- Read `internal/hybrid/hybrid.go` to see how structural hits are constructed
- Examine `internal/entity` types to see what data is available
- Check chunker package for expected Chunk payload shape

### Phase 2: Modify hybrid.go
- In the structural channel hit construction, extract file/line metadata from the entity
- Create a new Chunk-like struct or use existing chunker.Chunk type
- Attach this to Hit.Chunk field instead of null

### Phase 3: Update tests
- Add test coverage for structural-with-chunk case
- Verify existing tests still pass

## Key Files to Modify
1. `internal/hybrid/hybrid.go` - Main implementation
2. `internal/hybrid/*.go` - Potentially related files

## Questions to Answer
- Should we use the existing `chunker.Chunk` type or create a minimal embedded struct?
- Are there any other places in codebase that construct structural hits?
- What test coverage exists for hybrid.go currently?
