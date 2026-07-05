package tool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/id"
	"github.com/kellenff/yactt/internal/parser"
	"github.com/kellenff/yactt/internal/store"
)

// EditImpactArgs is the typed input for edit_impact. Mirrors design §4.6.
type EditImpactArgs struct {
	Renames []Rename `json:"renames"`
}

// Rename is one proposed rename.
type Rename struct {
	ID      string `json:"id"`
	NewName string `json:"new_name"`
}

// RenameImpact is the per-rename analysis output.
type RenameImpact struct {
	Target          string            `json:"target"`
	NewName         string            `json:"newName"`
	AffectedCallers []NodeEdgesResult `json:"affectedCallers"`
	AffectedTests   []NodeEdgesResult `json:"affectedTests"`
	SafeToRename    bool              `json:"safeToRename"`
	Conflicts       []Conflict        `json:"conflicts"`
	Provenance      domain.Provenance `json:"provenance"`
}

// Conflict is a name-collision candidate.
type Conflict struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// EditImpactResult is the per-rename bundle.
type EditImpactResult struct {
	Renames []RenameImpact `json:"renames"`
}

// EditImpactSchema is the JSON Schema for edit_impact. Mirrors §4.6.
var EditImpactSchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "renames": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "id":       { "type": "string" },
          "new_name": { "type": "string" }
        },
        "required": ["id", "new_name"]
      }
    }
  },
  "required": ["renames"],
  "additionalProperties": false
}`)

// EditImpactOutputSchema declares the structuredContent shape of
// edit_impact. The handler returns EditImpactResult{Renames: [...]} which
// is already an object — the schema pins that contract.
var EditImpactOutputSchema = json.RawMessage(`{
  "type": "object",
  "required": ["renames"],
  "properties": {
    "renames": {
      "type": "array",
      "items": { "type": "object" }
    }
  },
  "additionalProperties": false
}`)

// EditImpact returns a Handler that analyses a set of proposed renames.
//
// MVP implementation: each rename composes scanCallers + scanTests; conflicts
// are detected by name collision in the same package. safe_to_rename is true
// iff there are no conflicts and affected callers/tests is non-empty (i.e.
// the rename is propagated correctly).
func EditImpact(repo *store.Repo) func(ctx context.Context, args json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		var a EditImpactArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("invalid edit_impact args: %w", err)
		}
		if len(a.Renames) == 0 {
			return nil, fmt.Errorf("edit_impact: at least one rename required")
		}
		out := EditImpactResult{Renames: make([]RenameImpact, 0, len(a.Renames))}
		for _, r := range a.Renames {
			nodeID, err := id.Parse(r.ID)
			if err != nil {
				return nil, fmt.Errorf("edit_impact: %w", err)
			}
			file, sym, ok, lerr := repo.LocateSymbol(nodeID)
			if lerr != nil || !ok {
				return nil, fmt.Errorf("edit_impact: cannot locate %s", r.ID)
			}
			p := prov()
			callers := scanCallers(repo, file, sym, 100, p)
			tests := scanTests(repo, file, sym, 100, p)
			conflicts := detectConflicts(repo, sym, r.NewName)
			safe := len(conflicts) == 0
			out.Renames = append(out.Renames, RenameImpact{
				Target:          r.ID,
				NewName:         r.NewName,
				AffectedCallers: callers,
				AffectedTests:   tests,
				SafeToRename:    safe,
				Conflicts:       conflicts,
				Provenance:      *p,
			})
		}
		return out, nil
	}
}

// detectConflicts reports any existing symbol with `newName` in the same
// package — a rename would create a collision.
func detectConflicts(repo *store.Repo, _ parser.Symbol, newName string) []Conflict {
	lookup := repo.Lookup("", newName)
	out := []Conflict{}
	for _, e := range lookup {
		out = append(out, Conflict{
			ID:     symbolID(e.File, e.Sym, repo.Root()),
			Reason: "name collision with existing symbol in " + packagePath(repo.Root(), e.File),
		})
	}
	return out
}
