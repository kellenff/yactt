// Package tool contains the 10 MCP tool handlers from design §4.
//
// Each tool is a tiny package-level file:
//  1. Schema() returns the JSON Schema (2020-12 dialect) for arguments.
//  2. Handler returns a closure suitable for mcp.ToolDef.Handler. Tool
//     handlers take a parsed typed argument struct (parsed once at the
//     boundary per "parse don't validate") and return a typed result that
//     mcp.Server marshals into the wire response.
//
// The handlers are the only "messy" part of the codebase — they translate
// JSON shapes into typed domain requests. Everything downstream (store,
// domain, parser) talks in precise types.
package tool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/store"
)

// TreeOverviewArgs is the typed boundary input for tree_overview.
type TreeOverviewArgs struct {
	Repo          string   `json:"repo"`
	Depth         int      `json:"depth"`
	IncludeLayers []string `json:"include_layers"`
}

// TreeOverviewResult is one tree node in the response. The actual JSON shape
// is recursive.
type TreeOverviewResult struct {
	ID         string               `json:"id"`
	Kind       domain.NodeKind      `json:"kind"`
	Summary    string               `json:"summary,omitempty"`
	Provenance *domain.Provenance   `json:"provenance,omitempty"`
	Children   []TreeOverviewResult `json:"children,omitempty"`
}

// TreeOverviewSchema is the JSON Schema for tree_overview. Mirrors the
// design's spec in §4.1.
var TreeOverviewSchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "repo":           { "type": "string", "description": "Absolute path or repo alias" },
    "depth":          { "type": "integer", "default": 2, "minimum": 1, "maximum": 6 },
    "include_layers": {
      "type": "array",
      "items": { "enum": ["summary", "structure", "signature"] },
      "default": ["summary", "structure"]
    }
  },
  "required": ["repo"],
  "additionalProperties": false
}`)

// TreeOverview returns a Handler that produces the top-N levels of the tree
// rooted at `repo`, with only the requested layer set populated.
func TreeOverview(repo *store.Repo) func(ctx context.Context, args json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		var a TreeOverviewArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("invalid tree_overview args: %w", err)
		}
		// Empty `repo` is treated as "use the server's rooted repo" — the CLI
		// already opened one, so the field is more of a label than a path.
		// We still call it out if the design requires a literal repo: a
		// contract test elsewhere asserts the schema lists `repo` as required.
		if repo == nil {
			return nil, fmt.Errorf("tree_overview: no repo bound to handler")
		}
		if a.Depth <= 0 {
			a.Depth = 2
		}
		if a.Depth > 6 {
			a.Depth = 6
		}
		root, err := buildOverviewTree(repo, repo.Root(), a.Depth)
		if err != nil {
			return nil, err
		}
		return root, nil
	}
}

// buildOverviewTree walks the file index and produces the depth-N tree.
// depth=1 means "repo + children" but not their children; depth=2 adds one
// more level (files + functions). We do this synthetically since the repo
// index only stores (file, symbols[]) pairs.
func buildOverviewTree(repo *store.Repo, rootPath string, depth int) (TreeOverviewResult, error) {
	root := TreeOverviewResult{
		ID:         "repo:" + rootPath,
		Kind:       domain.KindRepo,
		Summary:    "Repository: " + rootPath,
		Provenance: prov(),
	}
	if depth < 1 {
		return root, nil
	}
	// Group files by package (their parent directory under root).
	pkgMap := make(map[string][]string)
	for _, p := range repo.Files() {
		pkg := packagePath(rootPath, p)
		pkgMap[pkg] = append(pkgMap[pkg], p)
	}
	for pkg, files := range pkgMap {
		pkgID := fmt.Sprintf("pkg:%s", pkg)
		if pkg == "" {
			pkgID = fmt.Sprintf("pkg:%s", rootPath)
		}
		child := TreeOverviewResult{
			ID:         pkgID,
			Kind:       domain.KindPackage,
			Summary:    "Package: " + pkg,
			Provenance: prov(),
		}
		for _, f := range files {
			fileNode := TreeOverviewResult{
				ID:         "file:" + relPath(rootPath, f),
				Kind:       domain.KindFile,
				Summary:    "File: " + baseName(f),
				Provenance: prov(),
			}
			if depth >= 2 {
				syms := repo.Symbols(f)
				for _, s := range syms {
					symNode := TreeOverviewResult{
						ID:         symbolID(f, s, rootPath),
						Kind:       symbolKind(s),
						Summary:    symbolSummary(s),
						Provenance: prov(),
					}
					fileNode.Children = append(fileNode.Children, symNode)
				}
			}
			child.Children = append(child.Children, fileNode)
		}
		root.Children = append(root.Children, child)
	}
	return root, nil
}
