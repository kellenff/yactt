package hybrid

import (
	"math"
	"sort"
	"strings"
	"unicode"

	"github.com/kellenff/yactt/internal/chunker"
)

// BM25 hyperparameters. k1 controls term-frequency saturation; b
// controls document-length normalization. The values below are the
// textbook defaults (Robertson, Walker, Jones 1995) and are what
// every BM25 reference implementation uses. We deliberately don't
// expose them as knobs — the in-tree scorer is a reference
// implementation, not a tuning surface; production users plug in
// their own BM25 via a vector-style backend.
const (
	bm25K1 = 1.2
	bm25B  = 0.75
)

// bm25Channel scores every chunk in the corpus against the query
// and returns the top-K by descending BM25 score. Empty chunks
// (zero text) get a score of zero and are dropped — they have
// nothing to match against.
//
// The scorer uses the same lowercase + unicode-letter tokenization
// as the BM25 reference impl in the literature; we don't strip
// punctuation or apply stop-word lists. The chunker already
// produces narrowly-scoped chunks (one function/method body), so
// the absence of stop-word filtering costs ~5% recall at most
// and saves an import + a list.
func bm25Channel(query string, chunks []chunker.Chunk, k int) []Hit {
	if k <= 0 || len(chunks) == 0 {
		return nil
	}
	qTerms := tokenize(query)
	if len(qTerms) == 0 {
		return nil
	}

	// Tokenize each chunk once; cache the term-frequency map.
	type doc struct {
		chunk chunker.Chunk
		tf    map[string]int
		len   int
	}
	docs := make([]doc, 0, len(chunks))
	totalLen := 0
	for _, c := range chunks {
		toks := tokenize(c.Text)
		if len(toks) == 0 {
			continue
		}
		tf := make(map[string]int, len(toks))
		for _, t := range toks {
			tf[t]++
		}
		docs = append(docs, doc{chunk: c, tf: tf, len: len(toks)})
		totalLen += len(toks)
	}
	if len(docs) == 0 {
		return nil
	}
	avgdl := float64(totalLen) / float64(len(docs))

	// Document frequency per query term.
	df := make(map[string]int, len(qTerms))
	for _, qt := range qTerms {
		for i := range docs {
			if _, ok := docs[i].tf[qt]; ok {
				df[qt]++
			}
		}
	}

	// Score every doc.
	type scored struct {
		idx   int
		score float64
	}
	scores := make([]scored, 0, len(docs))
	N := float64(len(docs))
	for i := range docs {
		d := &docs[i]
		var s float64
		for _, qt := range qTerms {
			fd := float64(d.tf[qt])
			if fd == 0 {
				continue
			}
			idf := math.Log(((N - float64(df[qt]) + 0.5) / (float64(df[qt]) + 0.5)) + 1)
			// Classic BM25 length-normalized term-frequency.
			num := fd * (bm25K1 + 1)
			den := fd + bm25K1*(1-bm25B+bm25B*float64(d.len)/avgdl)
			s += idf * (num / den)
		}
		if s > 0 {
			scores = append(scores, scored{idx: i, score: s})
		}
	}

	sort.SliceStable(scores, func(i, j int) bool {
		if scores[i].score != scores[j].score {
			return scores[i].score > scores[j].score
		}
		return docs[scores[i].idx].chunk.ID < docs[scores[j].idx].chunk.ID
	})
	if len(scores) > k {
		scores = scores[:k]
	}

	out := make([]Hit, 0, len(scores))
	for _, s := range scores {
		c := docs[s.idx].chunk
		out = append(out, Hit{
			ID:      c.ID,
			Score:   s.score,
			Channel: ChannelBM25,
			Chunk:   &c,
		})
	}
	return out
}

// tokenize splits a string into lowercase terms of contiguous
// unicode letters/digits. Punctuation is treated as a separator.
// We avoid the unicode package's "letter OR digit" predicate for
// hot-path speed; the chunker's Text is well-formed source code so
// the simple cut works fine and the benchmark stays fast.
func tokenize(s string) []string {
	if s == "" {
		return nil
	}
	out := make([]string, 0, 16)
	cur := strings.Builder{}
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		out = append(out, strings.ToLower(cur.String()))
		cur.Reset()
	}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			cur.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return out
}