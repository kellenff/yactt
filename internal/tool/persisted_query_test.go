package tool_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kellenff/yactt/internal/persisted"
	"github.com/kellenff/yactt/internal/tool"
)

// buildRunner constructs a minimal persisted query runner for tests.
// `toolOut` is what the wrapped tool returns when invoked.
func buildRunner(t *testing.T, toolOut any) *persisted.Runner {
	t.Helper()
	reg := persisted.NewRegistry()
	_ = reg.Register(persisted.Op{
		ID:          "onboarding",
		Description: "Repo map.",
		Tool:        "tree_overview",
		Args:        map[string]any{"depth": 2},
	})
	tools := map[string]persisted.ToolFunc{
		"tree_overview": func(ctx context.Context, args json.RawMessage) (any, error) {
			return toolOut, nil
		},
	}
	return persisted.NewRunner(reg, tools)
}

// TestPersistedQuery_HappyPath: the registered op dispatches and
// the wrapped tool's result is surfaced under the `results` envelope.
func TestPersistedQuery_HappyPath(t *testing.T) {
	toolOut := map[string]any{
		"id":   "repo:/x",
		"kind": "REPO",
	}
	runner := buildRunner(t, toolOut)
	handler := tool.PersistedQuery(runner)
	out, err := handler(context.Background(), json.RawMessage(`{"id":"onboarding"}`))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	env, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("envelope type: got %T", out)
	}
	results, ok := env["results"].(map[string]any)
	if !ok {
		t.Fatalf("results type: got %T", env["results"])
	}
	if results["id"] != "repo:/x" {
		t.Errorf("results.id = %v, want repo:/x", results["id"])
	}
	if results["kind"] != "REPO" {
		t.Errorf("results.kind = %v, want REPO", results["kind"])
	}
}

// TestPersistedQuery_EmptyID: empty id returns an error at the boundary.
func TestPersistedQuery_EmptyID(t *testing.T) {
	runner := buildRunner(t, map[string]any{})
	handler := tool.PersistedQuery(runner)
	_, err := handler(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("empty id: err = nil")
	}
	if !strings.Contains(err.Error(), "id is required") {
		t.Errorf("error doesn't say id is required; got %q", err.Error())
	}
}

// TestPersistedQuery_UnknownID: unknown id returns an error that
// lists valid ids — discoverability without a separate list tool.
func TestPersistedQuery_UnknownID(t *testing.T) {
	runner := buildRunner(t, map[string]any{})
	handler := tool.PersistedQuery(runner)
	_, err := handler(context.Background(), json.RawMessage(`{"id":"nope"}`))
	if err == nil {
		t.Fatal("unknown id: err = nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "unknown id") {
		t.Errorf("error doesn't say 'unknown id'; got %q", msg)
	}
	if !strings.Contains(msg, "onboarding") {
		t.Errorf("error doesn't list valid ids; got %q", msg)
	}
}

// TestPersistedQuery_BadArgs: invalid JSON returns an error at the
// boundary (parse-don't-validate).
func TestPersistedQuery_BadArgs(t *testing.T) {
	runner := buildRunner(t, map[string]any{})
	handler := tool.PersistedQuery(runner)
	_, err := handler(context.Background(), json.RawMessage(`not json`))
	if err == nil {
		t.Fatal("bad args: err = nil")
	}
	if !strings.Contains(err.Error(), "invalid persisted_query args") {
		t.Errorf("error doesn't say invalid args; got %q", err.Error())
	}
}

// TestPersistedQuery_SchemasAreObjects: both schemas must declare
// top-level type:"object" so the MCP host-side validator accepts
// the structuredContent envelope.
func TestPersistedQuery_SchemasAreObjects(t *testing.T) {
	for _, raw := range []json.RawMessage{tool.PersistedQuerySchema, tool.PersistedQueryOutputSchema} {
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if probe.Type != "object" {
			t.Errorf("schema type = %q, want object", probe.Type)
		}
	}
}
