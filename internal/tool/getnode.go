package tool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/id"
	"github.com/kellenff/yactt/internal/store"
)

// GetNodeArgs is the typed boundary input for node_get.
type GetNodeArgs struct {
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
  "required": ["id"],
  "additionalProperties": false
}`)

// GetNode returns a Handler that materializes a node's layers.
func GetNode(repo *store.Repo) func(ctx context.Context, args json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		var a GetNodeArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("invalid node_get args: %w", err)
		}
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
