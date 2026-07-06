package persisted

// RegisterExampleOps seeds the registry with a single starter op
// that demonstrates the shape: one tool call, static args, no
// parameter forwarding. New ops land here or in a future config-file
// loader.
//
// `onboarding` is the smallest useful workflow — "give me the lay of
// the land" — and shows agents what the persisted_query tool can
// do without committing to a curated-workflow file format.
func RegisterExampleOps(r *Registry) {
	r.MustRegister(Op{
		ID:          "onboarding",
		Description: "Repo map: tree_overview at depth 2 — top packages and files only.",
		Tool:        "tree_overview",
		Args: map[string]any{
			"repo":  "",
			"depth": 2,
		},
	})
}
