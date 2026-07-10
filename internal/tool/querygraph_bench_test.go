package tool

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kellenff/yactt/internal/store"
	"github.com/kellenff/yactt/internal/store/repofixture"
)

// BenchmarkQueryGraph_5HopCallers measures multi-hop latency on the
// standard repofixture. Three cases:
//
//	callers_5  — single seed, 5-hop transitive callers (the issue #9 baseline)
//	seeds_5_3  — 5 seeds, 3-hop fanout (the GraphRAG seed-set path)
//	seeds_10_3 — 10 seeds, 3-hop fanout (proves the shared visited cap
//	             keeps multi-seed work predictable)
//
// The success criterion from issue #35 is "5-hop transitive-caller query
// on a 100K-LOC repo ≤500ms". The fixture here is the small
// `internal/store/repofixture` Go tree, not a 100K-LOC monorepo — the
// benchmark reports walltime on the small fixture so a future regression
// on the in-process dispatch path is caught. The 500ms budget is for
// the 100K-LOC scale; we don't pretend the small fixture is
// representative of that.
//
// Run with:
//
//	go test ./internal/tool/... -bench BenchmarkQueryGraph -benchtime=2s -run=^$
func BenchmarkQueryGraph_5HopCallers(b *testing.B) {
	fx := repofixture.New(b)
	r, _, err := store.Load(fx.Root)
	if err != nil {
		b.Fatalf("store.Load: %v", err)
	}
	b.Cleanup(func() { _ = r.Close() })

	handler := QueryGraph(r)

	cases := []struct {
		name string
		args string
	}{
		{
			name: "callers_5",
			args: `{"from":"fn:payments.Charge","follow":["callers"],"depth":5,"limit":100}`,
		},
		{
			name: "seeds_5_3",
			args: `{"seeds":["fn:auth.Login","fn:auth.Authenticate","meth:auth.User.Refresh","meth:auth.Alpha.Ping","meth:auth.Beta.Ping"],"follow":["callers","callees"],"depth":3,"limit":100}`,
		},
		{
			name: "seeds_10_3",
			args: `{"seeds":["fn:auth.Login","fn:auth.Authenticate","fn:payments.Charge","fn:payments.Refund","meth:auth.User.Refresh","meth:auth.Alpha.Ping","meth:auth.Beta.Ping","fn:auth.Login","fn:auth.Authenticate","fn:payments.Charge"],"follow":["callers","callees"],"depth":3,"limit":100}`,
		},
	}

	ctx := context.Background()
	for _, bc := range cases {
		b.Run(bc.name, func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				out, err := handler(ctx, json.RawMessage(bc.args))
				if err != nil {
					b.Fatalf("handler: %v", err)
				}
				_, ok := out.(*QueryGraphResult)
				if !ok {
					b.Fatalf("result type: %T", out)
				}
			}
		})
	}
}
