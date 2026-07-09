package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kellenff/yactt/internal/chunker"
	"github.com/kellenff/yactt/internal/store"
	"github.com/kellenff/yactt/internal/store/repofixture"
)

// loadChunkFixture returns a loaded repo against the standard
// repofixture. The chunk CLI tests use the same fixture the chunker
// package tests use so the symbol/edge plumbing is exercised
// end-to-end at the CLI layer too.
func loadChunkFixture(t *testing.T) *store.Repo {
	t.Helper()
	fix := repofixture.New(t)
	r, errs, err := store.Load(fix.Root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, e := range errs {
		t.Errorf("Load per-file err: %v", e)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// TestRunChunkPipeline_FunctionPolicy is the end-to-end smoke test:
// run the pipeline against the fixture, decode every line, assert
// the basic shape. Mirrors what the binary's stdout would look
// like in production.
func TestRunChunkPipeline_FunctionPolicy(t *testing.T) {
	r := loadChunkFixture(t)
	var stdout, stderr bytes.Buffer
	if err := runChunkPipeline(r, chunker.Options{Policy: chunker.PolicyFunction}, &stdout, &stderr); err != nil {
		t.Fatalf("runChunkPipeline: %v", err)
	}

	lines := splitNonEmpty(stdout.String())
	if len(lines) == 0 {
		t.Fatal("no chunks emitted")
	}
	for i, line := range lines {
		var c chunker.Chunk
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			t.Errorf("line %d not valid JSON: %v\nline: %s", i, err, line)
			continue
		}
		if c.ID == "" {
			t.Errorf("line %d: missing id", i)
		}
		if c.QualifiedName == "" {
			t.Errorf("line %d: missing qualified_name", i)
		}
		if c.File == "" {
			t.Errorf("line %d: missing file", i)
		}
		if c.Text == "" {
			t.Errorf("line %d: missing text", i)
		}
	}
	if !strings.Contains(stderr.String(), "chunks written") {
		t.Errorf("stderr missing summary line: %q", stderr.String())
	}
}

// TestRunChunkPipeline_ClassVsFunctionCount pins the policy ordering
// guarantee: class chunks are fewer than function chunks on the
// same repo.
func TestRunChunkPipeline_ClassVsFunctionCount(t *testing.T) {
	r := loadChunkFixture(t)
	var funcOut, classOut bytes.Buffer
	if err := runChunkPipeline(r, chunker.Options{Policy: chunker.PolicyFunction}, &funcOut, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := runChunkPipeline(r, chunker.Options{Policy: chunker.PolicyClass}, &classOut, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	fn := len(splitNonEmpty(funcOut.String()))
	cl := len(splitNonEmpty(classOut.String()))
	if cl >= fn {
		t.Errorf("class chunks (%d) should be fewer than function chunks (%d)", cl, fn)
	}
}

// TestRunChunkPipeline_NilRepo pins the error contract for the
// pipeline entry point. (runChunk wraps the error with the load
// step, so this test pins the no-repo path specifically.)
func TestRunChunkPipeline_NilRepo(t *testing.T) {
	err := runChunkPipeline(nil, chunker.Options{}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Error("runChunkPipeline(nil repo) returned no error")
	}
}

// TestRunChunkPipeline_OutputIsNDJSON pins the wire shape: exactly
// one JSON object per line, no wrapping array, no leading summary.
func TestRunChunkPipeline_OutputIsNDJSON(t *testing.T) {
	r := loadChunkFixture(t)
	var out bytes.Buffer
	if err := runChunkPipeline(r, chunker.Options{Policy: chunker.PolicyFunction}, &out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	lines := splitNonEmpty(out.String())
	if len(lines) == 0 {
		t.Fatal("no chunks emitted")
	}
	for i, line := range lines {
		// Each line must be a valid JSON object starting with '{'.
		if !strings.HasPrefix(line, "{") {
			t.Errorf("line %d not a JSON object: %q", i, line)
		}
		if !strings.HasSuffix(line, "}") {
			t.Errorf("line %d not a JSON object: %q", i, line)
		}
	}
	// Trailing newline after the last chunk (json.Encoder always
	// appends one). Doesn't have to be a single trailing newline;
	// just confirm there's no wrapped-array trailing bracket.
	if strings.HasSuffix(strings.TrimSpace(out.String()), "]") {
		t.Errorf("output wrapped in array; expected bare NDJSON")
	}
	_ = context.Background() // keep import used for future ctx-aware test
}

// TestRunChunk_RequiresRepo pins the CLI error contract: missing
// --repo returns an error that includes "required".
func TestRunChunk_RequiresRepo(t *testing.T) {
	err := runChunk([]string{})
	if err == nil {
		t.Fatal("runChunk([]) returned no error")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("error %q doesn't mention 'required'", err.Error())
	}
}

// TestRunChunk_UnknownFlag pins the unknown-flag error contract.
func TestRunChunk_UnknownFlag(t *testing.T) {
	err := runChunk([]string{"--repo", "/tmp/x", "--bogus"})
	if err == nil {
		t.Fatal("runChunk with unknown flag returned no error")
	}
	if !strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("error %q doesn't mention 'unknown flag'", err.Error())
	}
}

// TestRunChunk_UnknownPolicy pins the policy-validation contract.
func TestRunChunk_UnknownPolicy(t *testing.T) {
	err := runChunk([]string{"--repo", "/tmp/x", "--policy", "bogus"})
	if err == nil {
		t.Fatal("runChunk with bogus policy returned no error")
	}
	if !strings.Contains(err.Error(), "policy") {
		t.Errorf("error %q doesn't mention policy", err.Error())
	}
}

// TestRunChunk_PositionalRejected pins that chunk doesn't accept
// positional args (the only positional in `yactt` is the repo path
// for `overview` and `mcp serve`).
func TestRunChunk_PositionalRejected(t *testing.T) {
	err := runChunk([]string{"--repo", "/tmp/x", "extra-arg"})
	if err == nil {
		t.Fatal("runChunk accepted positional arg")
	}
	if !strings.Contains(err.Error(), "positional") {
		t.Errorf("error %q doesn't mention 'positional'", err.Error())
	}
}

// TestRunChunk_NonExistentRepo pins the load-error contract.
func TestRunChunk_NonExistentRepo(t *testing.T) {
	err := runChunk([]string{"--repo", "/no/such/path/yactt-please-do-not-create"})
	if err == nil {
		t.Fatal("runChunk with bad repo returned no error")
	}
	if !strings.Contains(err.Error(), "load") {
		t.Errorf("error %q doesn't mention 'load'", err.Error())
	}
}

// splitNonEmpty returns the non-empty trimmed lines of s. Used to
// walk the chunker's NDJSON output in tests.
func splitNonEmpty(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			out = append(out, t)
		}
	}
	return out
}
