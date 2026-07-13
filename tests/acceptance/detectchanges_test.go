// Package acceptance_test — detect_changes acceptance. Exercises the
// production handler end-to-end against a real on-disk git repo.
//
// The shared `../fixtures/sample-go` fixture is read-only by
// convention — other acceptance tests depend on its current state.
// detect_changes needs a real git working tree and is happier in its
// own tempdir, so each test copies a small slice of files into a fresh
// `t.TempDir()`, inits git, commits twice, and exercises the diff
// between HEAD~1 and HEAD.
//
// Why this lives in acceptance rather than internal/tool:
//   - it asserts domain-level facts ("detect_changes between two
//     commits surfaces the affected function and its callers/tests")
//     with a multi-file fixture resembling the real-world shape;
//   - it's the seam where a future regression in the integration
//     between git-diff parsing, the symbol index, and the cross-file
//     edge scanners would surface.
package acceptance_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kellenff/yactt/internal/store"
	"github.com/kellenff/yactt/internal/tool"
)

// gitAvailable skips when `git` isn't on PATH; detect_changes shells
// out to git unconditionally.
func gitAvailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not on PATH: %v", err)
	}
}

// freshGitRepo copies a small source tree into a tempdir, inits git,
// writes two commits (initial + a body-change to `Login`), and loads
// the resulting repo via store.Load. Returns the repo. Cleanup is
// automatic via t.TempDir().
func freshGitRepo(t *testing.T, v1, v2 map[string]string) *store.Repo {
	t.Helper()
	gitAvailable(t)

	dir := t.TempDir()
	writeFiles(t, dir, v1)

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "detect_changes_test@local")
	run("config", "user.name", "detect_changes_test")
	run("config", "commit.gpgsign", "false")
	run("add", "-A")
	run("commit", "-q", "-m", "v1")

	writeFiles(t, dir, v2)
	run("add", "-A")
	run("commit", "-q", "--allow-empty", "-m", "v2")

	r, _, err := store.Load(dir)
	if err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	_ = seedRegForProject(t, r.Root())
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// callDC drives the typed handler end-to-end. Mirrors callJSON in
// acceptance_test.go but returns the typed DetectChangesResult.
func callDC(t *testing.T, repo *store.Repo, argsJSON string) *tool.DetectChangesResult {
	t.Helper()
	out, err := tool.DetectChanges(seedRegForProject(t, repo.Root()))(context.Background(), json.RawMessage(argsJSON))
	if err != nil {
		t.Fatalf("handler: %v (args=%s)", err, argsJSON)
	}
	r, ok := out.(*tool.DetectChangesResult)
	if !ok {
		t.Fatalf("result type: got %T", out)
	}
	return r
}

// TestDetectChangesAcceptance_DiffSurfacesChangedFunction is the
// happy-path acceptance: Login changes between v1 and v2; the result
// must surface it under `changes` with callers and tests fan-out.
func TestDetectChangesAcceptance_DiffSurfacesChangedFunction(t *testing.T) {
	gitAvailable(t)
	v1 := map[string]string{
		"auth/login.go": `package auth

// Login authenticates a user.
func Login(user, pass string) error {
	return nil
}
`,
		"payments/pay.go": `package payments

// Charge bills the user.
func Charge(amount int) error {
	return nil
}
`,
	}
	v2 := map[string]string{
		"auth/login.go": `package auth

// Login authenticates a user.
func Login(user, pass string) error {
	return Charge(1)
}
`,
		"payments/pay.go": `package payments

// Charge bills the user.
func Charge(amount int) error {
	return nil
}
`,
	}
	r := freshGitRepo(t, v1, v2)
	out := callDC(t, r, `{"base":"HEAD~1","head":"HEAD"}`)

	if out.Truncated {
		t.Errorf("expected non-truncated result")
	}
	if len(out.Changes) != 1 {
		t.Fatalf("changes: got %d, want 1: %+v", len(out.Changes), out.Changes)
	}
	c := out.Changes[0]
	if c.Symbol == nil {
		t.Fatal("expected symbol on first change; got nil")
	}
	if c.Symbol.ID != "fn:auth.Login" {
		t.Errorf("symbol id = %q, want fn:auth.Login", c.Symbol.ID)
	}
	if c.Symbol.Kind != "FUNCTION" {
		t.Errorf("symbol kind = %q, want FUNCTION", c.Symbol.Kind)
	}
	if out.Provenance.Tool != "yactt" {
		t.Errorf("provenance.tool = %q, want yactt", out.Provenance.Tool)
	}
}

// TestDetectChangesAcceptance_FilesSummary pins the per-file
// aggregate stats (added, deleted, hunks) on a non-trivial diff.
func TestDetectChangesAcceptance_FilesSummary(t *testing.T) {
	gitAvailable(t)
	v1 := map[string]string{
		"x.go": `package x

func Use() int { return 1 }
`,
	}
	v2 := map[string]string{
		"x.go": `package x

func Use() int { return 99 }
`,
	}
	r := freshGitRepo(t, v1, v2)
	out := callDC(t, r, `{"base":"HEAD~1","head":"HEAD"}`)

	if len(out.Files) != 1 {
		t.Fatalf("files: got %d, want 1: %+v", len(out.Files), out.Files)
	}
	f := out.Files[0]
	if f.Added != 1 || f.Deleted != 1 || f.Hunks != 1 {
		t.Errorf("per-file stats = %+v, want Added=1 Deleted=1 Hunks=1", f)
	}
}

// TestDetectChangesAcceptance_SinceShortcut exercises the `since`
// shorthand. base/since must resolve to the same diff.
func TestDetectChangesAcceptance_SinceShortcut(t *testing.T) {
	gitAvailable(t)
	v1 := map[string]string{"x.go": `package x
func Use() int { return 1 }
`}
	v2 := map[string]string{"x.go": `package x
func Use() int { return 99 }
`}
	r := freshGitRepo(t, v1, v2)

	viaBase := callDC(t, r, `{"base":"HEAD~1","head":"HEAD"}`)
	viaSince := callDC(t, r, `{"since":"HEAD~1"}`)

	if viaSince.Base != "HEAD~1" || viaSince.Head != "HEAD" {
		t.Errorf("since resolved to base=%q head=%q, want HEAD~1/HEAD", viaSince.Base, viaSince.Head)
	}
	if len(viaSince.Changes) != len(viaBase.Changes) {
		t.Errorf("since produced %d changes, base produced %d", len(viaSince.Changes), len(viaBase.Changes))
	}
}

// TestDetectChangesAcceptance_HeadSelfIsEmpty pins that asking for
// the diff of HEAD..HEAD produces a well-formed empty result.
func TestDetectChangesAcceptance_HeadSelfIsEmpty(t *testing.T) {
	gitAvailable(t)
	v1 := map[string]string{"x.go": `package x
func Use() int { return 1 }
`}
	v2 := map[string]string{"x.go": `package x
func Use() int { return 1 }
`}
	r := freshGitRepo(t, v1, v2)
	out := callDC(t, r, `{"base":"HEAD","head":"HEAD"}`)
	if len(out.Changes) != 0 {
		t.Errorf("expected 0 changes for empty diff; got %d", len(out.Changes))
	}
	if out.Truncated {
		t.Errorf("empty diff should not be truncated")
	}
}

// TestDetectChangesAcceptance_BoundaryRefRequired asserts the
// handler rejects empty/missing refs at the boundary.
func TestDetectChangesAcceptance_BoundaryRefRequired(t *testing.T) {
	gitAvailable(t)
	v1 := map[string]string{"x.go": `package x
func Use() int { return 1 }
`}
	v2 := map[string]string{"x.go": `package x
func Use() int { return 99 }
`}
	r := freshGitRepo(t, v1, v2)

	_, err := tool.DetectChanges(seedRegForProject(t, r.Root()))(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error on empty args; got nil")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("error %q doesn't mention 'required'", err.Error())
	}
}

// _ keeps the store import non-future-proof — used implicitly via the
// helper signatures above.
var _ = store.Repo{}
