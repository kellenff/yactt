package tool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/id"
	"github.com/kellenff/yactt/internal/store"
)

// NodeSourceArgs is the typed boundary input for node_source.
type NodeSourceArgs struct {
	ID            string `json:"id"`
	Range         []int  `json:"range"`
	IncludeTrivia bool   `json:"include_trivia"`
}

// NodeSourceResult mirrors domain.Source; we re-export it to keep the tool's
// output shape explicit (and to give tests a stable target).
type NodeSourceResult struct {
	Text       string            `json:"text"`
	LineRange  domain.LineRange  `json:"lines"`
	Encoding   string            `json:"encoding"`
	Provenance domain.Provenance `json:"provenance"`
}

// NodeSourceSchema is the JSON Schema for node_source. Mirrors design §4.3.
var NodeSourceSchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "id": { "type": "string" },
    "range": {
      "type": "array",
      "items": { "type": "integer" },
      "description": "[startLine, endLine] (1-based, inclusive start, exclusive end)."
    },
    "include_trivia": { "type": "boolean", "default": false }
  },
  "required": ["id"],
  "additionalProperties": false
}`)

// NodeSource returns a Handler that emits the lossless source slice for an
// ID. Falls back to file content when the ID is `file:<path>`.
func NodeSource(repo *store.Repo) func(ctx context.Context, args json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		var a NodeSourceArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("invalid node_source args: %w", err)
		}
		if a.ID == "" {
			return nil, fmt.Errorf("node_source: id is required")
		}
		nodeID, err := id.Parse(a.ID)
		if err != nil {
			return nil, fmt.Errorf("node_source: %w", err)
		}

		// Map to (file, line range).
		file, sym, ok, lerr := repo.LocateSymbol(nodeID)
		if lerr != nil || !ok {
			return nil, fmt.Errorf("node_source: cannot locate %s", a.ID)
		}
		f, ferr := repo.CachedFile(file)
		if ferr != nil {
			return nil, ferr
		}
		startLine, endLine := sym.StartRow, sym.EndRow
		if len(a.Range) == 2 {
			startLine, endLine = a.Range[0], a.Range[1]
			if startLine < sym.StartRow {
				startLine = sym.StartRow
			}
			if endLine > sym.EndRow {
				endLine = sym.EndRow
			}
		}
		r := domain.LineRange{Start: startLine, End: endLine}
		var body string
		if a.IncludeTrivia {
			body, err = f.SliceWithTrivia(r)
		} else {
			body, err = f.Slice(r)
		}
		if err != nil {
			return nil, err
		}
		return &NodeSourceResult{
			Text:       body,
			LineRange:  r,
			Encoding:   "utf-8",
			Provenance: *prov(),
		}, nil
	}
}
