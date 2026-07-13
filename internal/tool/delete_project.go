package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kellenff/yactt/internal/project"
	"github.com/kellenff/yactt/internal/registry"
)

// DeleteProjectArgs is the typed boundary input for
// delete_project. `project` (file:// URI) is the only knob:
// the tool evicts the registry row and removes the per-repo
// cache directory.
type DeleteProjectArgs struct {
	Project string `json:"project"`
}

// DeleteProjectResult is the structuredContent envelope for
// delete_project. Two booleans distinguish the registry eviction
// from the cache cleanup — either can fail independently, and
// the agent should see which.
type DeleteProjectResult struct {
	Deleted      bool   `json:"deleted"`
	CacheRemoved bool   `json:"cacheRemoved"`
	CachePath    string `json:"cachePath"`
}

// DeleteProjectSchema is the JSON Schema for delete_project.
var DeleteProjectSchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["project"],
  "properties": {
    "project": { "type": "string", "description": "Absolute path as a file:// URI (e.g. file:///abs/path)." }
  },
  "additionalProperties": false
}`)

// DeleteProjectOutputSchema declares the structuredContent shape
// of delete_project.
var DeleteProjectOutputSchema = json.RawMessage(`{
  "type": "object",
  "required": ["deleted", "cacheRemoved", "cachePath"],
  "properties": {
    "deleted":      { "type": "boolean" },
    "cacheRemoved": { "type": "boolean" },
    "cachePath":    { "type": "string" }
  },
  "additionalProperties": false
}`)

// DeleteProject returns a Handler that removes `args.Path` from
// the registry and deletes its per-repo cache directory.
//
// Cache removal is best-effort: the registry row is the source
// of truth, so a stale cache that we can't delete (e.g. another
// process holds it) is reported as `cacheRemoved:false` but
// doesn't fail the call. The agent can decide to retry.
//
// ponytail: we intentionally do not validate that the path is
// inside an allowed roots list. The current MCP server's trust
// model is "tool runs with the agent's permissions"; an
// agent choosing to delete its own project cache isn't a
// privilege-escalation path we need to harden here. Add a
// sandbox check when the registry is shared across users.
func DeleteProject(reg *registry.Registry) func(ctx context.Context, args json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		var a DeleteProjectArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("invalid delete_project args: %w", err)
		}
		ref, err := project.ParseRef(a.Project)
		if err != nil {
			return nil, fmt.Errorf("delete_project: %w", err)
		}
		abs, err := filepath.Abs(ref.Path)
		if err != nil {
			return nil, fmt.Errorf("delete_project: resolve path: %w", err)
		}
		cachePath := registry.CacheDirForRoot(abs)

		deleted, err := reg.Delete(abs)
		if err != nil {
			return nil, fmt.Errorf("delete_project: registry: %w", err)
		}

		// Skip cache cleanup when the cache directory can't
		// be resolved — nothing to remove. Caller still sees
		// `cachePath` so they know what would have been
		// targeted.
		cacheRemoved := false
		if cachePath != "" {
			if rerr := os.RemoveAll(cachePath); rerr != nil {
				return nil, fmt.Errorf("delete_project: cache remove %s: %w", cachePath, rerr)
			}
			cacheRemoved = true
		}
		return &DeleteProjectResult{
			Deleted:      deleted,
			CacheRemoved: cacheRemoved,
			CachePath:    cachePath,
		}, nil
	}
}