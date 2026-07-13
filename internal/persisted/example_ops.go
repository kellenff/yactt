package persisted

// RegisterExampleOps seeds the registry with the cheapest useful
// one-tool-call snapshots an agent reaches for at the start of a
// task. Each op is intentionally trivial — a single MCP tool call
// with static args — so the persisted_query MVP stays audit-free.
// Step-chaining and parameter forwarding are deliberate follow-ups.
//
//	"onboarding"    tree_overview depth=2 — repo at package level
//	"repo-map"      tree_overview depth=1 — top-level directories only
//	"architecture"  get_architecture    — languages, packages, hotspots,
//	                                     dead-code candidates, cycles
//	"graph_rag_demo" query_graph seeds=[main] — GraphRAG-shaped fanout
//	                                     from a common Go entry point
func RegisterExampleOps(r *Registry) {
	r.MustRegister(Op{
		ID:          "onboarding",
		Description: "Repo map: tree_overview at depth 2 — top packages and files only.",
		Tool:        "tree_overview",
		Args: map[string]any{
			"depth": 2,
		},
	})
	r.MustRegister(Op{
		ID:          "repo-map",
		Description: "Top-level directory map: tree_overview at depth 1 — packages and notable files only.",
		Tool:        "tree_overview",
		Args: map[string]any{
			"depth": 1,
		},
	})
	r.MustRegister(Op{
		ID:          "architecture",
		Description: "Architecture snapshot: languages, packages, hotspots, dead-code candidates, and import cycles — one call to get_architecture with default top-10 cap and cycle detection enabled.",
		Tool:        "get_architecture",
		Args: map[string]any{
			"top":            10,
			"include_cycles": true,
		},
	})
	r.MustRegister(Op{
		ID:          "graph_rag_demo",
		Description: "GraphRAG-shaped fanout: query_graph with a multi-seed set, follow=callers+callees, depth=2. Demonstrates the new seeds parameter from issue #35. Surfaces an error if fn:main.main doesn't resolve in the loaded repo.",
		Tool:        "query_graph",
		Args: map[string]any{
			"seeds":  []string{"fn:main.main"},
			"follow": []string{"callers", "callees"},
			"depth":  2,
			"limit":  50,
		},
	})
}
