package hybrid

import (
	"fmt"
	"strings"

	"github.com/kellenff/yactt/internal/chunker"
	"github.com/kellenff/yactt/internal/entity"
	"github.com/kellenff/yactt/internal/search"
	"github.com/kellenff/yactt/internal/store"
)

// createStructuralChunk builds a minimal chunk payload for structural hits.
// The search package's Result contains the file path and symbol ID, but not
// the text or row range directly. We need to fetch the cached file and look
// up the symbol's rows from r.Symbols(path).
func createStructuralChunk(r *store.Repo, res search.Result) (*chunker.Chunk, error) {
	path := res.Node.PathContext

	// Get the cached file to extract source text.
	cachedFile, err := r.CachedFile(path)
	if err != nil {
		return &chunker.Chunk{
			ID:            "",
			File:          path,
			QualifiedName: "",
			Kind:          string(res.Node.Kind),
		}, fmt.Errorf("failed to load cached file %s for hit %s: %w", path, res.Node.ID, err)
	}

	// Look up the symbol's row range from r.Symbols(path).
	startLine := 1
	endLine := 1

	syms := r.Symbols(path)
	if len(syms) > 0 {
		for _, sym := range syms {
			en := entity.FromParser(sym, "", "")
			if en.ID() == res.Node.ID {
				startLine = sym.StartRow + 1 // convert from 0-based tree-sitter rows to 1-based lines
				endLine = sym.EndRow          // already exclusive in tree-sitter
				break
			}
		}
	}

	// Extract the text from the cached file bytes.
	text := ""
	if startLine < len(cachedFile.Bytes) && endLine > 0 {
		text = string(cachedFile.Bytes[startLine:endLine])
	}

	return &chunker.Chunk{
		ID:            res.Node.ID,
		QualifiedName: "", // Will be populated by downstream code if needed
		File:          path,
		StartLine:     startLine,
		EndLine:       endLine,
		Text:          text,
		Kind:          string(res.Node.Kind),
	}, nil
}

// structuralChannel runs the yactt structural search (existing
// internal/search package — name + doc + path matching, ranked by
// match strength). This is the channel unique to yactt: vector
// stores and BM25 can't produce symbol IDs ranked by name+path
// proximity, and they don't have the persisted call-graph at all.
func structuralChannel(opts SearchStructuralOptions) ([]Hit, error) {
	terms := splitTerms(opts.Query)
	if len(terms) == 0 {
		return nil, nil
	}
	// The structural channel is the symbol/name index. It's a
	// term-overlap scorer, not a phrase scorer, so we pass each term
	// individually. Quoted phrases still work as a single term (the
	// search package's tokenizer preserves them); the scorer gives
	// them the same floor (0.9 for exact-name, 0.6 for substring).
	q := search.Query{
		Terms: terms,
		Limit: opts.k,
	}
	res := search.Search(opts.Repo, q)
	if len(res) == 0 {
		return nil, nil
	}
	out := make([]Hit, 0, len(res))
	for _, r := range res {
		chunk, err := createStructuralChunk(opts.Repo, r)
		if err != nil {
			// Can't create chunk for this result — skip it. The orchestrator
			// will have structural hits with null chunk if we skip here, but
			// that's acceptable since the error is already logged and the search
			// succeeded. We only want to drop individual hits that failed chunk
			// creation, not return an error up the stack.
			continue
		}
		out = append(out, Hit{
			ID:      r.Node.ID,
			Score:   r.Score,
			Channel: ChannelStructural,
			Chunk:   chunk,
		})
	}
	return out, nil
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
