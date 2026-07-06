package tool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/kellenff/yactt/internal/registry"
)

// ListProjectsArgs is the typed boundary input for list_projects.
// Empty today; the struct exists so future knobs (e.g. `tag`,
// `since`) can land without breaking the wire shape.
type ListProjectsArgs struct{}

// ListProjectsResult is the structuredContent envelope for
// list_projects. The list lives under `projects` so the envelope
// can grow (e.g. a future `summary` field) without renaming the
// existing key — agents reading `result.projects[*]` keep working.
type ListProjectsResult struct {
	Projects []registry.Entry `json:"projects"`
	Count    int              `json:"count"`
	Registry string           `json:"registry"` // absolute path on disk
}

// ListProjectsSchema is the JSON Schema for list_projects. No
// args; `additionalProperties:false` keeps the contract closed.
var ListProjectsSchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {},
  "additionalProperties": false
}`)

// ListProjectsOutputSchema declares the structuredContent shape
// of list_projects.
var ListProjectsOutputSchema = json.RawMessage(`{
  "type": "object",
  "required": ["projects", "count", "registry"],
  "properties": {
    "projects": { "type": "array", "items": { "type": "object" } },
    "count":    { "type": "integer", "minimum": 0 },
    "registry": { "type": "string" }
  },
  "additionalProperties": false
}`)

// ListProjects returns a Handler that enumerates every entry in
// the registry. The handler does not require a *store.Repo —
// it works in both `yactt mcp serve <path>` and registry-only
// (no path) modes. That decoupling is what makes "list + pick"
// the federation pivot the issue asks for.
func ListProjects(reg *registry.Registry) func(ctx context.Context, args json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		var a ListProjectsArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("invalid list_projects args: %w", err)
		}
		entries, err := reg.List()
		if err != nil {
			return nil, err
		}
		return &ListProjectsResult{
			Projects: entries,
			Count:    len(entries),
			Registry: reg.Path(),
		}, nil
	}
}
