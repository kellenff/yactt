package tool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/id"
	"github.com/kellenff/yactt/internal/project"
	"github.com/kellenff/yactt/internal/registry"
	"github.com/kellenff/yactt/internal/store"
)

// GetNodeArgs is the typed boundary input for node_get.
type GetNodeArgs struct {
	Project       string   `json:"project"`
	ID            string   `json:"id"`
	Layers        []string `json:"layers"`
	Range         []int    `json:"range"`
	IncludeTrivia bool     `json:"include_trivia"`
}

// GetNodeResult is the full domain.Node. Layers are populated per request;
// unrequested layers stay nil and are omitted via omitempty.
type GetNodeResult = domain.Node

// GetNodeSchema is the JSON Schema for node_get. Mirrors design §4.2.
var GetNodeSchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "project": { "type": "string", "description": "Absolute path as a file:// URI (e.g. file:///abs/path). Must be in the registry; call index_repository first." },
    "id": { "type": "string", "description": "Stable node ID, e.g. fn:auth.login.HandleCallback" },
    "layers": {
      "type": "array",
      "items": { "enum": ["summary", "signature", "body", "source", "tokens"] },
      "description": "Layers to materialize. Defaults to ['signature']."
    },
    "range": {
      "type": "array",
      "items": { "type": "integer" },
      "description": "Optional [startLine, endLine] — for source/tokens layers only."
    },
    "include_trivia": { "type": "boolean", "default": false }
  },
  "required": ["project", "id"],
  "additionalProperties": false
}`)

// GetNodeOutputSchema declares the structuredContent shape of node_get.
// The handler returns a *domain.Node (a single object). additionalProperties
// is closed (false) so the host-side schema validator doesn't accept
// arbitrary extra fields — consistent with every other yactt tool
// (issue #33).
var GetNodeOutputSchema = json.RawMessage(`{
  "type": "object",
  "required": ["id", "kind"],
  "properties": {
    "id":      { "type": "string" },
    "kind":    { "type": "string" },
    "name":    { "type": "string" },
    "summary": { "type": "string" }
  },
  "additionalProperties": false
}`)

// GetNode returns a Handler that materializes a node's layers.
func GetNode(reg *registry.Registry) func(ctx context.Context, args json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		var a GetNodeArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("invalid node_get args: %w", err)
		}
		repo, err := project.Resolve(reg, a.Project)
		if err != nil {
			return nil, err
		}
		defer func() { _ = repo.Close() }()
		if a.ID == "" {
			return nil, fmt.Errorf("node_get: id is required")
		}
		nodeID, err := id.Parse(a.ID)
		if err != nil {
			return nil, fmt.Errorf("node_get: %w", err)
		}
		if len(a.Layers) == 0 {
			a.Layers = []string{"signature"}
		}
		layerSet := layerSetFromList(a.Layers)
		n, err := store.MaterializeNode(repo, nodeID, layerSet)
		if err != nil {
			return nil, err
		}
		return n, nil
	}
}

// layerSetFromList converts a list of layer names into a map for fast lookup.
func layerSetFromList(names []string) map[domain.LayerName]bool {
	out := make(map[domain.LayerName]bool, len(names))
	for _, n := range names {
		out[domain.LayerName(n)] = true
	}
	return out
}
