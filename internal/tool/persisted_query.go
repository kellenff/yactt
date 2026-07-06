package tool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/kellenff/yactt/internal/persisted"
)

// PersistedQueryArgs is the typed boundary input for persisted_query.
// `id` is the registered op's identifier; the runner resolves the
// wrapped tool from the registry at dispatch time.
type PersistedQueryArgs struct {
	ID string `json:"id"`
}

// PersistedQuerySchema is the JSON Schema for persisted_query.
// Mirrors the surface every other tool exposes: a single required
// `id` string, no surprises.
var PersistedQuerySchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "id": { "type": "string", "description": "Registered persisted-query identifier, e.g. \"onboarding\"." }
  },
  "required": ["id"],
  "additionalProperties": false
}`)

// PersistedQueryOutputSchema declares the structuredContent shape of
// persisted_query. The wrapped tool's output is surfaced verbatim
// under `results`, so callers see the same wire shape they would
// have seen calling the tool directly. This keeps the wrapper
// thin — agents don't have to learn a new envelope per persisted
// query.
var PersistedQueryOutputSchema = json.RawMessage(`{
  "type": "object",
  "required": ["results"],
  "properties": {
    "results": { "type": "object" }
  },
  "additionalProperties": false
}`)

// PersistedQuery returns a Handler that dispatches a registered
// persisted query by id. Errors are wrapped with op-id context so
// the agent can tell which query failed.
func PersistedQuery(runner *persisted.Runner) func(ctx context.Context, args json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		var a PersistedQueryArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("invalid persisted_query args: %w", err)
		}
		if a.ID == "" {
			return nil, fmt.Errorf("persisted_query: id is required")
		}
		out, err := runner.Run(ctx, a.ID)
		if err != nil {
			return nil, err
		}
		// Wrap the wrapped tool's output in an envelope so
		// structuredContent on the wire is a JSON object (the MCP
		// contract). See internal/mcp/server.go:validateOutputSchema.
		return map[string]any{"results": out}, nil
	}
}
