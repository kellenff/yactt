package tool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/kellenff/yactt/internal/persisted"
)

// PersistedQueryArgs is the typed boundary input for persisted_query.
// `op` is the registered op's identifier; `project` is the file:// URI
// of the project the op should run against. The runner resolves the
// wrapped tool from the registry at dispatch time and injects
// `project` into the wrapped tool's args.
type PersistedQueryArgs struct {
	Op      string `json:"op"`
	Project string `json:"project"`
}

// PersistedQuerySchema is the JSON Schema for persisted_query.
// `op` is the registered op's identifier; `project` is the file://
// URI of the project the op should run against. Both are required.
var PersistedQuerySchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "op":      { "type": "string", "description": "Registered persisted-query identifier, e.g. \"onboarding\"." },
    "project": { "type": "string", "description": "Absolute path as a file:// URI. Injected into the wrapped tool's args before dispatch." }
  },
  "required": ["op", "project"],
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
		if a.Op == "" {
			return nil, fmt.Errorf("persisted_query: op is required")
		}
		if a.Project == "" {
			return nil, fmt.Errorf("persisted_query: project is required")
		}
		out, err := runner.Run(ctx, a.Op, a.Project, args)
		if err != nil {
			return nil, err
		}
		// Wrap the wrapped tool's output in an envelope so
		// structuredContent on the wire is a JSON object (the MCP
		// contract). See internal/mcp/server.go:validateOutputSchema.
		return map[string]any{"results": out}, nil
	}
}
