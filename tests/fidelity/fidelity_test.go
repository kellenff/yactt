// Package fidelity_test exercises the canonical tool progression a real
// harness would take for representative code-intelligence tasks.
//
// Each TestFidelity_TaskN drives the steps the agent is expected to follow
// and asserts the **final** result matches the domain expectation, plus
// asserts each intermediate step returned a non-error result of the right
// shape. This pins both end-to-end correctness and per-step regression —
// a broken Tier-0 lookup or a renamed wire field surfaces as a clear
// "step K returned X, expected Y" failure rather than a confusing late-
// stage assertion miss.
//
// Task prompts and canonical flows are documented in TASKS.md.
package fidelity_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/entity"
	"github.com/kellenff/yactt/internal/id"
	"github.com/kellenff/yactt/internal/parser"
	"github.com/kellenff/yactt/internal/registry"
	"github.com/kellenff/yactt/internal/store"
	"github.com/kellenff/yactt/internal/tool"
)

// seedRegForFidelity creates a fresh *registry.Registry with one
// entry for the given root. Used by every tool factory call in
// this file; the handler resolves the project URI via project.Resolve.
func seedRegForFidelity(t *testing.T, root string) *registry.Registry {
	t.Helper()
	dir := t.TempDir()
	reg := registry.New(filepath.Join(dir, "projects.json"))
	if err := reg.Upsert(registry.Entry{
		Name:      filepath.Base(root),
		Path:      root,
		IndexedAt: time.Now().UTC(),
		Files:     0,
	}); err != nil {
		t.Fatalf("seedRegForFidelity: %v", err)
	}
	return reg
}

// drive invokes a handler and fails the test on error. Returns the
// decoded JSON shape (everything goes through map[string]any to make
// per-step assertions ergonomic).
// driveWithProject prepends a `project` (file:// URI) field to the
// args JSON if the field is missing. The fixture root is read from
// the test's repo variable via seedRegForFidelity; the helper just
// constructs the URI from the test's repo reference.
//
// Used by drive and driveRaw: every per-tool args JSON in this file
// stays compact (no "project" field needed).
func driveWithProject(t *testing.T, repoRoot, argsJSON string) string {
	if strings.Contains(argsJSON, `"project"`) {
		return argsJSON
	}
	if argsJSON == "{}" {
		return `{"project":"file://` + repoRoot + `"}`
	}
	return `{"project":"file://` + repoRoot + `",` + argsJSON[1:]
}

// drive invokes a handler with `repoRoot`'s project URI injected
// into the args JSON and fails the test on error. Returns the
// decoded JSON shape (everything goes through map[string]any to
// make per-step assertions ergonomic). The repoRoot parameter is
// required because every code-intel tool now resolves its project
// via a file:// URI; passing it through here keeps the per-tool
// args JSON in the test bodies compact.
func drive(t *testing.T, repoRoot string, h func(ctx context.Context, args json.RawMessage) (any, error), argsJSON string) map[string]any {
	t.Helper()
	argsJSON = driveWithProject(t, repoRoot, argsJSON)
	out, err := h(context.Background(), json.RawMessage(argsJSON))
	if err != nil {
		t.Fatalf("handler error: %v (args=%s)", err, argsJSON)
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var asMap map[string]any
	if err := json.Unmarshal(b, &asMap); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	return asMap
}

// driveRaw returns the raw `any` so tests can cast to typed results
// where the envelope shape matters. Used by tests that need access to
// typed fields like TreeOverviewResult nodes. repoRoot has the same
// thread-the-project semantics as drive.
func driveRaw(t *testing.T, repoRoot string, h func(ctx context.Context, args json.RawMessage) (any, error), argsJSON string) any {
	t.Helper()
	argsJSON = driveWithProject(t, repoRoot, argsJSON)
	out, err := h(context.Background(), json.RawMessage(argsJSON))
	if err != nil {
		t.Fatalf("handler error: %v (args=%s)", err, argsJSON)
	}
	return out
}

// loadFixtureRepo loads the shared sample-go fixture once per test.
// ponytail: no caching across tests — fixture load is fast enough that
// the simpler code wins.
func loadFixtureRepo(t *testing.T) *store.Repo {
	t.Helper()
	if _, err := os.Stat("../fixtures/sample-go"); err != nil {
		t.Skipf("sample-go fixture unavailable: %v", err)
	}
	repo, errs, err := store.Load("../fixtures/sample-go")
	if err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	if len(errs) > 0 {
		t.Logf("load reported %d file errors: %v", len(errs), errs)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo
}

// TestFidelity_Task1_CodeNavigation_LocateAndCaller verifies the canonical
// flow "find Login and one of its callers".
//
//	Prompt: Where is Login defined, and where is it used? Show me one caller.
func TestFidelity_Task1_CodeNavigation_LocateAndCaller(t *testing.T) {
	repo := loadFixtureRepo(t)
	reg := seedRegForFidelity(t, repo.Root())

	// Step 1: tree_overview — orient. The handler returns the root
	// TreeOverviewResult directly (not wrapped in `{tree: …}`), so the
	// shape we expect is {id, kind, summary, children}.
	overview := drive(t, repo.Root(), tool.TreeOverview(reg), `{"depth":1}`)
	if _, ok := overview["id"]; !ok {
		t.Fatalf("step 1 tree_overview: missing 'id' field; got %v", overview)
	}
	children, ok := overview["children"].([]any)
	if !ok || len(children) == 0 {
		t.Fatalf("step 1 tree_overview: expected non-empty children; got %v", overview)
	}

	// Step 2: find_symbol("auth/Login") — locate. Both the slash and
	// the dotted form resolve to the same node (issue #28 fixed the
	// dotted miss).
	symStep := drive(t, repo.Root(), tool.FindSymbol(reg), `{"name_path":"auth/Login"}`)
	syms, ok := symStep["symbols"].([]any)
	if !ok || len(syms) == 0 {
		t.Fatalf("step 2 find_symbol: expected non-empty symbols; got %v", symStep)
	}

	// Step 2b: dotted form must agree (issue #28 regression pin).
	symDotted := drive(t, repo.Root(), tool.FindSymbol(reg), `{"name_path":"auth.Login"}`)
	dottedSyms, ok := symDotted["symbols"].([]any)
	if !ok || len(dottedSyms) == 0 {
		t.Fatalf("step 2b find_symbol: dotted form returned no symbols; got %v", symDotted)
	}

	// Step 3: find_referencing_symbols("fn:auth.Login") — one caller.
	// ponytail: this tool's arg is a node ID, not a name_path, so the
	// `fn:` prefix is required.
	refStep := drive(t, repo.Root(), tool.FindReferencingSymbols(reg), `{"symbol":"fn:auth.Login"}`)
	refs, ok := refStep["references"].([]any)
	if !ok {
		t.Fatalf("step 3 find_referencing_symbols: missing 'references' field; got %v", refStep)
	}
	// auth/login_test.go calls Login — at least one caller exists.
	if len(refs) == 0 {
		t.Fatalf("step 3 find_referencing_symbols: expected ≥1 caller for fn:auth.Login; got 0")
	}
}

// TestFidelity_Task2_CodeNavigation_CallChain verifies the canonical flow
// "what does Login call, and can we navigate one level deeper".
//
//	Prompt: What does Login call? Show me the call chain.
//
// ponytail: the sample-go fixture's first-hop callees (ValidateToken,
// SetSession, payments.Charge) don't call anything themselves, so the
// assertion stops at "we can call node_edges on every first-hop callee
// without error" — proving the chain is navigable rather than asserting
// a non-existent second hop. The fixture is the limit, not the chain.
func TestFidelity_Task2_CodeNavigation_CallChain(t *testing.T) {
	repo := loadFixtureRepo(t)
	reg := seedRegForFidelity(t, repo.Root())

	// Step 1: find_symbol — locate Login.
	symStep := drive(t, repo.Root(), tool.FindSymbol(reg), `{"name_path":"auth/Login"}`)
	if syms, _ := symStep["symbols"].([]any); len(syms) == 0 {
		t.Fatalf("step 1: expected to locate Login; got %v", symStep)
	}

	// Step 2: node_edges(callees) — first hop.
	edges1 := drive(t, repo.Root(), tool.NodeEdges(reg), `{"id":"fn:auth.Login","kinds":["callees"]}`)
	e1, _ := edges1["edges"].([]any)
	if len(e1) == 0 {
		t.Fatalf("step 2: expected Login to have callees; got 0")
	}

	// Step 3: for each first-hop callee, drive node_edges(callees)
	// again. Assertion: every first-hop callee resolves (no error).
	// The fixture's leaves don't call further, so the second-hop list
	// is empty — but the call must not fail. A failure here means the
	// symbol ID format broke (e.g. id.For output changed shape).
	seen := map[string]bool{}
	navigated := 0
	for _, raw := range e1 {
		edge, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id, _ := edge["targetId"].(string)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		edges2 := drive(t, repo.Root(), tool.NodeEdges(reg), `{"id":"`+id+`","kinds":["callees"]}`)
		if _, ok := edges2["edges"].([]any); ok {
			navigated++
		}
	}
	if navigated == 0 {
		t.Fatalf("step 3: expected to navigate at least one first-hop callee; navigated 0")
	}
}

// TestFidelity_Task3_RepoOrientation_TopLevelStructure verifies the
// canonical flow "give me the top-level structure".
//
//	Prompt: What's the top-level structure of this repo?
func TestFidelity_Task3_RepoOrientation_TopLevelStructure(t *testing.T) {
	repo := loadFixtureRepo(t)
	reg := seedRegForFidelity(t, repo.Root())

	out := driveRaw(t, repo.Root(), tool.TreeOverview(reg), `{"depth":2}`)
	tree, ok := out.(tool.TreeOverviewResult)
	if !ok {
		t.Fatalf("unexpected TreeOverview type: %T", out)
	}
	// Assert at least the auth and payments packages appear. The
	// fixture has auth/login.go, auth/session.go, auth/login_test.go,
	// payments/pay.go. Top-level node IDs are the package names.
	names := listExportedNames(tree)
	if len(names) == 0 {
		t.Fatalf("step 1 tree_overview: no exported names surfaced; got %+v", tree)
	}
	// Spot-check: at least one auth-package function is in the list.
	found := false
	for _, n := range names {
		if strings.HasPrefix(n, "auth.") || strings.HasPrefix(n, "fn:auth.") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected at least one auth.* export in tree_overview; got %v", names)
	}
}

// walk is a depth-first traversal over TreeOverviewResult nodes.
// ponytail: not generic — the tree shape here is shallow (depth 2) and
// recursive walk takes 6 lines.
func walk(n tool.TreeOverviewResult, fn func(tool.TreeOverviewResult) bool) {
	if !fn(n) {
		return
	}
	for _, child := range n.Children {
		walk(child, fn)
	}
}

// listExportedNames flattens a TreeOverviewResult into the set of node
// IDs that have kind FUNCTION / METHOD / CLASS (the surface an agent
// would report in a "what's exported" answer).
//
// ponytail: kind constants are uppercase (REPO/PACKAGE/FILE/FUNCTION/
// METHOD/CLASS) per domain.NodeKind — matching against the lowercase
// tree node names trips a real-world string-comparison bug we want to
// surface here, not hide.
func listExportedNames(n tool.TreeOverviewResult) []string {
	var out []string
	walk(n, func(node tool.TreeOverviewResult) bool {
		switch node.Kind {
		case "FUNCTION", "METHOD", "CLASS":
			out = append(out, node.ID)
		}
		return true
	})
	return out
}

// TestFidelity_Task4_DiffImpact_PublicSurfaceChange verifies the canonical
// flow "did the public surface of auth/login.go change since HEAD~1".
//
//	Prompt: Did the public surface of auth/login.go change since HEAD~1?
//
// The fixture's git history doesn't exist, so the test sets up a fresh
// repo with two commits: v1 has `Login` returning a hard-coded string; v2
// adds a parameter `token` to `Login`. The diff between HEAD~1 and HEAD
// must surface auth/login.go in the `files` list AND bind a `Change`
// row to `fn:auth.Login` — the symbol resolver's tiered fallback
// (see internal/tool/detectchanges.go:enclosingSymbol) anchors the
// hunk's first row to `Login` even when the diff also adds helper
// declarations on trailing rows.
func TestFidelity_Task4_DiffImpact_PublicSurfaceChange(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not on PATH: %v", err)
	}

	v1 := map[string]string{
		"go.mod": "module sample\n\ngo 1.22\n",
		"auth/login.go": `package auth

// Login returns a fixed session id.
func Login() string { return "sess-1" }
`,
	}
	v2 := map[string]string{
		"go.mod": "module sample\n\ngo 1.22\n",
		"auth/login.go": `package auth

// Login authenticates a user by token.
func Login(token string) (string, error) {
	if token == "" {
		return "", errBadToken
	}
	return "sess-" + token, nil
}

var errBadToken = errStr("bad token")

type strErr string

func (e strErr) Error() string { return string(e) }
`,
	}

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
	run("config", "user.email", "fidelity@local")
	run("config", "user.name", "fidelity_test")
	run("config", "commit.gpgsign", "false")
	run("add", "-A")
	run("commit", "-q", "-m", "v1")
	writeFiles(t, dir, v2)
	run("add", "-A")
	run("commit", "-q", "-m", "v2")

	repo, _, err := store.Load(dir)
	if err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	reg := seedRegForFidelity(t, repo.Root())

	// Step 1: detect_changes against HEAD~1 for auth/login.go.
	dc := drive(t, repo.Root(), tool.DetectChanges(reg),
		`{"project":"file://` + repo.Root() + `","base":"HEAD~1","scope":["auth/login.go"]}`)

	// Final assertions:
	//   1. auth/login.go is in the `files` list (file-level summary).
	//   2. a `Change` row is bound to `fn:auth.Login` (symbol-level
	//      attribution). The tiered resolver guarantees this even when
	//      the hunk overflows `Login`'s body with trailing declarations.
	files, ok := dc["files"].([]any)
	if !ok {
		t.Fatalf("step 1 detect_changes: missing 'files' field; got %v", dc)
	}
	foundFile := false
	for _, f := range files {
		if m, ok := f.(map[string]any); ok {
			if name, _ := m["file"].(string); name == "auth/login.go" {
				foundFile = true
				break
			}
		}
	}
	if !foundFile {
		t.Fatalf("step 1 detect_changes: expected auth/login.go in files; got %v", files)
	}

	changes, ok := dc["changes"].([]any)
	if !ok {
		t.Fatalf("step 1 detect_changes: missing 'changes' field; got %v", dc)
	}
	foundSymbol := false
	for _, c := range changes {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		sym, ok := cm["symbol"].(map[string]any)
		if !ok {
			continue
		}
		if id, _ := sym["id"].(string); id == "fn:auth.Login" {
			foundSymbol = true
			break
		}
	}
	if !foundSymbol {
		t.Fatalf("step 1 detect_changes: expected fn:auth.Login in changes; got %v", changes)
	}
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

// TestFidelity_KindMapping_RoundTrips pins the cross-layer kind
// mapping contract end-to-end. Per issue #25: a model that sees
// `kind:"FUNCTION"` in a tool response and writes a filter against the
// grammar spelling should silently fail today; this test makes the
// mapping discoverable via `get_graph_schema`'s `kindMap` field and
// pins the canonical table across FromParser/FromID round-trips.
//
// Four steps:
//  1. get_graph_schema surfaces a non-empty kindMap keyed by every
//     canonical kind, each carrying a grammar-form list and id prefix.
//  2. For every (canonical, grammar) pair in the table, FromParser
//     produces an Entity whose DomainKind/IDKind match the listing.
//  3. For every grammar form, the canonical ID survives a parse →
//     FromID round-trip with the same DomainKind.
//  4. For a known method in the fixture repo (payments.Wallet.Charge),
//     entityFromSymbol resolves the receiver to the Wallet class.
func TestFidelity_KindMapping_RoundTrips(t *testing.T) {
	repo := loadFixtureRepo(t)
	_ = seedRegForFidelity(t, repo.Root())

	// Step 1: get_graph_schema surfaces kindMap.
	schema := drive(t, repo.Root(), tool.GetGraphSchema(), `{}`)
	kindMapRaw, ok := schema["kindMap"].(map[string]any)
	if !ok || len(kindMapRaw) == 0 {
		t.Fatalf("step 1 get_graph_schema: missing or empty kindMap; got %v", schema["kindMap"])
	}

	// Step 2: every (canonical, grammar) pair in the table produces an
	// Entity with matching DomainKind/IDKind via FromParser.
	for domainKindRaw, m := range kindMapRaw {
		mapping, ok := m.(map[string]any)
		if !ok {
			t.Errorf("step 2 kindMap[%q] not an object; got %T", domainKindRaw, m)
			continue
		}
		grammarList, _ := mapping["grammar"].([]any)
		idPrefix, _ := mapping["id"].(string)
		if idPrefix == "" {
			t.Errorf("step 2 kindMap[%q].id is empty", domainKindRaw)
		}
		for _, g := range grammarList {
			grammar, _ := g.(string)
			// Methods need a Receiver populated so idKindFromGrammar
			// returns "meth" instead of falling back to "fn".
			sym := parser.Symbol{Kind: grammar, Name: "X", Receiver: "R"}
			ent := entity.FromParser(sym, "pkg/file.go", "pkg")
			if string(ent.DomainKind()) != domainKindRaw {
				t.Errorf("step 2 grammar=%q → DomainKind=%q, want %q",
					grammar, ent.DomainKind(), domainKindRaw)
			}
			if string(ent.IDKind()) != idPrefix {
				t.Errorf("step 2 grammar=%q → IDKind=%q, want %q",
					grammar, ent.IDKind(), idPrefix)
			}
		}
	}

	// Step 3: every grammar form's canonical ID round-trips through
	// Parse → FromID with the same DomainKind.
	for _, m := range kindMapRaw {
		mapping, _ := m.(map[string]any)
		grammarList, _ := mapping["grammar"].([]any)
		for _, g := range grammarList {
			grammar, _ := g.(string)
			sym := parser.Symbol{Kind: grammar, Name: "X", Receiver: "R"}
			orig := entity.FromParser(sym, "pkg/file.go", "pkg")
			parsed, err := id.Parse(orig.ID())
			if err != nil {
				t.Errorf("step 3 Parse(%q) = %v", orig.ID(), err)
				continue
			}
			recovered := entity.FromID(parsed)
			if recovered.DomainKind() != orig.DomainKind() {
				t.Errorf("step 3 DomainKind round-trip: %q → %q → %q",
					grammar, orig.DomainKind(), recovered.DomainKind())
			}
			if recovered.ID() != orig.ID() {
				t.Errorf("step 3 ID round-trip: %q → %q → %q",
					grammar, orig.ID(), recovered.ID())
			}
		}
	}

	// Step 4: a known method (payments.Wallet.Charge) resolves its
	// receiver via the symbol index. The fixture deliberately adds
	// a Wallet struct + method so this step has something to resolve.
	// We mirror the tool layer's entityFromSymbol resolution pass:
	// build the entity, then call ResolveReceivers with a callback
	// that wraps repo.Lookup (try-with-pkg, then without).
	var methodEntities []entity.Entity
	for _, path := range repo.Files() {
		for _, s := range repo.Symbols(path) {
			if s.Kind != "method_declaration" || s.Name != "Charge" {
				continue
			}
			methodEntities = append(methodEntities,
				entity.FromParser(s, path, store.PackagePath(repo.Root(), path)))
		}
	}
	lookup := entity.ReceiverLookup(func(receiverName, pkg string) (entity.Entity, bool) {
		for _, m := range repo.Lookup(pkg, receiverName) {
			cand := entity.FromParser(m.Sym, m.File, store.PackagePath(repo.Root(), m.File))
			if cand.DomainKind().IsCode() {
				return cand, true
			}
		}
		for _, m := range repo.Lookup("", receiverName) {
			cand := entity.FromParser(m.Sym, m.File, store.PackagePath(repo.Root(), m.File))
			if cand.DomainKind().IsCode() {
				return cand, true
			}
		}
		return entity.Entity{}, false
	})
	entity.ResolveReceivers(methodEntities, lookup)

	found := false
	for _, ent := range methodEntities {
		if ent.Receiver() == nil {
			continue
		}
		if ent.Receiver().DomainKind() != domain.KindClass {
			t.Errorf("step 4 %s: receiver kind = %q, want CLASS",
				ent.ID(), ent.Receiver().DomainKind())
		}
		found = true
	}
	if !found {
		t.Error("step 4: payments.Wallet.Charge did not resolve its receiver; " +
			"check the fixture's Wallet struct exists in payments/pay.go")
	}
}

// TestFidelity_AgentFlow_TransitiveCallers pins the issue #33 success
// criterion: an agent answers "where is X used, transitively, and what
// calls into it?" in ≤4 tool calls using only yactt's MCP tools, with
// no client-side filtering.
//
//	Prompt: Where is auth.Login defined, and what transitively calls it?
//
// Optimal sequence (counted): 1) find_symbol to resolve the name, 2)
// query_graph(follow=["callers"], depth=N) for transitive callers. Total
// 2 calls — well under the 4-call budget. The test asserts the count is
// ≤4 and the final result includes the test caller (`fn:auth.TestLogin_Success`
// or similar — anything that calls Login transitively).
//
// ponytail: the call counter is per-handler invocation, so each drive()
// counts as one. We assert `≤4` to leave room for retries on miss, not
// to mandate the optimal path.
func TestFidelity_AgentFlow_TransitiveCallers(t *testing.T) {
	repo := loadFixtureRepo(t)
	reg := seedRegForFidelity(t, repo.Root())

	var calls int
	countingDrive := func(argsJSON string) map[string]any {
		t.Helper()
		calls++
		return drive(t, repo.Root(), tool.FindSymbol(reg), argsJSON)
	}

	// Step 1: locate Login (1 call).
	sym := countingDrive(`{"name_path":"auth.Login"}`)
	syms, ok := sym["symbols"].([]any)
	if !ok || len(syms) == 0 {
		t.Fatalf("step 1: expected to locate auth.Login; got %v", sym)
	}
	login, ok := syms[0].(map[string]any)
	if !ok {
		t.Fatalf("step 1: symbols[0] type %T; want object", syms[0])
	}
	loginID, _ := login["node"].(map[string]any)
	if loginID == nil {
		// Try the alternate shape: in some envs Node is nested
		// differently. Fall back to the id-keyed shape.
		if id, ok := login["id"].(string); ok {
			loginID = map[string]any{"id": id}
		}
	}
	if loginID == nil {
		t.Fatalf("step 1: missing 'node' object; got %v", login)
	}
	loginIDStr, _ := loginID["id"].(string)
	if loginIDStr == "" {
		t.Fatalf("step 1: missing node id; got %v", loginID)
	}

	// Step 2: transitive callers via query_graph (1 call).
	calls++
	raw, err := tool.QueryGraph(reg)(context.Background(),
		json.RawMessage(driveWithProject(t, repo.Root(),
			fmt.Sprintf(`{"from":%q,"follow":["callers"],"depth":3,"limit":50}`, loginIDStr))))
	if err != nil {
		t.Fatalf("step 2 query_graph: %v", err)
	}
	b, _ := json.Marshal(raw)
	var env map[string]any
	_ = json.Unmarshal(b, &env)
	rows, _ := env["rows"].([]any)
	if len(rows) == 0 {
		t.Fatalf("step 2 query_graph: expected ≥1 transitive caller for %s; got 0 (calls so far: %d)", loginIDStr, calls)
	}

	// Success criterion: ≤4 tool calls. The agent can retry on miss
	// without violating the budget. We allow generous slack — the
	// point is "an agent CAN answer this in few calls", not "must
	// use exactly the optimal path".
	if calls > 4 {
		t.Errorf("agent flow used %d tool calls; success criterion is ≤4", calls)
	}
}
