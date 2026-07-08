package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/entity"
	"github.com/kellenff/yactt/internal/parser"
	"github.com/kellenff/yactt/internal/store"
)

// GetSymbolsOverviewArgs is the typed input for get_symbols_overview.
// Mirrors design §4.8.
type GetSymbolsOverviewArgs struct {
	File string `json:"file"`
}

// SymbolsOverviewNode is one symbol entry.
type SymbolsOverviewNode struct {
	ID       string                `json:"id"`
	Kind     domain.NodeKind       `json:"kind"`
	Name     string                `json:"name"`
	Summary  string                `json:"summary"`
	Receiver *entity.ReceiverView  `json:"receiver,omitempty"`
}

// GetSymbolsOverviewSchema is the JSON Schema for get_symbols_overview.
var GetSymbolsOverviewSchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "file": { "type": "string" }
  },
  "required": ["file"],
  "additionalProperties": false
}`)

// GetSymbolsOverviewOutputSchema declares the structuredContent shape of
// get_symbols_overview. The handler returns the
// {symbols: [...], file: "..."} envelope object that pins the
// MCP-spec-required object shape on the wire.
var GetSymbolsOverviewOutputSchema = json.RawMessage(`{
  "type": "object",
  "required": ["symbols", "file"],
  "properties": {
    "symbols": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["id", "kind", "name", "summary"],
        "properties": {
          "id":       { "type": "string" },
          "kind":     { "type": "string", "description": "Domain kind (FUNCTION/METHOD/CLASS/MODULE). For the grammar-form and id-prefix mapping, call get_graph_schema and inspect kindMap." },
          "name":     { "type": "string" },
          "summary":  { "type": "string" },
          "receiver": { "type": ["object", "null"] }
        }
      }
    },
    "file": { "type": "string" }
  },
  "additionalProperties": false
}`)

// GetSymbolsOverview returns a Handler that emits the top-N structural
// outline of a file.
func GetSymbolsOverview(repo *store.Repo) func(ctx context.Context, args json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		var a GetSymbolsOverviewArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("invalid get_symbols_overview args: %w", err)
		}
		if a.File == "" {
			return nil, fmt.Errorf("get_symbols_overview: file is required")
		}
		// Match by either repo-relative or absolute path. The MCP client may
		// pass either; we accept both.
		file, ok := resolveFile(repo, a.File)
		if !ok {
			return nil, fmt.Errorf("get_symbols_overview: cannot find file %s", a.File)
		}
		syms := repo.Symbols(file)
		out := []SymbolsOverviewNode{}
		for _, s := range syms {
			out = append(out, buildOverviewNode(repo, file, s))
		}
		return map[string]any{"symbols": out, "file": file}, nil
	}
}

func buildOverviewNode(repo *store.Repo, file string, s parser.Symbol) SymbolsOverviewNode {
	ent := entityFromSymbol(s, file, repo)
	return SymbolsOverviewNode{
		ID:       ent.ID(),
		Kind:     ent.DomainKind(),
		Name:     s.Name,
		Summary:  ent.Summary(),
		Receiver: ent.ReceiverView(),
	}
}

// resolveFile attempts to find the on-disk absolute path matching either
// an absolute or repo-relative `p`. The match must land on a real file
// inside the repo — anything resolving outside the repo (via `..`,
// an absolute path to elsewhere, etc.) is rejected by the Files() filter
// at the end. `filepath.Clean` normalises `/./`, `//`, and trailing
// separators before the lookup so the match against repo.Files is
// apples-to-apples.
func resolveFile(repo *store.Repo, p string) (string, bool) {
	if strings.HasPrefix(p, repo.Root()) {
		return filepath.Clean(p), true
	}
	full := filepath.Join(repo.Root(), strings.TrimPrefix(p, "/"))
	for _, f := range repo.Files() {
		if f == full {
			return f, true
		}
	}
	return "", false
}
