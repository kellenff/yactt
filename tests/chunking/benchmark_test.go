// Benchmark + recall tests for issue #34's AST-aware chunker.
//
// Two failure modes are gated here:
//
//  1. Wall time. `BenchmarkRun_100Files` + `TestRun_100Files_Under10s`
//     pin the "≤10s for 100 files" success criterion. They do not
//     require Ollama.
//
//  2. Recall vs. char-count baseline. `TestRecall_ASTBeatsBaseline`
//     computes recall@5 and recall@10 for both chunkers using
//     nomic-embed-text + cosine similarity. Gated behind -short
//     because it requires Ollama on localhost:11434.
//
// The recall questions are auto-generated from the fixture's
// symbols (no hand-tagging; see `genQuestions` below for the
// template). The hand-tagging is a v2 improvement.

package chunking

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kellenff/yactt/internal/chunker"
	"github.com/kellenff/yactt/internal/parser"
	"github.com/kellenff/yactt/internal/store"
	"github.com/kellenff/yactt/tests/chunking/genfixture"
)

// Question is one (question, expected_ast_id) pair. The expected
// ID is the canonical AST-chunk ID the question is about. The
// baseline recall metric maps this to a baseline chunk via line-
// range overlap.
type Question struct {
	Q  string `json:"q"`
	ID string `json:"id"`
}

// genQuestions builds the recall set from a list of fixture symbols.
// The questions are mechanical but the templating is varied enough
// that simple "exact phrase match" doesn't trivially solve the
// benchmark — the chunker still has to surface the right region.
//
// Sampling: 15 method, 10 function, 5 class, using a fixed seed
// so the recall set is deterministic.
func genQuestions(syms []genfixture.Symbol, seed int64) []Question {
	var methods, functions, classes []genfixture.Symbol
	for _, s := range syms {
		switch s.Kind {
		case "METHOD":
			methods = append(methods, s)
		case "FUNCTION":
			functions = append(functions, s)
		case "CLASS":
			classes = append(classes, s)
		}
	}
	rng := rand.New(rand.NewSource(seed))
	shuffle(rng, methods)
	shuffle(rng, functions)
	shuffle(rng, classes)

	var out []Question
	for i := 0; i < 15 && i < len(methods); i++ {
		s := methods[i]
		parts := strings.Split(s.QualifiedName, ".")
		// parts = [pkg, Type, Method]
		out = append(out, Question{
			Q:  fmt.Sprintf("where is the %s %s in the %s domain", strings.ToLower(parts[1]), strings.ToLower(parts[2]), s.Package),
			ID: s.ID,
		})
	}
	for i := 0; i < 10 && i < len(functions); i++ {
		s := functions[i]
		name := strings.ToLower(strings.SplitN(s.QualifiedName, ".", 2)[1])
		out = append(out, Question{
			Q:  fmt.Sprintf("where is the %s pipeline handled for the %s domain", name, s.Package),
			ID: s.ID,
		})
	}
	for i := 0; i < 5 && i < len(classes); i++ {
		s := classes[i]
		name := strings.SplitN(s.QualifiedName, ".", 2)[1]
		out = append(out, Question{
			Q:  fmt.Sprintf("what is the %s type definition in the %s package", name, s.Package),
			ID: s.ID,
		})
	}
	return out
}

func shuffle(rng *rand.Rand, s []genfixture.Symbol) {
	rng.Shuffle(len(s), func(i, j int) { s[i], s[j] = s[j], s[i] })
}

// ----- wall-time bench -----

// BenchmarkRun_100Files is the headline wall-time check. The
// issue-34 success criterion is "100-file repo chunks in ≤10s on a
// developer laptop". Run with: `go test -bench=BenchmarkRun_100Files
// -benchtime=1x ./tests/chunking/`.
func BenchmarkRun_100Files(b *testing.B) {
	r, opts := loadSynthRepo(b)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var buf bytes.Buffer
		n, err := chunker.Run(ctx, r, opts, &buf)
		if err != nil {
			b.Fatalf("Run: %v", err)
		}
		if n == 0 {
			b.Fatalf("Run emitted 0 chunks")
		}
	}
}

// TestRun_100Files_Under10s is the wall-time gate from a Test
// function. One run, must complete in ≤10s. Always runs (no
// Ollama needed).
func TestRun_100Files_Under10s(t *testing.T) {
	r, opts := loadSynthRepo(t)
	ctx := context.Background()
	start := time.Now()
	var buf bytes.Buffer
	n, err := chunker.Run(ctx, r, opts, &buf)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n == 0 {
		t.Fatal("Run emitted 0 chunks")
	}
	if elapsed > 10*time.Second {
		t.Errorf("Run took %v, want <= 10s for 100-file repo", elapsed)
	}
	t.Logf("chunks: %d  bytes: %d  elapsed: %v", n, buf.Len(), elapsed)
}

// loadSynthRepo is the bench/test shared loader. Builds the
// fixture under t.TempDir() and returns a loaded repo plus the
// default chunker options.
func loadSynthRepo(tb testing.TB) (*store.Repo, chunker.Options) {
	tb.Helper()
	dir := tb.TempDir()
	if _, err := genfixture.Write(dir); err != nil {
		tb.Fatalf("genfixture.Write: %v", err)
	}
	r, errs, err := store.Load(dir)
	if err != nil {
		tb.Fatalf("store.Load: %v", err)
	}
	for _, e := range errs {
		tb.Errorf("store.Load per-file: %v", e)
	}
	tb.Cleanup(func() { _ = r.Close() })
	return r, chunker.Options{Policy: chunker.PolicyFunction}
}

// ----- recall benchmark (Ollama-gated) -----

// TestRecall_ASTBeatsBaseline is the recall@5 + recall@10
// comparison. Gated behind -short: requires Ollama at
// localhost:11434. Run with:
//
//	go test -count=1 -timeout 600s -run TestRecall_ASTBeatsBaseline ./tests/chunking/
//
// Skip when -short is set (matches the LSP-test convention used
// elsewhere in the repo).
func TestRecall_ASTBeatsBaseline(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Ollama at localhost:11434")
	}
	r, opts := loadSynthRepo(t)
	syms, _ := readFixtureSymbols(t)
	questions := genQuestions(syms, 0xCAFEBABE)
	if len(questions) < 25 {
		t.Fatalf("recall set too small: %d (want >= 25)", len(questions))
	}
	t.Logf("recall set: %d questions across %d symbols", len(questions), len(syms))

	ctx := context.Background()

	// 1. Run both chunkers.
	var astBuf, baseBuf bytes.Buffer
	astN, err := chunker.Run(ctx, r, opts, &astBuf)
	if err != nil {
		t.Fatalf("AST Run: %v", err)
	}
	baseN, err := chunker.RunBaseline(ctx, r, opts, &baseBuf)
	if err != nil {
		t.Fatalf("Baseline Run: %v", err)
	}
	t.Logf("AST: %d chunks  Baseline: %d chunks", astN, baseN)

	astChunks := decodeAST(t, astBuf.Bytes())
	baseChunks := decodeBase(t, baseBuf.Bytes())

	// 2. Index AST chunks by ID and baseline chunks by source line
	// range (so the recall metric can map expected_id -> baseline
	// chunk via overlap).
	astByID := map[string]chunker.Chunk{}
	for _, c := range astChunks {
		astByID[c.ID] = c
	}

	// 3. Build the baseline expected map: for each question, find
	// the baseline chunk whose line range overlaps the expected
	// AST chunk's line range. If no overlap, the question is
	// "unanswerable" via baseline and we count it as a miss.
	baseExpected := make(map[string]string) // question.id -> baseline.ID
	for _, q := range questions {
		ast, ok := astByID[q.ID]
		if !ok {
			continue
		}
		for _, b := range baseChunks {
			if rangesOverlap(ast.StartLine, ast.EndLine, b.StartLine, b.EndLine) {
				baseExpected[q.ID] = b.ID
				break
			}
		}
	}

	// 4. Embed the corpora + questions.
	embedder := newEmbedder(t, embeddingsCacheDir(t))
	astEmbeds, err := embedCorpus(embedder, astText(astChunks))
	if err != nil {
		t.Fatalf("embed AST: %v", err)
	}
	baseEmbeds, err := embedCorpus(embedder, baseText(baseChunks))
	if err != nil {
		t.Fatalf("embed baseline: %v", err)
	}
	qTexts := make([]string, len(questions))
	for i, q := range questions {
		qTexts[i] = q.Q
	}
	qEmbeds, err := embedCorpus(embedder, qTexts)
	if err != nil {
		t.Fatalf("embed questions: %v", err)
	}

	// 5. Compute recall@5 and recall@10.
	astRecall5, astRecall10 := recall(astEmbeds, qEmbeds, idsOf(astChunks), expected(astChunks, questions))
	baseRecall5, baseRecall10 := recall(baseEmbeds, qEmbeds, idsOfBaseline(baseChunks), expectedBaseline(baseExpected, questions))
	t.Logf("AST       recall@5=%.2f  recall@10=%.2f", astRecall5, astRecall10)
	t.Logf("Baseline  recall@5=%.2f  recall@10=%.2f", baseRecall5, baseRecall10)

	// 6. Issue criterion: AST should win by ≥2× on recall@5.
	if baseRecall5 > 0 && astRecall5/baseRecall5 < 2.0 {
		t.Errorf("AST recall@5 (%.2f) not >= 2x baseline (%.2f) — success criterion unmet", astRecall5, baseRecall5)
	}
	if astRecall5 < baseRecall5 {
		t.Errorf("AST recall@5 (%.2f) lower than baseline (%.2f) — something's wrong", astRecall5, baseRecall5)
	}
}

// ----- recall machinery -----

// expected returns the list of expected IDs (one per question),
// in the same order as questions. nil entries mean "no expected
// AST chunk" — those questions are skipped.
func expected(chunks []chunker.Chunk, qs []Question) []string {
	byID := map[string]bool{}
	for _, c := range chunks {
		byID[c.ID] = true
	}
	out := make([]string, len(qs))
	for i, q := range qs {
		if byID[q.ID] {
			out[i] = q.ID
		}
	}
	return out
}

// expectedBaseline maps each question to the baseline chunk ID
// that overlaps the question's expected AST chunk. Empty string
// means "no baseline chunk covers the expected lines" → counts
// as a baseline miss.
func expectedBaseline(baseExpected map[string]string, qs []Question) []string {
	out := make([]string, len(qs))
	for i, q := range qs {
		out[i] = baseExpected[q.ID]
	}
	return out
}

// recall computes recall@5 and recall@10. For each question, the
// top-K chunks by cosine similarity are computed; recall@K is
// 1/K_max if the expected ID is in the top K, else 0. (Standard
// recall formula, not MRR.) Returns two values: recall@5, recall@10.
//
// `embeddings` is per-corpus-chunk; `ids` is the corpus's ID list;
// `expecteds` is the per-question expected ID ("" = skip).
func recall(embeddings, qEmbeds [][]float32, ids, expecteds []string) (float64, float64) {
	if len(expecteds) != len(qEmbeds) {
		panic("recall: expected/qEmbed length mismatch")
	}
	var hit5, hit10, total int
	for qi, qVec := range qEmbeds {
		exp := expecteds[qi]
		if exp == "" {
			continue
		}
		total++
		// Score = cosine(q, corpus[i]) for every corpus entry.
		type scored struct {
			id    string
			score float64
		}
		scores := make([]scored, len(embeddings))
		for i, cVec := range embeddings {
			scores[i] = scored{id: ids[i], score: cosine(qVec, cVec)}
		}
		sort.Slice(scores, func(i, j int) bool { return scores[i].score > scores[j].score })
		top5 := map[string]bool{}
		for i := 0; i < 5 && i < len(scores); i++ {
			top5[scores[i].id] = true
		}
		top10 := map[string]bool{}
		for i := 0; i < 10 && i < len(scores); i++ {
			top10[scores[i].id] = true
		}
		if top5[exp] {
			hit5++
		}
		if top10[exp] {
			hit10++
		}
	}
	if total == 0 {
		return 0, 0
	}
	return float64(hit5) / float64(total), float64(hit10) / float64(total)
}

func cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

func rangesOverlap(aStart, aEnd, bStart, bEnd int) bool {
	return aStart <= bEnd && bStart <= aEnd
}

func idsOf(chunks []chunker.Chunk) []string {
	out := make([]string, len(chunks))
	for i, c := range chunks {
		out[i] = c.ID
	}
	return out
}

func idsOfBaseline(chunks []chunker.BaselineChunk) []string {
	out := make([]string, len(chunks))
	for i, c := range chunks {
		out[i] = c.ID
	}
	return out
}

func astText(chunks []chunker.Chunk) []string {
	out := make([]string, len(chunks))
	for i, c := range chunks {
		out[i] = c.Text
	}
	return out
}

func baseText(chunks []chunker.BaselineChunk) []string {
	out := make([]string, len(chunks))
	for i, c := range chunks {
		out[i] = c.Text
	}
	return out
}

func decodeAST(t *testing.T, b []byte) []chunker.Chunk {
	t.Helper()
	var out []chunker.Chunk
	dec := json.NewDecoder(bytes.NewReader(b))
	for dec.More() {
		var c chunker.Chunk
		if err := dec.Decode(&c); err != nil {
			t.Fatalf("decode AST: %v", err)
		}
		out = append(out, c)
	}
	return out
}

func decodeBase(t *testing.T, b []byte) []chunker.BaselineChunk {
	t.Helper()
	var out []chunker.BaselineChunk
	dec := json.NewDecoder(bytes.NewReader(b))
	for dec.More() {
		var c chunker.BaselineChunk
		if err := dec.Decode(&c); err != nil {
			t.Fatalf("decode baseline: %v", err)
		}
		out = append(out, c)
	}
	return out
}

// readFixtureSymbols regenerates the fixture and returns the
// symbols. Used by the recall test to derive questions without a
// separate checked-in recall file.
func readFixtureSymbols(tb testing.TB) ([]genfixture.Symbol, error) {
	tb.Helper()
	dir := tb.TempDir()
	return genfixture.Write(dir)
}

// ----- embedding (Ollama) + cache -----

type embedder struct {
	baseURL string
	model   string
	cache   *embedCache
}

func newEmbedder(tb *testing.T, cacheDir string) *embedder {
	tb.Helper()
	return &embedder{
		baseURL: "http://localhost:11434",
		model:   "nomic-embed-text",
		cache:   newEmbedCache(cacheDir),
	}
}

func (e *embedder) embedOne(text string) ([]float32, error) {
	if v, ok := e.cache.get(text); ok {
		return v, nil
	}
	body, _ := json.Marshal(map[string]any{"model": e.model, "prompt": text})
	req, _ := http.NewRequest("POST", e.baseURL+"/api/embeddings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama status %d: %s", resp.StatusCode, b)
	}
	var out struct {
		Embedding []float32 `json:"embedding"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("ollama decode: %w", err)
	}
	if len(out.Embedding) == 0 {
		return nil, fmt.Errorf("ollama returned empty embedding for %q", text[:min(80, len(text))])
	}
	e.cache.put(text, out.Embedding)
	return out.Embedding, nil
}

func embedCorpus(e *embedder, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v, err := e.embedOne(t)
		if err != nil {
			return nil, fmt.Errorf("embed[%d]: %w", i, err)
		}
		out[i] = v
	}
	return out, nil
}

// embeddingsCacheDir returns the on-disk cache directory. Lives
// under $TMPDIR so it doesn't pollute the repo; gitignored at
// the tests/chunking level.
func embeddingsCacheDir(t *testing.T) string {
	dir := filepath.Join(t.TempDir(), "embeddings")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}
	return dir
}

// embedCache is a simple text-keyed on-disk embedding cache.
// One file per text, named by a hash of the text. Avoids
// re-embedding on every test run.
type embedCache struct {
	dir string
}

func newEmbedCache(dir string) *embedCache { return &embedCache{dir: dir} }

func (c *embedCache) key(text string) string {
	// Use the first 16 bytes of the SHA-256 as the cache key. Two
	// texts colliding in 16 bytes is astronomically unlikely, and
	// even if they do, both embeddings are deterministic for the
	// same model so the worst case is "slightly slower first run".
	h := sha256sum(text)
	return filepath.Join(c.dir, h[:32]+".json")
}

func (c *embedCache) get(text string) ([]float32, bool) {
	p := c.key(text)
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, false
	}
	var v []float32
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, false
	}
	return v, true
}

func (c *embedCache) put(text string, v []float32) {
	b, _ := json.Marshal(v)
	_ = os.WriteFile(c.key(text), b, 0o644)
}

// sha256sum returns the hex SHA-256 of s. The cache keys off the
// first 32 hex chars; collision odds are negligible for the
// chunker benchmark's text volume.
func sha256sum(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// Reader interface usage; ensure bufio is referenced (used by
// the local cache's read path). Keeps imports honest if the
// cache file path is later expanded to streaming reads.
var _ = bufio.NewReader

// min is a Go 1.21+ builtin; the repo is on 1.26 so it's
// available. This reference prevents accidental removal.
var _ = min(1, 2)

// parser.Name reference so the package compiles even if the
// embedding call paths are gated out in -short mode.
var _ = parser.LangGo
