package persisted_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/kellenff/yactt/internal/persisted"
)

// TestRegistry_RegisterGet: register one op, retrieve by id.
func TestRegistry_RegisterGet(t *testing.T) {
	r := persisted.NewRegistry()
	if err := r.Register(persisted.Op{
		ID:          "onboarding",
		Description: "Repo map.",
		Tool:        "tree_overview",
		Args:        map[string]any{"depth": 2},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, ok := r.Get("onboarding")
	if !ok {
		t.Fatal("Get: ok = false")
	}
	if got.ID != "onboarding" {
		t.Errorf("ID = %q, want onboarding", got.ID)
	}
	if got.Tool != "tree_overview" {
		t.Errorf("Tool = %q, want tree_overview", got.Tool)
	}
	if got.Description != "Repo map." {
		t.Errorf("Description = %q", got.Description)
	}
}

// TestRegistry_DuplicateID: a second Register with the same id
// must fail. Stable op IDs are a contract with agents; silent
// overwrite is unacceptable.
func TestRegistry_DuplicateID(t *testing.T) {
	r := persisted.NewRegistry()
	_ = r.Register(persisted.Op{ID: "foo", Tool: "x"})
	err := r.Register(persisted.Op{ID: "foo", Tool: "y"})
	if err == nil {
		t.Fatal("Register duplicate: err = nil")
	}
	if !errors.Is(err, persisted.ErrDuplicateID) {
		t.Errorf("err = %v, want errors.Is(ErrDuplicateID)", err)
	}
}

// TestRegistry_EmptyID: Register with empty id must fail.
func TestRegistry_EmptyID(t *testing.T) {
	r := persisted.NewRegistry()
	err := r.Register(persisted.Op{ID: "", Tool: "x"})
	if !errors.Is(err, persisted.ErrEmptyID) {
		t.Errorf("err = %v, want errors.Is(ErrEmptyID)", err)
	}
}

// TestRegistry_IDsOrdered: IDs() returns sorted ids (so the
// "unknown id" error message is stable for diffs).
func TestRegistry_IDsOrdered(t *testing.T) {
	r := persisted.NewRegistry()
	_ = r.Register(persisted.Op{ID: "zeta", Tool: "x"})
	_ = r.Register(persisted.Op{ID: "alpha", Tool: "y"})
	_ = r.Register(persisted.Op{ID: "mu", Tool: "z"})
	got := r.IDs()
	want := []string{"alpha", "mu", "zeta"}
	if len(got) != len(want) {
		t.Fatalf("IDs len = %d, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("IDs[%d] = %q, want %q", i, got[i], w)
		}
	}
}

// TestRegistry_GetUnknown: Get of a non-existent id returns ok=false.
func TestRegistry_GetUnknown(t *testing.T) {
	r := persisted.NewRegistry()
	if _, ok := r.Get("nope"); ok {
		t.Error("Get(nope): ok = true")
	}
}

// TestRegistry_ConcurrentSafe: parallel Register/Get must not race.
// Run with -race to surface regressions.
func TestRegistry_ConcurrentSafe(t *testing.T) {
	r := persisted.NewRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := idFor(i)
			_ = r.Register(persisted.Op{ID: id, Tool: "x"})
			_, _ = r.Get(id)
		}(i)
	}
	wg.Wait()
	if len(r.IDs()) != 50 {
		t.Errorf("IDs len = %d, want 50", len(r.IDs()))
	}
}

func idFor(i int) string {
	return "op" + intStr(i)
}

func intStr(n int) string {
	if n == 0 {
		return "0"
	}
	const digits = "0123456789"
	var s []byte
	for n > 0 {
		s = append([]byte{digits[n%10]}, s...)
		n /= 10
	}
	return string(s)
}

// TestRunner_RunHappyPath: Run dispatches the registered op's tool
// handler and returns its result.
func TestRunner_RunHappyPath(t *testing.T) {
	reg := persisted.NewRegistry()
	_ = reg.Register(persisted.Op{
		ID:   "ok",
		Tool: "echo",
		Args: map[string]any{"v": 42},
	})
	calls := map[string]int{}
	tools := map[string]persisted.ToolFunc{
		"echo": func(ctx context.Context, args json.RawMessage) (any, error) {
			calls["echo"]++
			return map[string]any{"ack": true, "args": string(args)}, nil
		},
	}
	runner := persisted.NewRunner(reg, tools)
	out, err := runner.Run(context.Background(), "ok")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("Run result type: got %T", out)
	}
	if m["ack"] != true {
		t.Errorf("Run result: ack = %v, want true", m["ack"])
	}
	if calls["echo"] != 1 {
		t.Errorf("echo tool calls = %d, want 1", calls["echo"])
	}
}

// TestRunner_RunUnknownID: Run with an id that's not registered
// returns an error mentioning valid ids (discoverability).
func TestRunner_RunUnknownID(t *testing.T) {
	reg := persisted.NewRegistry()
	_ = reg.Register(persisted.Op{ID: "alpha", Tool: "x"})
	runner := persisted.NewRunner(reg, map[string]persisted.ToolFunc{
		"x": func(ctx context.Context, args json.RawMessage) (any, error) { return nil, nil },
	})
	_, err := runner.Run(context.Background(), "beta")
	if err == nil {
		t.Fatal("Run unknown: err = nil")
	}
	if got := err.Error(); !contains(got, "beta") || !contains(got, "alpha") {
		t.Errorf("error doesn't list valid ids; got %q", got)
	}
}

// TestRunner_RunUnknownTool: the op references a tool that the
// Runner has no handler for. Surfaces a typed error.
func TestRunner_RunUnknownTool(t *testing.T) {
	reg := persisted.NewRegistry()
	_ = reg.Register(persisted.Op{ID: "x", Tool: "ghost"})
	runner := persisted.NewRunner(reg, map[string]persisted.ToolFunc{}) // empty
	_, err := runner.Run(context.Background(), "x")
	if err == nil {
		t.Fatal("Run with missing tool: err = nil")
	}
	if !contains(err.Error(), "unknown tool") {
		t.Errorf("error doesn't mention unknown tool; got %q", err.Error())
	}
}

// TestRunner_PropagatesError: tool handler error wraps with op-id
// context so an agent can tell which persisted query failed.
func TestRunner_PropagatesError(t *testing.T) {
	reg := persisted.NewRegistry()
	_ = reg.Register(persisted.Op{ID: "x", Tool: "fail"})
	runner := persisted.NewRunner(reg, map[string]persisted.ToolFunc{
		"fail": func(ctx context.Context, args json.RawMessage) (any, error) {
			return nil, errors.New("boom")
		},
	})
	_, err := runner.Run(context.Background(), "x")
	if err == nil {
		t.Fatal("err = nil")
	}
	if !contains(err.Error(), "boom") {
		t.Errorf("error doesn't propagate tool error; got %q", err.Error())
	}
	if !contains(err.Error(), "x") {
		t.Errorf("error doesn't wrap op id; got %q", err.Error())
	}
}

// contains is a tiny helper that avoids dragging strings into the
// test file for two trivial substring checks.
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
