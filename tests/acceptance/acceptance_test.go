// Package acceptance exercises the 10 MCP tools end-to-end against a small
// fixture Go project at ../fixtures/sample-go.
//
// Acceptance tests live separately from contract tests because they:
//
//   - load a real repo (slower — 10s of ms vs. µs),
//   - assert domain-level facts about outputs (e.g. "tree_overview emits a
//     user node with the fixture's functions"),
//   - catch correctness regressions that contract tests can't reach.
//
// Naming convention: each Test is named after a tool, with subtests for the
// scenarios it covers.
package acceptance_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/search"
	"github.com/kellenff/yactt/internal/store"
	"github.com/kellenff/yactt/internal/tool"
)

// fixtureRepo is the per-test repo handle. Cached across tests for speed.
var fixtureRepoCache *store.Repo

func loadRepo(t *testing.T) *store.Repo {
	t.Helper()
	if fixtureRepoCache != nil {
		return fixtureRepoCache
	}
	repo, errs, err := store.Load("../fixtures/sample-go")
	if err != nil {
		t.Fatalf("loading fixture: %v", err)
	}
	if len(errs) > 0 {
		t.Logf("load reported %d file errors: %v", len(errs), errs)
	}
	fixtureRepoCache = repo
	return repo
}

// call runs the handler with `argsJSON` and returns the raw result. Concrete
// type varies per tool; tests either direct-cast or JSON-round-trip through
// callAsMap for structural assertions.
func callJSON(t *testing.T, h func(ctx context.Context, args json.RawMessage) (any, error), argsJSON string) any {
	t.Helper()
	out, err := h(context.Background(), json.RawMessage(argsJSON))
	if err != nil {
		t.Fatalf("handler error: %v (args=%s)", err, argsJSON)
	}
	return out
}

// callAsMap runs the handler and JSON-round-trips the result. This normalises
// all types to map[string]any/slice/primitive shapes — convenient for tests
// that don't care about the concrete result type, only its contents.
func callAsMap(t *testing.T, h func(ctx context.Context, args json.RawMessage) (any, error), argsJSON string) any {
	t.Helper()
	out, err := h(context.Background(), json.RawMessage(argsJSON))
	if err != nil {
		t.Fatalf("handler error: %v (args=%s)", err, argsJSON)
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var asMap any
	if err := json.Unmarshal(b, &asMap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return asMap
}

// treeHelper walks a tree_overview result and returns the count of leaves.
func treeLeaves(tree any) int {
	m, ok := tree.(tool.TreeOverviewResult)
	if !ok {
		return 0
	}
	count := 0
	for _, c := range m.Children {
		count += treeLeaves(c)
	}
	count++ // this node itself
	return count
}

func TestTreeOverviewAcceptance(t *testing.T) {
	repo := loadRepo(t)
	out := callJSON(t, tool.TreeOverview(repo), `{"repo":"","depth":2}`)
	root, ok := out.(tool.TreeOverviewResult)
	if !ok {
		t.Fatalf("root type: got %T", out)
	}
	if root.ID == "" || root.Kind != domain.KindRepo {
		t.Fatalf("root: id=%q kind=%v", root.ID, root.Kind)
	}
	if treeLeaves(root) == 0 {
		t.Fatalf("root has no descendants; tree=%+v", root)
	}
	// Find a package node whose summary mentions `auth`.
	var found bool
	for _, p := range root.Children {
		if strings.Contains(p.Summary, "auth") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected an auth package in tree, got %d children", len(root.Children))
	}
}

func TestNodeGetAcceptance(t *testing.T) {
	repo := loadRepo(t)
	out := callJSON(t, tool.GetNode(repo), `{"id":"fn:auth.Login","layers":["summary","signature","body"]}`)
	n, ok := out.(*domain.Node)
	if !ok {
		t.Fatalf("node type: got %T", out)
	}
	if n.Summary == "" {
		t.Fatal("expected non-empty summary")
	}
	if n.Signature == nil || n.Signature.Text == "" {
		t.Fatalf("expected signature text; got %+v", n.Signature)
	}
	if n.Body == nil || len(n.Body.Stmts) == 0 {
		t.Fatalf("expected non-empty body")
	}
	// AST05 (Issue #2): the default summary layer must NOT include the
	// doc-comment prose. Assert the absence of "authenticates" and the
	// presence of a signature-derived fallback instead.
	if strings.Contains(strings.ToLower(n.Summary), "authenticates") {
		t.Fatalf("summary leaks doc-comment content (AST05 violation); got %q", n.Summary)
	}
	if !strings.HasPrefix(n.Summary, "Function:") {
		t.Fatalf("summary %q should use the <Kind>: prefix", n.Summary)
	}
}

func TestNodeSourceAcceptance(t *testing.T) {
	repo := loadRepo(t)
	out := callJSON(t, tool.NodeSource(repo), `{"id":"fn:auth.ValidateToken"}`)
	r, ok := out.(*tool.NodeSourceResult)
	if !ok {
		t.Fatalf("source type: got %T", out)
	}
	if r.Text == "" {
		t.Fatal("expected non-empty source")
	}
	if !strings.Contains(r.Text, "func ValidateToken") {
		t.Fatalf("expected function header in source, got %q", r.Text)
	}
}

func TestSearchAcceptance(t *testing.T) {
	repo := loadRepo(t)
	out := callJSON(t, tool.Search(repo), `{"query":"ValidateToken","scope":""}`)
	env, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("search envelope type: got %T", out)
	}
	hits, ok := env["results"].([]search.Result)
	if !ok {
		t.Fatalf("search results slice type: got %T", env["results"])
	}
	if len(hits) == 0 {
		t.Fatal("expected at least one search hit")
	}
	if hits[0].Node.ID != "fn:auth.ValidateToken" {
		t.Fatalf("expected first hit to be ValidateToken, got %s", hits[0].Node.ID)
	}
}

func TestGetSymbolsOverviewAcceptance(t *testing.T) {
	repo := loadRepo(t)
	out := callAsMap(t, tool.GetSymbolsOverview(repo), `{"file":"auth/login.go","depth":1}`)
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("overview type: got %T", out)
	}
	syms, ok := m["symbols"].([]any)
	if !ok {
		t.Fatalf("symbols slice type: got %T", m["symbols"])
	}
	if len(syms) < 3 {
		t.Fatalf("expected at least 3 symbols in login.go, got %d", len(syms))
	}
	want := map[string]bool{"Login": false, "ValidateToken": false, "SetSession": false}
	for _, s := range syms {
		n, _ := s.(map[string]any)
		if name, _ := n["name"].(string); name != "" {
			if _, ok := want[name]; ok {
				want[name] = true
			}
		}
	}
	for name, found := range want {
		if !found {
			t.Fatalf("expected symbol %q in login.go outline", name)
		}
	}
}

func TestFindCodeAcceptance(t *testing.T) {
	repo := loadRepo(t)
	out := callJSON(t, tool.FindCode(repo), `{"pattern":"ValidateToken","pattern_kind":"regex","limit":20}`)
	env, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("find_code envelope type: got %T", out)
	}
	hits, ok := env["matches"].([]tool.FindCodeMatch)
	if !ok {
		t.Fatalf("find_code matches slice type: got %T", env["matches"])
	}
	if len(hits) == 0 {
		t.Fatal("expected hits for ValidateToken")
	}
}

// TestGetGraphSchemaAcceptance pins the schema tool against the live
// fixture. The five enum slices must be present and the yactt provenance
// stamp must be set — Issue #7 acceptance criterion.
func TestGetGraphSchemaAcceptance(t *testing.T) {
	repo := loadRepo(t)
	out := callAsMap(t, tool.GetGraphSchema(repo), `{}`)
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("graph schema type: got %T", out)
	}
	for _, key := range []string{"nodeKinds", "edgeKinds", "layers", "defaultEdges", "codeKinds"} {
		slice, ok := m[key].([]any)
		if !ok {
			t.Fatalf("%s not a slice; got %T", key, m[key])
		}
		if len(slice) == 0 {
			t.Errorf("%s is empty", key)
		}
	}
	prov, ok := m["provenance"].(map[string]any)
	if !ok {
		t.Fatalf("provenance type: got %T", m["provenance"])
	}
	if tool, _ := prov["tool"].(string); tool != "yactt" {
		t.Errorf("provenance.tool = %q, want \"yactt\"", tool)
	}
}

// TestGetCodeSnippetAcceptance covers both input shapes (stable id +
// qualified name_path) and asserts the snippet includes the function
// header. Mirrors the canonical "show me the code for X" workflow that
// used to require find_symbol + node_source.
func TestGetCodeSnippetAcceptance(t *testing.T) {
	repo := loadRepo(t)

	// By id
	out := callAsMap(t, tool.GetCodeSnippet(repo), `{"id":"fn:auth.Login"}`)
	m := out.(map[string]any)
	if id, _ := m["id"].(string); id != "fn:auth.Login" {
		t.Errorf("by-id id = %q, want fn:auth.Login", id)
	}
	text, _ := m["text"].(string)
	if !strings.Contains(text, "func Login") {
		t.Errorf("by-id text missing function header; got:\n%s", text)
	}

	// By name_path
	out = callAsMap(t, tool.GetCodeSnippet(repo), `{"name_path":"auth.ValidateToken"}`)
	m = out.(map[string]any)
	if id, _ := m["id"].(string); id != "fn:auth.ValidateToken" {
		t.Errorf("by-name id = %q, want fn:auth.ValidateToken", id)
	}
}

// TestGetArchitectureAcceptance pins the architecture summary's envelope
// shape against the fixture: the section lists are all present and the
// fixture's language mix includes Go.
func TestGetArchitectureAcceptance(t *testing.T) {
	repo := loadRepo(t)
	out := callAsMap(t, tool.GetArchitecture(repo), `{}`)
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("architecture type: got %T", out)
	}

	// Top-level keys.
	for _, key := range []string{"summary", "languages", "topPackages", "hotspots", "deadCode", "importCycles", "provenance"} {
		if _, ok := m[key]; !ok {
			t.Errorf("missing key %q in architecture result", key)
		}
	}

	// Languages includes Go on the fixture.
	langs, ok := m["languages"].([]any)
	if !ok {
		t.Fatalf("languages slice type: got %T", m["languages"])
	}
	hasGo := false
	for _, l := range langs {
		entry, _ := l.(map[string]any)
		if name, _ := entry["name"].(string); name == "go" {
			hasGo = true
		}
	}
	if !hasGo {
		t.Errorf("expected Go in languages; got %+v", langs)
	}
}

func TestFindSymbolAcceptance(t *testing.T) {
	repo := loadRepo(t)
	out := callJSON(t, tool.FindSymbol(repo), `{"name_path":"auth/Login*","limit":10,"include_body":true}`)
	env, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("find_symbol envelope type: got %T", out)
	}
	hits, ok := env["symbols"].([]tool.FindSymbolResult)
	if !ok {
		t.Fatalf("find_symbol symbols slice type: got %T", env["symbols"])
	}
	if len(hits) == 0 {
		t.Fatal("expected at least one match for auth/Login*")
	}
}

func TestFindReferencingSymbolsAcceptance(t *testing.T) {
	repo := loadRepo(t)
	out := callAsMap(t, tool.FindReferencingSymbols(repo), `{"symbol":"fn:auth.ValidateToken","kinds":["tests","calls"]}`)
	env, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("find_referencing_symbols envelope type: got %T", out)
	}
	edges, ok := env["references"].([]any)
	if !ok {
		t.Fatalf("find_referencing_symbols references slice type: got %T", env["references"])
	}
	if len(edges) == 0 {
		t.Fatal("expected at least one edge (tests or callers)")
	}
}

func TestEditImpactAcceptance(t *testing.T) {
	repo := loadRepo(t)
	out := callAsMap(t, tool.EditImpact(repo), `{"renames":[{"id":"fn:auth.SetSession","new_name":"OpenSession"}]}`)
	r, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("edit_impact type: got %T", out)
	}
	renames, ok := r["renames"].([]any)
	if !ok || len(renames) != 1 {
		t.Fatalf("expected one rename analysis, got %d", len(renames))
	}
	first := renames[0].(map[string]any)
	if first["safeToRename"] != true {
		t.Fatalf("expected safe_to_rename=true, got %+v", first)
	}
}

func TestNodeEdgesAcceptance(t *testing.T) {
	repo := loadRepo(t)
	out := callAsMap(t, tool.NodeEdges(repo), `{"id":"fn:auth.Login","kinds":["callees"],"limit":10}`)
	env, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("node_edges envelope type: got %T", out)
	}
	edges, ok := env["edges"].([]any)
	if !ok {
		t.Fatalf("node_edges edges slice type: got %T", env["edges"])
	}
	if len(edges) == 0 {
		t.Fatal("expected callees for Login")
	}
}

// TestNodeGet_CrossPackage_Charge confirms that node_get returns the
// signature + body layers for `fn:payments.Charge` and that the body
// stmts mention the function name. This is the cross-package node the
// smoke test surfaced; without LSP materialization it still works via
// the tree-sitter pass.
func TestNodeGet_CrossPackage_Charge(t *testing.T) {
	repo := loadRepo(t)
	out := callJSON(t, tool.GetNode(repo), `{"id":"fn:payments.Charge","layers":["signature","body"]}`)
	n, ok := out.(*domain.Node)
	if !ok {
		t.Fatalf("node_get type: got %T", out)
	}
	if n == nil {
		t.Fatal("node_get returned nil")
	}
	if n.ID != "fn:payments.Charge" {
		t.Fatalf("id=%q want fn:payments.Charge", n.ID)
	}
	if n.Signature == nil || !strings.Contains(n.Signature.Text, "Charge") {
		t.Fatalf("signature missing or empty: %+v", n.Signature)
	}
	if n.Body == nil || len(n.Body.Stmts) == 0 {
		t.Fatalf("body missing or empty: %+v", n.Body)
	}
}

// TestNodeGet_CrossPackage_Login_BodyMentionsCharge confirms that the
// body of `auth.Login` references `payments.Charge` somewhere in its
// statements. Without the field_identifier fix in extractCalleeName,
// the body would still serialise (it just wouldn't link across
// packages); the substantive regression this catches is that the
// body layer is *not* accidentally empty for cross-package callers.
func TestNodeGet_CrossPackage_Login_BodyMentionsCharge(t *testing.T) {
	repo := loadRepo(t)
	out := callJSON(t, tool.GetNode(repo), `{"id":"fn:auth.Login","layers":["body"]}`)
	n, ok := out.(*domain.Node)
	if !ok {
		t.Fatalf("node_get type: got %T", out)
	}
	if n == nil || n.Body == nil {
		t.Fatal("node_get returned nil or body layer nil")
	}
	mentions := false
	for _, stmt := range n.Body.Stmts {
		if strings.Contains(stmt.Text, "payments.Charge") {
			mentions = true
			break
		}
	}
	if !mentions {
		t.Fatalf("expected a body stmt mentioning payments.Charge, got %+v", n.Body.Stmts)
	}
}

// TestFindSymbol_ExactNamePath exercises the name-path matching path of
// find_symbol. The slash-separated path "auth/Login" resolves to the
// package prefix "auth." plus the symbol-name pattern "Login", so the
// matcher can land on `auth.Login` without wildcards. Catches regressions
// in the segment-splitting logic of findsymbol.go.
func TestFindSymbol_ExactNamePath(t *testing.T) {
	repo := loadRepo(t)
	out := callJSON(t, tool.FindSymbol(repo), `{"name_path":"auth/Login","limit":5,"include_body":false}`)
	env, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("find_symbol envelope type: got %T", out)
	}
	hits, ok := env["symbols"].([]tool.FindSymbolResult)
	if !ok {
		t.Fatalf("find_symbol symbols slice type: got %T", env["symbols"])
	}
	if len(hits) == 0 {
		t.Fatal("expected at least one match for auth/Login")
	}
	if hits[0].Node == nil || hits[0].Node.ID != "fn:auth.Login" {
		t.Fatalf("first hit node id=%q want fn:auth.Login", hits[0].Node.ID)
	}
}

// TestFindSymbol_PrefixGlob_Login documents the prefix-glob behaviour:
// `auth/Login*` is the package hint + a glob pattern; it should resolve
// to `Login` (the function under test) without picking up unrelated
// entries in the same package. This is the matchByNamePattern contract
// the smoke test relied on.
func TestFindSymbol_PrefixGlob_Login(t *testing.T) {
	repo := loadRepo(t)
	out := callJSON(t, tool.FindSymbol(repo), `{"name_path":"auth/Login*","limit":10,"include_body":false}`)
	env, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("find_symbol envelope type: got %T", out)
	}
	hits, ok := env["symbols"].([]tool.FindSymbolResult)
	if !ok {
		t.Fatalf("find_symbol symbols slice type: got %T", env["symbols"])
	}
	if len(hits) == 0 {
		t.Fatal("expected at least one match for auth/Login*")
	}
	loginHit := false
	for _, h := range hits {
		if h.Node != nil && h.Node.ID == "fn:auth.Login" {
			loginHit = true
			break
		}
	}
	if !loginHit {
		t.Fatalf("expected fn:auth.Login in prefix-glob hits, got %+v", hits)
	}
}

// TestFindCode_RegexPattern_Charge verifies the regex pattern_kind of
// find_code against the cross-package fixture. The smoke test attempted
// `pattern_kind=substring` which the schema does not list as a valid
// value; the regression we guard here is that the regex engine still
// returns the cross-package call site (auth/login.go: `payments.Charge(sess)`).
func TestFindCode_RegexPattern_Charge(t *testing.T) {
	repo := loadRepo(t)
	out := callJSON(t, tool.FindCode(repo), `{"pattern":"payments\\.Charge","pattern_kind":"regex","limit":20}`)
	env, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("find_code envelope type: got %T", out)
	}
	hits, ok := env["matches"].([]tool.FindCodeMatch)
	if !ok {
		t.Fatalf("find_code matches slice type: got %T", env["matches"])
	}
	if len(hits) == 0 {
		t.Fatal("expected at least one regex match for payments.Charge")
	}
	loginHit := false
	for _, h := range hits {
		if strings.Contains(h.File, "auth/login.go") {
			loginHit = true
			break
		}
	}
	if !loginHit {
		t.Fatalf("expected a hit in auth/login.go for payments.Charge, got %+v", hits)
	}
}

// TestFindReferencingSymbols_CrossPackage_Charge is the substantive
// regression guard for the field_identifier fix: callers of
// `payments.Charge` include `auth.Login` (the cross-package caller the
// Tier-1 LSP path also resolves, but Tier-2 must still catch it on
// machines without gopls).
func TestFindReferencingSymbols_CrossPackage_Charge(t *testing.T) {
	repo := loadRepo(t)
	out := callAsMap(t, tool.FindReferencingSymbols(repo), `{"symbol":"fn:payments.Charge","kinds":["calls"]}`)
	env, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("find_referencing_symbols envelope type: got %T", out)
	}
	edges, ok := env["references"].([]any)
	if !ok {
		t.Fatalf("find_referencing_symbols references slice type: got %T", env["references"])
	}
	if len(edges) == 0 {
		t.Fatal("expected at least one caller of payments.Charge")
	}
	loginHit := false
	for _, e := range edges {
		m, _ := e.(map[string]any)
		if id, _ := m["targetId"].(string); id == "fn:auth.Login" {
			loginHit = true
			break
		}
	}
	if !loginHit {
		t.Fatalf("expected fn:auth.Login in callers of payments.Charge, got %+v", edges)
	}
}
