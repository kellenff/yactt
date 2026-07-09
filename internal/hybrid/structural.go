package hybrid

import (
	"strings"

	"github.com/kellenff/yactt/internal/search"
)

// structuralChannel runs the yactt structural search (existing
// internal/search package — name + doc + path matching, ranked by
// match strength). This is the channel unique to yactt: vector
// stores and BM25 can't produce symbol IDs ranked by name+path
// proximity, and they don't have the persisted call-graph at all.
func structuralChannel(opts Options, k int) []Hit {
	terms := splitTerms(opts.Query)
	if len(terms) == 0 {
		return nil
	}
	// The structural channel is the symbol/name index. It's a
	// term-overlap scorer, not a phrase scorer, so we pass each term
	// individually. Quoted phrases still work as a single term (the
	// search package's tokenizer preserves them); the scorer gives
	// them the same floor (0.9 for exact-name, 0.6 for substring).
	q := search.Query{
		Terms: terms,
		Limit: k,
	}
	res := search.Search(opts.Repo, q)
	if len(res) == 0 {
		return nil
	}
	out := make([]Hit, 0, len(res))
	for _, r := range res {
		// Coerce the empty Chunk to a per-channel "no chunk"
		// placeholder. We use nil here; the orchestrator's JSON
		// marshaller omits nil Chunks.
		// The search package's per-result kind is already a domain.NodeKind
		// but we don't surface it here — the structural channel's
		// score is the only per-channel signal the merge needs.
		out = append(out, Hit{
			ID:      r.Node.ID,
			Score:   r.Score,
			Channel: ChannelStructural,
			Chunk:   nil,
		})
	}
	return out
}

// splitTerms mirrors internal/tool/search.go's query tokenizer but
// returns the terms without the strict-quote enforcement the MCP
// tool uses — for the hybrid orchestrator we want to be lenient
// because we're one of three channels, and the BM25 + vector
// channels are even more lenient (they tokenize on whitespace).
// We deliberately do NOT import the tool package to keep the
// hybrid orchestrator free of the MCP layer (this orchestrator
// is a CLI, not an MCP tool).
func splitTerms(s string) []string {
	if s == "" {
		return nil
	}
	out := []string{}
	cur := strings.Builder{}
	inQuote := false
	for _, r := range s {
		switch r {
		case '"':
			// Tolerate unbalanced quotes — close on the next quote
			// OR end of string. The MCP tool's splitQuery rejects
			// them; the orchestrator forgives because it's a fuse,
			// not a gate.
			if inQuote {
				if cur.Len() > 0 {
					out = append(out, cur.String())
					cur.Reset()
				}
				inQuote = false
				continue
			}
			inQuote = true
		case ' ':
			if inQuote {
				cur.WriteRune(r)
				continue
			}
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		// Flush whatever's left, including any trailing open quote
		// (we just strip it; the term is still meaningful).
		out = append(out, cur.String())
	}
	return out
}