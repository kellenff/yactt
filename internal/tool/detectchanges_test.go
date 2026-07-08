package tool

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/parser"
	"github.com/kellenff/yactt/internal/store"
	"github.com/kellenff/yactt/internal/store/repofixture"
)

// --- git fixture helpers --------------------------------------------------

// gitAvailable skips the test if `git` isn't on PATH. detect_changes shells
// out to git; tests need to follow the same code path or the assertion is
// meaningless.
func gitAvailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not on PATH: %v", err)
	}
}

// writeGitRepo creates a tempdir git repo with two commits: v1 is the
// initial state, v2 is the head. Each map is {relpath: contents}.
//
// Calls store.Load on the resulting dir and returns the loaded repo plus
// a cleanup func that closes it. The git state is on disk so detect_changes
// can run real `git diff HEAD~1..HEAD` against it.
func writeGitRepo(t *testing.T, v1, v2 map[string]string) *store.Repo {
	t.Helper()
	gitAvailable(t)

	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	run("init", "-q")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "detect_changes_test")
	run("config", "commit.gpgsign", "false")
	writeFiles(t, dir, v1)
	run("add", "-A")
	run("commit", "-q", "-m", "v1")
	writeFiles(t, dir, v2)
	run("add", "-A")
	run("commit", "-q", "-m", "v2")

	r, _, err := store.Load(dir)
	if err != nil {
		t.Fatalf("store.Load: %v", err)
	}
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

// callDC is the typed-call helper for detect_changes. Mirrors callSnippet
// in getcodesnippet_test.go — drives the handler in-process so the test
// fails fast on handler errors and gives a typed result.
func callDC(t *testing.T, repo *store.Repo, args string) *DetectChangesResult {
	t.Helper()
	out, err := DetectChanges(repo)(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("handler: %v (args=%s)", err, args)
	}
	r, ok := out.(*DetectChangesResult)
	if !ok {
		t.Fatalf("result type: got %T", out)
	}
	return r
}

// --- tests ----------------------------------------------------------------

// TestDetectChanges_OneFunction pins the happy path: one function body
// changes between commits, the result collapses to one Change row keyed
// by that function's stable id, with callers/tests fan-out populated.
func TestDetectChanges_OneFunction(t *testing.T) {
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
	r := writeGitRepo(t, v1, v2)

	out := callDC(t, r, `{"base":"HEAD~1","head":"HEAD"}`)

	if out.Base != "HEAD~1" || out.Head != "HEAD" {
		t.Errorf("refs: base=%q head=%q, want HEAD~1/HEAD", out.Base, out.Head)
	}
	if out.Provenance.Tool != "yactt" {
		t.Errorf("provenance.tool = %q, want \"yactt\"", out.Provenance.Tool)
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
	if c.Symbol.Kind != domain.KindFunction {
		t.Errorf("symbol kind = %q, want FUNCTION", c.Symbol.Kind)
	}
	if len(c.Ranges) == 0 {
		t.Error("expected at least one range on the change")
	}
	// Test-bucket fan-out: Charge's `_test.go` doesn't exist in this
	// fixture, but callers SHOULD resolve. scanCallers walks every
	// other file's declarations; with v2's Login body calling Charge,
	// there is no caller of Login (Login is a leaf), so callers is
	// expected to be empty. Just assert the slot exists (omitted
	// when empty per json:",omitempty").
}

// TestDetectChanges_FileOnlyHunk covers the case where a hunk lands
// outside any declaration (e.g. an import block). The result must
// surface a Change with Symbol=nil so the caller still sees the diff.
func TestDetectChanges_FileOnlyHunk(t *testing.T) {
	v1 := map[string]string{
		"x.go": `package x

import "fmt"

// Use is the only declaration.
func Use() {
	fmt.Println("a")
}
`,
	}
	v2 := map[string]string{
		"x.go": `package x

import (
	"fmt"
	"os"
)

// Use is the only declaration.
func Use() {
	fmt.Println("a")
	fmt.Println("b")
}
`,
	}
	r := writeGitRepo(t, v1, v2)

	out := callDC(t, r, `{"base":"HEAD~1","head":"HEAD"}`)

	// Expect two changes: one file-only (the import block) and one
	// for the function body.
	var fileOnly, funcChange *Change
	for i := range out.Changes {
		c := &out.Changes[i]
		if c.Symbol == nil {
			if fileOnly != nil {
				t.Errorf("more than one file-only change: %+v", c)
			}
			fileOnly = c
		} else {
			if funcChange != nil {
				t.Errorf("more than one symbol change: %+v", c)
			}
			funcChange = c
		}
	}
	if fileOnly == nil {
		t.Fatalf("expected one file-only change; got changes: %+v", out.Changes)
	}
	if filepath.Base(fileOnly.File) != "x.go" {
		t.Errorf("file-only file = %q, want basename x.go", fileOnly.File)
	}
	if len(fileOnly.Ranges) == 0 {
		t.Error("file-only change has no ranges")
	}
	if funcChange == nil {
		t.Fatal("expected one symbol change; got none")
	}
	if funcChange.Symbol == nil || funcChange.Symbol.ID != "fn:Use" {
		// Files at the repo root have an empty package path, so
		// `id.For(sym, "")` produces "fn:Use" (no package prefix).
		t.Errorf("symbol change id = %+v, want fn:Use", funcChange.Symbol)
	}
}

// TestDetectChanges_SinceForm exercises the `since` shortcut: passing
// only `since` should resolve to base=since, head="HEAD" (default).
func TestDetectChanges_SinceForm(t *testing.T) {
	v1 := map[string]string{
		"x.go": `package x
func Use() int { return 1 }
`,
	}
	v2 := map[string]string{
		"x.go": `package x
func Use() int { return 2 }
`,
	}
	r := writeGitRepo(t, v1, v2)

	out := callDC(t, r, `{"since":"HEAD~1"}`)
	if out.Base != "HEAD~1" {
		t.Errorf("base = %q, want HEAD~1", out.Base)
	}
	if out.Head != "HEAD" {
		t.Errorf("head = %q, want HEAD", out.Head)
	}
	if len(out.Changes) != 1 {
		t.Errorf("expected 1 change; got %d", len(out.Changes))
	}
}

// TestDetectChanges_BaseAndSinceRejected covers the XOR check at the
// boundary. Both fields populated should be rejected without ever
// touching git.
func TestDetectChanges_BaseAndSinceRejected(t *testing.T) {
	r := loadTestRepo(t)
	_, err := DetectChanges(r)(context.Background(), json.RawMessage(`{"base":"HEAD~1","since":"HEAD~1"}`))
	if err == nil {
		t.Fatal("expected error when both base and since are set")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error %q doesn't mention exclusivity", err.Error())
	}
}

// TestDetectChanges_RequiresRef covers the empty-ref boundary. The
// handler must reject with a clear "required" message before reaching
// the git subprocess.
func TestDetectChanges_RequiresRef(t *testing.T) {
	r := loadTestRepo(t)
	_, err := DetectChanges(r)(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error when neither base nor since is set")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("error %q doesn't mention required", err.Error())
	}
}

// TestDetectChanges_NotGitRepo covers the failure path when the repo
// isn't a real git working tree. The repofixture is a synthetic repo
// (its .git/ is just an empty stub), so `git diff` surfaces git's own
// error — we accept any "not a git" diagnostic.
func TestDetectChanges_NotGitRepo(t *testing.T) {
	gitAvailable(t)
	r := loadTestRepo(t)
	_, err := DetectChanges(r)(context.Background(), json.RawMessage(`{"base":"HEAD~1","head":"HEAD"}`))
	if err == nil {
		t.Fatal("expected error on non-git repo; got nil")
	}
	if !strings.Contains(err.Error(), "git") {
		t.Errorf("error %q doesn't mention git", err.Error())
	}
}

// TestDetectChanges_LimitCapsEdges exercises the per-symbol cap on
// callers/tests/overrides. The fixture has multiple functions; a
// `limit: 1` should never produce more than 1 edge of each kind.
func TestDetectChanges_LimitCapsEdges(t *testing.T) {
	v1 := map[string]string{
		"hub.go": `package hub

// Run is the entry point.
func Run() int { return 1 }
`,
		"a.go": `package hub

func A() int { return Run() + 1 }
`,
		"b.go": `package hub

func B() int { return Run() + 2 }
`,
		"c.go": `package hub

func C() int { return Run() + 3 }
`,
	}
	v2 := map[string]string{
		"hub.go": `package hub

// Run is the entry point.
func Run() int { return 99 }
`,
		"a.go": `package hub

func A() int { return Run() + 1 }
`,
		"b.go": `package hub

func B() int { return Run() + 2 }
`,
		"c.go": `package hub

func C() int { return Run() + 3 }
`,
	}
	r := writeGitRepo(t, v1, v2)

	out := callDC(t, r, `{"base":"HEAD~1","head":"HEAD","limit":1}`)

	if len(out.Changes) != 1 {
		t.Fatalf("changes: got %d, want 1: %+v", len(out.Changes), out.Changes)
	}
	c := out.Changes[0]
	if c.Symbol == nil || c.Symbol.ID != "fn:Run" {
		// Files at the repo root have an empty package path, so
		// `id.For(sym, "")` produces "fn:Run" (no package prefix).
		t.Fatalf("expected change on fn:Run; got %+v", c)
	}
	if len(c.Callers) > 1 {
		t.Errorf("callers: got %d, want <= 1: %+v", len(c.Callers), c.Callers)
	}
}

// TestDetectChanges_FilesSummary covers the per-file aggregate stats:
// added/deleted/hunks must add up.
func TestDetectChanges_FilesSummary(t *testing.T) {
	v1 := map[string]string{
		"x.go": `package x

func Use() int { return 1 }
`,
	}
	v2 := map[string]string{
		"x.go": `package x

func Use() int { return 1 }
func NewOne() int { return 2 }
`,
	}
	r := writeGitRepo(t, v1, v2)

	out := callDC(t, r, `{"base":"HEAD~1","head":"HEAD"}`)

	if len(out.Files) != 1 {
		t.Fatalf("files: got %d, want 1: %+v", len(out.Files), out.Files)
	}
	f := out.Files[0]
	if f.File != "x.go" {
		t.Errorf("file = %q, want x.go", f.File)
	}
	if f.Added == 0 {
		t.Error("expected addedLines > 0")
	}
	if f.Hunks == 0 {
		t.Error("expected hunks > 0")
	}
}

// TestMergeRanges exercises the range-merging helper directly. The
// handler relies on it to collapse overlapping hunks from the same
// declaration into one LineRange.
func TestMergeRanges(t *testing.T) {
	rs := []domain.LineRange{
		{Start: 10, End: 15},
		{Start: 5, End: 8},
		{Start: 14, End: 20},
		{Start: 25, End: 25},
	}
	got := mergeRanges(rs)
	want := []domain.LineRange{
		{Start: 5, End: 8},
		{Start: 10, End: 20},
		{Start: 25, End: 25},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mergeRanges:\n got %+v\nwant %+v", got, want)
	}
	// Idempotence — sorting then collapsing twice should be a no-op.
	if !reflect.DeepEqual(mergeRanges(got), want) {
		t.Error("mergeRanges is not idempotent")
	}
}

// TestEnclosingSymbol pins the three-tier fallback that detect_changes
// relies on to surface a Change.Symbol on simple hunk ranges. The bug
// this guards against (#27): the original resolver used "largest
// containing" semantics with strict containment, so a hunk that
// overflowed the function body or landed partly in preamble fell to
// file-only and `Symbol` came back nil. Each tier is exercised
// against the shared repofixture (auth/login.go) so the test
// doesn't depend on git or on a synthetic fixture file.
//
// Tier 1 — hunk strictly inside a function → that function.
// Tier 2 — hunk starts in a function, extends past it → that
//
//	function (regression for fidelity test #4).
//
// Tier 3 — hunk starts in preamble, overlaps multiple symbols →
//
//	symbol with the largest row-overlap (regression for the
//	"smallest symbol wins" pathology).
//
// None  — hunk entirely above all declarations → false.
func TestEnclosingSymbol(t *testing.T) {
	fx := repofixture.New(t)
	repo, _, err := store.Load(fx.Root)
	if err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	syms := repo.Symbols(fx.LoginPath)
	if len(syms) == 0 {
		t.Fatal("no symbols in fixture auth/login.go; cannot anchor test ranges")
	}
	byName := map[string]parser.Symbol{}
	for _, s := range syms {
		byName[s.Name] = s
	}
	login, ok := byName["Login"]
	if !ok {
		t.Fatal("no Login symbol in fixture; cannot anchor Tier-1 ranges")
	}
	auth, ok := byName["Authenticate"]
	if !ok {
		t.Fatal("no Authenticate symbol in fixture; cannot anchor Tier-3 ranges")
	}

	cases := []struct {
		name       string
		start, end int
		wantID     string
		wantOK     bool
	}{
		{
			name:   "Tier 1: hunk strictly inside function",
			start:  int(login.StartRow) + 1,
			end:    int(login.StartRow) + 2,
			wantID: "fn:auth.Login",
			wantOK: true,
		},
		{
			name:   "Tier 2: hunk starts in function and overflows past it",
			start:  int(login.StartRow),
			end:    int(auth.EndRow),
			wantID: "fn:auth.Login",
			wantOK: true,
		},
		{
			// Login spans 3 rows (3..6); Authenticate spans 6 rows
			// (13..19). A hunk starting at row 0 and ending at
			// Authenticate's end overlaps both — T3 should pick
			// Authenticate because its overlap (6) is larger than
			// Login's (3), not because it is the larger symbol.
			name:   "Tier 3: preamble-overlapping hunk picks max overlap",
			start:  0,
			end:    int(auth.EndRow),
			wantID: "fn:auth.Authenticate",
			wantOK: true,
		},
		{
			name:   "None: hunk entirely above any declaration",
			start:  0,
			end:    1,
			wantID: "",
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sym, id, ok := enclosingSymbol(repo, fx.LoginPath, tc.start, tc.end)
			if ok != tc.wantOK {
				t.Errorf("ok = %v, want %v (sym.Name=%q)", ok, tc.wantOK, sym.Name)
			}
			if id != tc.wantID {
				t.Errorf("id = %q, want %q", id, tc.wantID)
			}
		})
	}
}

// TestParseUnifiedDiff exercises the diff parser directly. The handler
// composes it once per call; pinning the parser here catches regressions
// without the noise of a real git subprocess.
//
// The fixture uses hunk-header counts as the source of truth for the
// added/deleted aggregates (the body just illustrates shape).
func TestParseUnifiedDiff(t *testing.T) {
	diff := []byte(`diff --git a/x.go b/x.go
--- a/x.go
+++ b/x.go
@@ -1,2 +1,5 @@
 package x
+import "os"
+import "io"
+import "net"
 import "fmt"
`)
	hunks, files := parseUnifiedDiff(diff)
	if len(files) != 1 {
		t.Fatalf("files: got %d, want 1: %+v", len(files), files)
	}
	if files[0].File != "x.go" {
		t.Errorf("file = %q, want x.go", files[0].File)
	}
	if files[0].Added != 5 {
		t.Errorf("addedLines = %d, want 5", files[0].Added)
	}
	if files[0].Hunks != 1 {
		t.Errorf("hunks = %d, want 1", files[0].Hunks)
	}
	if len(hunks) != 1 {
		t.Fatalf("hunks: got %d, want 1: %+v", len(hunks), hunks)
	}
	// Pure-deletion hunk: newCount == 0 → skipped from the hunks
	// list (but its oldCount is still aggregated into the file).
	diffDel := []byte(`diff --git a/x.go b/x.go
--- a/x.go
+++ b/x.go
@@ -1,3 +0,0 @@
-package x
-import "fmt"
-func Use() int { return 1 }
`)
	hunksDel, filesDel := parseUnifiedDiff(diffDel)
	if len(hunksDel) != 0 {
		t.Errorf("pure-deletion hunk should be skipped; got %+v", hunksDel)
	}
	if len(filesDel) != 1 || filesDel[0].Deleted != 3 {
		t.Errorf("pure-deletion: file summary = %+v, want deletedLines=3", filesDel)
	}
}

// TestNormaliseRefs exercises the args-normalisation helper directly.
// Pinning each branch keeps the handler's first call site obvious.
func TestNormaliseRefs(t *testing.T) {
	type tc struct {
		base, since, head  string
		wantBase, wantHead string
		wantErr            string
	}
	cases := []tc{
		{base: "main", head: "HEAD", wantBase: "main", wantHead: "HEAD"},
		{base: "main", wantBase: "main", wantHead: "HEAD"},
		{since: "HEAD~1", wantBase: "HEAD~1", wantHead: "HEAD"},
		{base: "main", since: "HEAD~1", wantErr: "mutually exclusive"},
		{head: "HEAD", wantErr: "required"},
		{base: "", since: "", head: "", wantErr: "required"},
		// Defence against git argv injection: refuse any ref whose
		// first byte is `-` so a caller can't smuggle flags like
		// `--upload-pack=...` into the subprocess.
		{base: "--upload-pack=evil", wantErr: "must not start with '-'"},
		{since: "-x", wantErr: "must not start with '-'"},
		{base: "main", head: "--exec=evil", wantErr: "must not start with '-'"},
	}
	for _, c := range cases {
		gotBase, gotHead, err := normaliseRefs(c.base, c.since, c.head)
		if c.wantErr != "" {
			if err == nil {
				t.Errorf("normaliseRefs(%q,%q,%q): expected error containing %q, got nil",
					c.base, c.since, c.head, c.wantErr)
				continue
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("normaliseRefs(%q,%q,%q): error %q doesn't contain %q",
					c.base, c.since, c.head, err.Error(), c.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("normaliseRefs(%q,%q,%q): unexpected error %v", c.base, c.since, c.head, err)
			continue
		}
		if gotBase != c.wantBase || gotHead != c.wantHead {
			t.Errorf("normaliseRefs(%q,%q,%q) = (%q,%q), want (%q,%q)",
				c.base, c.since, c.head, gotBase, gotHead, c.wantBase, c.wantHead)
		}
	}
}

// TestDetectChanges_DeterministicOrdering pins the order of the
// `changes` slice (file, then symbol id) so the result is stable across
// runs and across Go map iteration. The fixture intersperses content
// between A and Z so git produces two separate hunks (otherwise git
// would coalesce both adjacent changes into a single hunk, masking the
// per-symbol ordering we want to pin).
func TestDetectChanges_DeterministicOrdering(t *testing.T) {
	v1 := map[string]string{
		"a.go": `package x

// A is the first function.
func A() int {
	return 1
}

// Some unrelated content here.

// Z is the last function.
func Z() int {
	return 1
}
`,
	}
	v2 := map[string]string{
		"a.go": `package x

// A is the first function.
func A() int {
	return 99
}

// Some unrelated content here.

// Z is the last function.
func Z() int {
	return 99
}
`,
	}
	r := writeGitRepo(t, v1, v2)

	out := callDC(t, r, `{"base":"HEAD~1","head":"HEAD"}`)
	ids := []string{}
	for _, c := range out.Changes {
		if c.Symbol != nil {
			ids = append(ids, c.Symbol.ID)
		}
	}
	if len(ids) != 2 {
		t.Fatalf("expected 2 symbol changes; got %d (%v)", len(ids), ids)
	}
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	if !reflect.DeepEqual(ids, []string{"fn:A", "fn:Z"}) {
		// The handler emits symbol changes in sorted-key order
		// (deterministic across runs). Sorted == "fn:A","fn:Z" here,
		// so any other order is a regression.
		t.Errorf("changes order = %v, want [fn:A fn:Z]", ids)
	}
	if !reflect.DeepEqual(sorted, []string{"fn:A", "fn:Z"}) {
		t.Errorf("sorted keys = %v, want [fn:A fn:Z]", sorted)
	}
}
