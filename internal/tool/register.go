package tool

import (
	"github.com/kellenff/yactt/internal/audit"
	"github.com/kellenff/yactt/internal/mcp"
	"github.com/kellenff/yactt/internal/persisted"
	"github.com/kellenff/yactt/internal/registry"
)

// RegisterAllTools wires the canonical tool set onto srv.
//
// All 21 tools register here. The four registry tools
// (list_projects, index_repository, index_status, delete_project)
// work against the registry directly; the 15 code-intel tools
// resolve their project URI through `reg` on every call (no
// pre-loaded *store.Repo needed).
//
// `emitStartup` and `warnTrust` are the audit + TOFU hooks used
// by index_repository. They run at most once per process
// (memoised inside IndexRepository). The persistent HTTP
// transport passes nil for both — its daemon lifecycle is
// different from stdio and these hooks don't apply.
//
// Extracted from cmd/yactt/main.go so the persistent HTTP
// transport can share the wiring without duplicating the
// descriptions.
func RegisterAllTools(srv *mcp.Server, reg *registry.Registry, emitStartup func(audit.Startup) error, warnTrust func()) {
	// Four registry tools — all the server exposes, plus the 15
	// code-intel tools below. There is no single-repo boot path;
	// every code-intel tool resolves its project URI through `reg`.
	srv.RegisterTool(mcp.ToolDef{
		Name: "list_projects", Description: "Enumerate every project in the registry, sorted by path. Call first when an agent joins an MCP session and doesn't yet know which repos are available.",
		InputSchema: ListProjectsSchema, OutputSchema: ListProjectsOutputSchema,
		Handler: ListProjects(reg),
	})
	srv.RegisterTool(mcp.ToolDef{
		Name: "index_repository", Description: "Required first call: walk a repo at `project` (file:// URI), write an entry to the registry, return the row. After this returns, the 15 code-intel tools appear in `tools/list`. Mode knob is accepted (only `full` is wired today). Emits a startup audit line on the first successful index per process.",
		InputSchema: IndexRepositorySchema, OutputSchema: IndexRepositoryOutputSchema,
		Handler: IndexRepository(reg, emitStartup, warnTrust),
	})
	srv.RegisterTool(mcp.ToolDef{
		Name: "index_status", Description: "Registry row + per-repo cache freshness for `path`. Use this to check whether a repo is already indexed (`cacheFresh=true`) or whether `index_repository` needs to run first (`cacheFresh=false`).",
		InputSchema: IndexStatusSchema, OutputSchema: IndexStatusOutputSchema,
		Handler: IndexStatus(reg),
	})
	srv.RegisterTool(mcp.ToolDef{
		Name: "delete_project", Description: "Evict `project` (file:// URI) from the registry and remove its per-repo cache directory. Idempotent on missing rows — safe to retry on a stale or half-deleted entry.",
		InputSchema: DeleteProjectSchema, OutputSchema: DeleteProjectOutputSchema,
		Handler: DeleteProject(reg),
	})

	// 15 repo-bound code-intel tools — all take a *registry.Registry
	// and resolve args.Project on every call.
	treeOverview := TreeOverview(reg)
	nodeGet := GetNode(reg)
	nodeSource := NodeSource(reg)
	nodeEdges := NodeEdges(reg)
	search := Search(reg)
	editImpact := EditImpact(reg)
	findSymbol := FindSymbol(reg)
	getSymbolsOverview := GetSymbolsOverview(reg)
	findCode := FindCode(reg)
	findReferencingSymbols := FindReferencingSymbols(reg)
	getGraphSchema := GetGraphSchema()
	getCodeSnippet := GetCodeSnippet(reg)
	getArchitecture := GetArchitecture(reg)
	queryGraph := QueryGraph(reg)
	searchCode := SearchCode(reg)
	detectChanges := DetectChanges(reg)

	tools := []mcp.ToolDef{
		{Name: "tree_overview", Description: "First call when orienting: map the repo structure (packages, files, top-level symbols). Tune `depth` (1–6, default 2); narrow with `scope` (absolute path under repo root) to drill into a package or subdir. Truncates at 16 KiB — start shallow, drill with `get_symbols_overview` once you know the path.", InputSchema: TreeOverviewSchema, OutputSchema: TreeOverviewOutputSchema, Handler: treeOverview},
		{Name: "node_get", Description: "After `tree_overview` / `find_symbol` / `search` returns a stable `id`, pull specific layers (`summary`, `signature`, `body`, `source`, `tokens`). Cheap → expensive: start with `summary`, escalate only when you need more. Don't call without an `id`.", InputSchema: GetNodeSchema, OutputSchema: GetNodeOutputSchema, Handler: nodeGet},
		{Name: "node_source", Description: "Lossless source text for a node (or a whole file via `id=file:<path>`). Use when you need the verbatim text, not a parsed layer. Pass `range=[start,end]` to bound.", InputSchema: NodeSourceSchema, OutputSchema: NodeSourceOutputSchema, Handler: nodeSource},
		{Name: "node_edges", Description: "Single-hop callers / callees / tests / overrides / imports for a node. For transitive (>1 hop) caller/callee chains, use `query_graph` instead — one `query_graph` call replaces a loop of `node_edges` calls.", InputSchema: NodeEdgesSchema, OutputSchema: NodeEdgesOutputSchema, Handler: nodeEdges},
		{Name: "search", Description: "BM25 over symbol name + doc-comment — best for fuzzy 'is there anything called *Foo*?' discovery. Not regex; use `find_code(pattern_kind=regex)` for line-shaped patterns. Lower limit than `find_symbol` (default 10) so prefer it for free-form search, `find_symbol` for exact lookup.", InputSchema: SearchSchema, OutputSchema: SearchOutputSchema, Handler: search},
		{Name: "edit_impact", Description: "Required before any rename: returns the blast radius (callers, tests, overrides) without applying. Pair with the Edit tool afterward. Pass renames as [{id:..., new_name:...}].", InputSchema: EditImpactSchema, OutputSchema: EditImpactOutputSchema, Handler: editImpact},
		{Name: "find_symbol", Description: "Glob over the name-path (`pkg.Name` or `pkg/Name`, with `*` allowed) when you know roughly what something is called. Default limit 20. On miss, the response includes a `suggestions` field with edit-distance matches — try those before falling back to `search`.", InputSchema: FindSymbolSchema, OutputSchema: FindSymbolOutputSchema, Handler: findSymbol},
		{Name: "get_symbols_overview", Description: "When you have a file path (not a symbol id): get the top-level symbols of that file. Cheaper than a loop of `node_get` calls. Use after `tree_overview(scope=...)` or `search` to drill into a known file.", InputSchema: GetSymbolsOverviewSchema, OutputSchema: GetSymbolsOverviewOutputSchema, Handler: getSymbolsOverview},
		{Name: "find_code", Description: "Line-shaped patterns: regex (`pattern_kind=regex`) for grep-ish work, or tree-sitter AST patterns (`pattern_kind=tree_sitter`) for code-shaped queries (e.g. 'all calls to Foo'). Use `scope` to limit to a directory. For 'which functions handle *X*?' use `search_code` instead.", InputSchema: FindCodeSchema, OutputSchema: FindCodeOutputSchema, Handler: findCode},
		{Name: "search_code", Description: "When you want 'which functions handle *X*?': wraps `find_code` and groups matches by enclosing function, deduped, ranked by structural importance. Best tool for 'where is X handled' questions. Use `find_code` when you need raw matches without the grouping.", InputSchema: SearchCodeSchema, OutputSchema: SearchCodeOutputSchema, Handler: searchCode},
		{Name: "find_referencing_symbols", Description: "Single-hop symbol-addressed alias of `node_edges`. Accepts a node ID OR a name_path. For multi-hop (transitive) callers/callees, use `query_graph` instead — `find_referencing_symbols` only returns direct neighbours.", InputSchema: FindReferencingSymbolsSchema, OutputSchema: FindReferencingSymbolsOutputSchema, Handler: findReferencingSymbols},
		{Name: "get_graph_schema", Description: "List the canonical node kinds (FUNCTION/METHOD/CLASS/MODULE/FILE/PACKAGE/REPO), edge kinds (callers/callees/tests/overrides/imports), and layer names. Call this first when you need to write a `query_graph` filter or any kind-aware query — no need to hardcode strings.", InputSchema: GetGraphSchemaSchema, OutputSchema: GetGraphSchemaOutputSchema, Handler: getGraphSchema},
		{Name: "get_code_snippet", Description: "Source slice for a symbol by stable id OR qualified name_path. One call replaces find_symbol+node_source. Use when you already know what you want and just need the body. On miss, the response includes a `suggestions` field with edit-distance matches.", InputSchema: GetCodeSnippetSchema, OutputSchema: GetCodeSnippetOutputSchema, Handler: getCodeSnippet},
		{Name: "get_architecture", Description: "Repo-level health at a glance: dead-code candidates, hot spots (high fan-out), import cycles, language breakdown, package list. Run once after `tree_overview` for a first-look snapshot. Capped per-section (dead-code ≤50, cycles ≤10) — re-call with filters if you need more.", InputSchema: GetArchitectureSchema, OutputSchema: GetArchitectureOutputSchema, Handler: getArchitecture},
		{Name: "query_graph", Description: "Trace reachability across multiple hops — the right tool for 'who transitively depends on X?' or 'what does X transitively reach?'. Single-seed (`from`) or multi-seed (`seeds`) — multi-seed is the GraphRAG shape that takes a vector-retriever's top-K hits as input and emits a ranked subgraph (per-row `score` = weight × 1/(1+depth)) ready for packing. One call replaces a loop of `node_edges` calls. `depth` 1–5 (default 2); cost-capped at 1000 rows / 5 s / 5000 visited nodes / 50 seeds so large traversals terminate safely. Pass `follow:[\"callers\"]` for transitive callers, `[\"callees\"]` for transitive callees, or alternate via `follow:[\"callees\",\"callers\"]`.", InputSchema: QueryGraphSchema, OutputSchema: QueryGraphOutputSchema, Handler: queryGraph},
		{Name: "detect_changes", Description: "Impact of a git-ref diff: changed files, hunks, enclosing function/method per hunk, and callers/tests/overrides per affected symbol. Accepts {base,head} or {since} (Issue #11).", InputSchema: DetectChangesSchema, OutputSchema: DetectChangesOutputSchema, Handler: detectChanges},
	}
	for _, t := range tools {
		srv.RegisterTool(t)
	}

	// Persistent query registry (Phase 1.5). Each tool handler is
	// exposed under its MCP name so an op's Tool field can reference
	// it directly. The example ops land in persisted/example_ops.go.
	toolFuncs := map[string]persisted.ToolFunc{
		"tree_overview":            treeOverview,
		"node_get":                 nodeGet,
		"node_source":              nodeSource,
		"node_edges":               nodeEdges,
		"search":                   search,
		"edit_impact":              editImpact,
		"find_symbol":              findSymbol,
		"get_symbols_overview":     getSymbolsOverview,
		"find_code":                findCode,
		"search_code":              searchCode,
		"find_referencing_symbols": findReferencingSymbols,
		"get_graph_schema":         getGraphSchema,
		"get_code_snippet":         getCodeSnippet,
		"get_architecture":         getArchitecture,
		"query_graph":              queryGraph,
		"detect_changes":           detectChanges,
	}
	RegisterPersistedQuery(srv, toolFuncs)
}

// RegisterPersistedQuery wires the persisted_query tool onto the
// server. Extracted from RegisterAllTools because it's the one
// tool that's both wired by stdio (via runMCPServe) and the HTTP
// transport — duplicating its ToolDef would invite drift.
//
// `toolFuncs` is the per-tool lookup table the runner uses to
// dispatch persisted-query ops to live tool handlers. Both
// transports pass the same table here (it's the 15 code-intel
// tools; the registry tools aren't persistent-query targets).
func RegisterPersistedQuery(srv *mcp.Server, toolFuncs map[string]persisted.ToolFunc) {
	preg := persisted.NewRegistry()
	persisted.RegisterExampleOps(preg)
	runner := persisted.NewRunner(preg, toolFuncs)
	srv.RegisterTool(mcp.ToolDef{
		Name:         "persisted_query",
		Description:  "Run a registered persisted query by id. Curated workflows (onboarding, etc.) ship as named ops.",
		InputSchema:  PersistedQuerySchema,
		OutputSchema: PersistedQueryOutputSchema,
		Handler:      PersistedQuery(runner),
	})
}