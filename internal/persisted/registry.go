// Package persisted holds the runtime registry of pre-built MCP tool
// invocations referenced by ID. Teams ship curated workflows (PR
// review, onboarding, security audit) that agents reference by name
// instead of reconstructing the call shape from scratch on every turn.
//
// MVP scope (per the Phase 1.5 plan): one tool call per op. Each Op
// records the tool name and a static args map; the runner dispatches
// the wrapped call and returns its result. Step chaining and
// parameter forwarding are deliberate follow-ups — they need a real
// curated-workflow file format and per-step template syntax, which
// are out of scope for this slice.
package persisted

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// Op is one persisted query. ID is the agent-facing reference;
// Tool + Args are what the runner dispatches.
type Op struct {
	ID          string
	Description string
	Tool        string         // MCP tool name
	Args        map[string]any // static args forwarded to the tool handler
}

// ErrEmptyID is returned when Register or Get receives an empty id.
var ErrEmptyID = errors.New("persisted: empty id")

// ErrDuplicateID is returned when Register is called twice with the
// same id. The registry treats op IDs as stable contracts — agents
// rely on them — so a silent overwrite is unacceptable.
var ErrDuplicateID = errors.New("persisted: duplicate id")

// Registry is a thread-safe collection of Op. Lookups are O(1); the
// registry is small (a handful of ops at most) so no LRU is needed.
type Registry struct {
	mu  sync.RWMutex
	ops map[string]Op
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{ops: make(map[string]Op)}
}

// Register adds an op. Errors on empty id or duplicate id; the
// caller is expected to fix the offending registration at startup
// rather than retry, so we return the error to the load path.
func (r *Registry) Register(op Op) error {
	if op.ID == "" {
		return ErrEmptyID
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.ops[op.ID]; exists {
		return fmt.Errorf("%w: %q", ErrDuplicateID, op.ID)
	}
	r.ops[op.ID] = op
	return nil
}

// MustRegister is the panic-on-error variant for startup wiring. Use
// in cmd/yactt/main.go where a duplicate id is a programmer error.
func (r *Registry) MustRegister(op Op) {
	if err := r.Register(op); err != nil {
		panic(err)
	}
}

// Get returns the op with the given id, or ok=false if not found.
func (r *Registry) Get(id string) (Op, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	op, ok := r.ops[id]
	return op, ok
}

// IDs returns the registered ids in lexicographic order. Used by the
// persisted_query tool to surface valid ids in the "unknown id"
// error message — agents can see what's available without a separate
// list tool.
func (r *Registry) IDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.ops))
	for id := range r.ops {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// List returns a copy of every registered op. Order is lexicographic
// by id; the slice is fresh so callers can mutate it freely.
func (r *Registry) List() []Op {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Op, 0, len(r.ops))
	for _, op := range r.ops {
		out = append(out, op)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ToolFunc is the handler signature the runner dispatches against.
// Same shape as mcp.ToolDef.Handler — kept independent so the
// registry has no compile-time dependency on internal/mcp.
type ToolFunc func(ctx context.Context, args json.RawMessage) (any, error)

// Runner holds a registry plus the tool-handler map it dispatches
// against. Constructed once at startup; safe for concurrent use.
type Runner struct {
	reg   *Registry
	tools map[string]ToolFunc
}

// NewRunner returns a Runner wired to reg and tools. The tools map
// should contain every tool the registry's ops might reference;
// missing-tool errors are surfaced at dispatch time.
func NewRunner(reg *Registry, tools map[string]ToolFunc) *Runner {
	return &Runner{reg: reg, tools: tools}
}

// Run looks up opID, resolves the wrapped tool's handler, and
// dispatches the call. The `project` URI is injected into the
// wrapped tool's args under the "project" key (overriding any value
// the op's static args supply) so every code-intel tool that needs
// a project gets one transparently. Returns the tool's result
// unchanged so persisted_query callers see the same wire shape as
// direct calls. Errors are wrapped with the op id so an agent can
// tell which persisted query failed.
//
// `rawArgs` is the full persisted_query caller's JSON, kept around
// so the runner can layer the caller's args on top of the op's
// static args (caller wins for any key other than "project").
func (r *Runner) Run(ctx context.Context, opID, project string, rawArgs json.RawMessage) (any, error) {
	op, ok := r.reg.Get(opID)
	if !ok {
		return nil, fmt.Errorf("persisted_query: unknown id %q; valid ids: %v", opID, r.reg.IDs())
	}
	fn, ok := r.tools[op.Tool]
	if !ok {
		return nil, fmt.Errorf("persisted_query: op %q references unknown tool %q", opID, op.Tool)
	}
	argsJSON, err := r.mergedArgs(op, project, rawArgs)
	if err != nil {
		return nil, fmt.Errorf("persisted_query: op %q: marshal args: %w", opID, err)
	}
	out, err := fn(ctx, argsJSON)
	if err != nil {
		return nil, fmt.Errorf("persisted_query: op %q (%s) failed: %w", opID, op.Tool, err)
	}
	return out, nil
}

// mergedArgs layers the persisted_query caller's args on top of
// the op's static args (caller wins for any key other than
// "project") and injects `project` (file:// URI) under the
// "project" key — the wrapped tool's required field. The caller
// can't override project because that would let an agent
// bypass the URI validation.
func (r *Runner) mergedArgs(op Op, project string, rawArgs json.RawMessage) ([]byte, error) {
	var callerArgs map[string]any
	if len(rawArgs) > 0 {
		if err := json.Unmarshal(rawArgs, &callerArgs); err != nil {
			return nil, fmt.Errorf("persisted_query: invalid args: %w", err)
		}
	}
	out := make(map[string]any, len(op.Args)+len(callerArgs)+1)
	for k, v := range op.Args {
		out[k] = v
	}
	for k, v := range callerArgs {
		// Don't let the caller override `project` — that would
		// let an agent bypass the URI check.
		if k == "project" {
			continue
		}
		out[k] = v
	}
	out["project"] = project
	return json.Marshal(out)
}

// ArgsJSONFor returns the args map from op marshalled to JSON. The
// runner consumes bytes via ToolFunc; this helper is the canonical
// serialiser. Exposed so callers can dry-run or pre-compute.
func ArgsJSONFor(op Op) ([]byte, error) {
	if len(op.Args) == 0 {
		return []byte("{}"), nil
	}
	return json.Marshal(op.Args)
}
