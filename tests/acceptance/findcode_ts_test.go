// Package acceptance_test exercises the 10 MCP tools end-to-end against
// the fixture Go project at ../fixtures/sample-go. This file covers the
// tree-sitter pattern_kind branch of find_code — the regex branch has
// its own coverage in acceptance_test.go.
package acceptance_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kellenff/yactt/internal/tool"
)

// TestFindCodeAcceptance_TreeSitter drives the production handler with
// pattern_kind=tree_sitter and asserts ≥1 hit on auth/login.go whose
// snippet mentions `Charge`. This proves the runner reaches the file's
// parsed tree and emits a FindCodeMatch with the expected content.
//
// The fixture is Go-only, so cross-language coverage lives in the
// collaboration tests under internal/tool/findcode_tree_sitter_test.go.
func TestFindCodeAcceptance_TreeSitter(t *testing.T) {
	repo := loadRepo(t)
	out := callJSON(t, tool.FindCode(reg), `{"pattern":"(call_expression) @c","pattern_kind":"tree_sitter","limit":50}`)
	env, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("find_code envelope type: got %T", out)
	}
	hits, ok := env["matches"].([]tool.FindCodeMatch)
	if !ok {
		t.Fatalf("find_code matches slice type: got %T", env["matches"])
	}
	if len(hits) == 0 {
		t.Fatal("expected hits for (call_expression); got 0")
	}

	var loginCharge bool
	for _, h := range hits {
		if !strings.Contains(h.File, "auth/login.go") {
			continue
		}
		if strings.Contains(h.Snippet, "Charge") {
			loginCharge = true
			break
		}
	}
	if !loginCharge {
		t.Fatalf("expected a hit in auth/login.go whose snippet mentions Charge; got %+v", hits)
	}
}

// TestFindCodeAcceptance_TreeSitter_CompileError verifies the
// production handler surfaces a compile failure as a user-readable
// error rather than a panic or empty result.
func TestFindCodeAcceptance_TreeSitter_CompileError(t *testing.T) {
	repo := loadRepo(t)
	_, err := tool.FindCode(reg)(context.Background(), json.RawMessage(`{"pattern":"(function_declaration","pattern_kind":"tree_sitter","limit":10}`))
	if err == nil {
		t.Fatal("expected compile error; got nil")
	}
	if !strings.Contains(err.Error(), "invalid tree-sitter query") {
		t.Errorf("error missing standard prefix: %q", err.Error())
	}
}
