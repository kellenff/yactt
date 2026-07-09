# Chunker benchmark

Success-criterion gate for issue #34. Two test paths:

## Wall time (no Ollama required)

```sh
go test -v -count=1 -run TestRun_100Files_Under10s ./tests/chunking/
go test -bench=BenchmarkRun_100Files -benchtime=3x -run='^$' ./tests/chunking/
```

The 100-file synthetic repo chunks in ~95ms on an M3 Max. The
issue-34 success criterion is "≤10s on a developer laptop"; we
have ~100× headroom.

## Recall vs. char-count baseline (requires Ollama)

```sh
# In one terminal:
ollama serve  # with nomic-embed-text pulled
# In another:
go test -v -count=1 -timeout 600s -run TestRecall_ASTBeatsBaseline ./tests/chunking/
```

The test:
1. Builds the synthetic 100-file repo under `t.TempDir()`.
2. Runs both chunkers (AST and char-count baseline).
3. Generates 30 (question, expected-AST-id) pairs from the
   fixture's symbols.
4. Embeds every chunk's `text` and every question with
   `nomic-embed-text` via Ollama.
5. Computes recall@5 and recall@10 for both chunkers via
   brute-force cosine similarity.
6. Asserts AST recall@5 is ≥2× the baseline's.

Embeddings are cached on disk under `tests/chunking/.cache/`
(gitignored) so repeat runs are O(network) for new chunks only.

## What the fixture looks like

- 100 Go source files across 3 packages (`auth`, `payments`, `users`).
- ~734 declarations: ~503 methods, ~192 classes, ~39 top-level functions.
- Every 10th file is a "large" file: one type, 8 small methods.
  This is the case where the char-count baseline loses most —
  a 1000-char window combines multiple small methods.
- Cross-package calls in ~33% of methods, so the persisted
  call-edge index has real entries to surface.
- Generator uses a fixed PRNG seed (`0x1AC1AC1A`) so the
  fixture is byte-deterministic.

## What the recall set looks like

30 auto-generated questions:
- 15 method-level: `where is the <type> <method> in the <pkg> domain`
- 10 function-level: `where is the <name> pipeline handled for the <pkg> domain`
- 5 class-level: `what is the <Type> type definition in the <pkg> package`

The questions are templated from the symbol names — not
hand-tagged. A v2 should swap in a human-tagged set. Until
then, the recall metric measures "can the chunker surface the
region containing the named symbol", which is exactly the
contract a RAG pipeline needs.

## Regenerating the fixture

The generator (`genfixture/genfixture.go`) is deterministic; no
manual regeneration is needed. To force a fresh fixture (e.g.
when changing the seed), just `rm -rf .cache/`.

## Caveats

- AST recall@5 is currently 1.00 — the questions are too easy
  (each maps verbatim to the symbol name in the chunk text).
  A v2 should add paraphrased questions to make the test
  harder; the headline win (≥2× vs baseline) still holds
  because the baseline's recall is constrained by chunk size.
- The baseline uses fixed 1000-char windows with 200-char
  overlap. Real-world baselines vary; the 2× win should
  generalize, but pinning the exact ratio to a particular
  baseline is not the goal.
- Wall time is dominated by the chunker's per-symbol work, not
  the underlying parser. If the chunker ever regresses on
  performance, the bench will catch it.
