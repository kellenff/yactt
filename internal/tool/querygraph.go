package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/id"
	"github.com/kellenff/yactt/internal/parser"
	"github.com/kellenff/yactt/internal/store"
)

// Cost caps for query_graph. Mirrors the per-tool limits used by node_edges
// (limit 50), find_symbol (20), and get_architecture (gated Tarjan).
//
// The visited cap protects against an accidentally huge frontier; the
// wallclock cap (via context.WithTimeout) protects against a single scanner
// hanging on a pathological input.
const (
	queryGraphDefaultDepth = 2
	queryGraphMaxDepth     = 5
	queryGraphDefaultLimit = 100
	queryGraphMaxLimit     = 1000
	queryGraphMaxVisited   = 5000
	queryGraphMaxRuntime   = 5 * time.Second
)

// validFollowKinds is the set of edge kinds query_graph can walk. Mirrors
// the five kinds node_edges surfaces.
var validFollowKinds = map[string]struct{}{
	"callers":   {},
	"callees":   {},
	"tests":     {},
	"imports":   {},
	"overrides": {},
}

// QueryGraphArgs is the typed input for query_graph. The DSL is a curated
// vocabulary: from, follow (edge kinds to walk, cycled across hops), depth
// (max hops), kind (filter target NodeKind), exclude (skip test files),
// limit (cap result rows).
type QueryGraphArgs struct {
	From    string   `json:"from"`
	Follow  []string `json:"follow"`
	Depth   int      `json:"depth"`
	Kind    string   `json:"kind"`
	Exclude string   `json:"exclude"`
	Limit   int      `json:"limit"`
}

// QueryGraphRow is one row in the result. Mirrors NodeEdgesResult so clients
// already rendering edges can render rows with no new code; adds depth and
// via (the follow kind that produced this row) for traversal context.
type QueryGraphRow struct {
	EdgeKind      domain.EdgeKind   `json:"edgeKind"`
	TargetID      string            `json:"targetId"`
	TargetKind    domain.NodeKind   `json:"targetKind,omitempty"`
	TargetSummary string            `json:"targetSummary,omitempty"`
	Location      domain.Location   `json:"location"`
	Confidence    float64           `json:"confidence"`
	Provenance    domain.Provenance `json:"provenance"`
	Depth         int               `json:"depth"`
	Via           []string          `json:"via"`
}

// QueryGraphResult is the structuredContent envelope for query_graph.
//
// Count == len(Rows). Truncated is set when the answer was clipped by
// limit, visited cap, or wallclock. Visited is the count of scanner calls
// made (each step expands a frontier node). From/Followed echo the
// normalised input so clients can confirm what ran.
type QueryGraphResult struct {
	Rows       []QueryGraphRow   `json:"rows"`
	Count      int               `json:"count"`
	Truncated  bool              `json:"truncated"`
	Visited    int               `json:"visited"`
	From       string            `json:"from"`
	Followed   []string          `json:"followed"`
	Provenance domain.Provenance `json:"provenance"`
}

// QueryGraphSchema is the JSON Schema for query_graph. The from field is
// required; follow must be a non-empty list of the five edge kinds node_edges
// already surfaces.
var QueryGraphSchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "from":   { "type": "string" },
    "follow": {
      "type": "array",
      "minItems": 1,
      "items": { "enum": ["callers", "callees", "tests", "imports", "overrides"] }
    },
    "depth":    { "type": "integer", "minimum": 1, "maximum": 5, "default": 2 },
    "kind":     { "type": "string" },
    "exclude":  { "enum": ["tests"] },
    "limit":    { "type": "integer", "minimum": 1, "maximum": 1000, "default": 100 }
  },
  "required": ["from", "follow"],
  "additionalProperties": false
}`)

// QueryGraphOutputSchema declares the structuredContent shape. Top-level
// type:"object" is the MCP contract (server.go:166-169).
var QueryGraphOutputSchema = json.RawMessage(`{
  "type": "object",
  "required": ["rows", "count", "truncated", "visited", "from", "followed", "provenance"],
  "properties": {
    "rows": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["edgeKind", "targetId", "location", "confidence", "provenance", "depth", "via"],
        "properties": {
          "edgeKind":      { "type": "string" },
          "targetId":      { "type": "string" },
          "targetKind":    { "type": "string" },
          "targetSummary": { "type": "string" },
          "location":      { "type": "object" },
          "confidence":    { "type": "number" },
          "provenance":    { "type": "object" },
          "depth":         { "type": "integer" },
          "via":           { "type": "array", "items": { "type": "string" } }
        }
      }
    },
    "count":      { "type": "integer" },
    "truncated":  { "type": "boolean" },
    "visited":    { "type": "integer" },
    "from":       { "type": "string" },
    "followed":   { "type": "array", "items": { "type": "string" } },
    "provenance": { "type": "object" }
  },
  "additionalProperties": false
}`)

// frontierEntry pairs a file path with its parser.Symbol so we can hand the
// scanner a (file, sym) tuple at each step. We carry the full Symbol rather
// than just the ID because LocateSymbol already gives us the symbol and the
// scanners take (file, sym).
type frontierEntry struct {
	file string
	sym  parser.Symbol
}

// QueryGraph returns a Handler that performs a multi-hop traversal from a
// seed node. The traversal composes the existing scanXxx functions from
// nodeedges.go — no new graph API is introduced. Depth, result count, total
// visited nodes, and wallclock are all bounded so a misbehaving query can't
// OOM the process.
func QueryGraph(repo *store.Repo) func(ctx context.Context, args json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		var a QueryGraphArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("invalid query_graph args: %w", err)
		}
		follow, err := normaliseFollow(a.Follow)
		if err != nil {
			return nil, fmt.Errorf("query_graph: %w", err)
		}
		if a.From == "" {
			return nil, fmt.Errorf("query_graph: from is required")
		}
		if a.Depth == 0 {
			a.Depth = queryGraphDefaultDepth
		}
		if a.Depth < 1 || a.Depth > queryGraphMaxDepth {
			return nil, fmt.Errorf("query_graph: depth %d out of range [1,%d]", a.Depth, queryGraphMaxDepth)
		}
		if a.Limit == 0 {
			a.Limit = queryGraphDefaultLimit
		}
		if a.Limit < 1 || a.Limit > queryGraphMaxLimit {
			return nil, fmt.Errorf("query_graph: limit %d out of range [1,%d]", a.Limit, queryGraphMaxLimit)
		}
		kindFilter, err := normaliseKind(a.Kind)
		if err != nil {
			return nil, fmt.Errorf("query_graph: %w", err)
		}
		excludeTests, err := normaliseExclude(a.Exclude)
		if err != nil {
			return nil, fmt.Errorf("query_graph: %w", err)
		}

		nodeID, err := id.Parse(a.From)
		if err != nil {
			return nil, fmt.Errorf("query_graph: from %w", err)
		}
		fromFile, fromSym, ok, lerr := repo.LocateSymbol(nodeID)
		if lerr != nil || !ok {
			return nil, fmt.Errorf("query_graph: cannot locate %s", a.From)
		}

		rctx, cancel := context.WithTimeout(ctx, queryGraphMaxRuntime)
		defer cancel()

		// Prov stamped on every row. YacttProvenance — the answer is composed
		// in-process from in-memory indices, not from a parse pass.
		prov := domain.YacttProvenance()

		rows := []QueryGraphRow{}
		seen := map[string]bool{a.From: true}
		visited := 0
		truncated := false
		frontier := []frontierEntry{{file: fromFile, sym: fromSym}}

		for step := 0; step < a.Depth; step++ {
			if rctx.Err() != nil {
				truncated = true
				break
			}
			via := follow[step%len(follow)]
			next := []frontierEntry{}
			for _, fe := range frontier {
				if visited >= queryGraphMaxVisited {
					truncated = true
					break
				}
				if rctx.Err() != nil {
					truncated = true
					break
				}
				visited++
				edges := scanFor(via, repo, fe.file, fe.sym, a.Limit, &prov)
				for _, e := range edges {
					if seen[e.TargetID] {
						continue
					}
					seen[e.TargetID] = true
					// Walk through every discovered target regardless of
					// filters — the kind filter is a row-emission gate,
					// not a traversal gate. That way agents can drill
					// past intermediate non-matching nodes to find deeper
					// matches.
					tid, perr := id.Parse(e.TargetID)
					if perr == nil {
						if tf, ts, ok2, _ := repo.LocateSymbol(tid); ok2 {
							next = append(next, frontierEntry{file: tf, sym: ts})
						}
					}
					// Apply row-level filters.
					if excludeTests && strings.HasSuffix(e.Location.File, "_test.go") {
						continue
					}
					if kindFilter != "" && e.TargetKind != kindFilter {
						continue
					}
					if len(rows) >= a.Limit {
						truncated = true
						continue
					}
					rows = append(rows, QueryGraphRow{
						EdgeKind:      e.EdgeKind,
						TargetID:      e.TargetID,
						TargetKind:    e.TargetKind,
						TargetSummary: e.TargetSummary,
						Location:      e.Location,
						Confidence:    e.Confidence,
						Provenance:    e.Provenance,
						Depth:         step + 1,
						Via:           []string{via},
					})
				}
			}
			if len(next) == 0 {
				break
			}
			frontier = next
		}

		return &QueryGraphResult{
			Rows:       rows,
			Count:      len(rows),
			Truncated:  truncated,
			Visited:    visited,
			From:       a.From,
			Followed:   follow,
			Provenance: prov,
		}, nil
	}
}

// normaliseFollow lower-cases and validates each follow entry. Returning
// the normalised slice (not the input) keeps the rest of the handler
// lowercase-only and lets the error path run before any state changes.
func normaliseFollow(in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, fmt.Errorf("follow must be a non-empty list")
	}
	out := make([]string, 0, len(in))
	for _, raw := range in {
		f := strings.ToLower(strings.TrimSpace(raw))
		if _, ok := validFollowKinds[f]; !ok {
			return nil, fmt.Errorf("unknown follow kind %q (allowed: callers, callees, tests, imports, overrides)", raw)
		}
		out = append(out, f)
	}
	return out, nil
}

// normaliseKind upper-cases the kind filter and verifies it's a known
// NodeKind. Empty string is allowed (no filter).
func normaliseKind(in string) (domain.NodeKind, error) {
	if in == "" {
		return "", nil
	}
	k := domain.NodeKind(strings.ToUpper(strings.TrimSpace(in)))
	switch k {
	case domain.KindRepo, domain.KindPackage, domain.KindFile,
		domain.KindFunction, domain.KindMethod, domain.KindClass, domain.KindModule:
		return k, nil
	}
	return "", fmt.Errorf("unknown kind %q", in)
}

// normaliseExclude parses the exclude knob. Only "tests" is supported in v1
// — it filters rows whose target file ends in _test.go.
func normaliseExclude(in string) (bool, error) {
	if in == "" {
		return false, nil
	}
	if strings.EqualFold(strings.TrimSpace(in), "tests") {
		return true, nil
	}
	return false, fmt.Errorf("unknown exclude value %q (only \"tests\" is supported)", in)
}

// scanFor dispatches to the matching scanner. All five edge kinds reuse
// the existing scanXxx from nodeedges.go — the handlers share a package,
// so the dispatch is in-process and free.
func scanFor(kind string, repo *store.Repo, file string, sym parser.Symbol, limit int, p *domain.Provenance) []NodeEdgesResult {
	switch kind {
	case "callees":
		return scanCallees(repo, file, sym, limit, p)
	case "callers":
		return scanCallers(repo, file, sym, limit, p)
	case "tests":
		return scanTests(repo, file, sym, limit, p)
	case "imports":
		return scanImports(repo, file, sym, limit, p)
	case "overrides":
		return scanOverrides(repo, file, sym, limit, p)
	}
	return nil
}