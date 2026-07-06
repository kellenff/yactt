// Package tool — search_code wraps find_code's scan in a different shape:
// group the raw matches into their containing functions, drop the duplicates,
// rank by structural importance (definitions first, popular functions next,
// tests last). Mirrors codebase-memory-mcp's search_code tool.
//
// Heavy lifting (file scan + enclosing-symbol detection) lives in
// find_code.go and is reused via the unexported findCodeRegex /
// findCodeTreeSitter helpers; this file only adds the group + rank layer.
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/store"
)

// SearchCodeArgs is the typed input for search_code. Mirrors find_code but
// drops `include_context` (always on for grouping) and drops the rank knob
// (YAGNI: a single importance strategy, named below).
type SearchCodeArgs struct {
	Pattern     string `json:"pattern"`
	PatternKind string `json:"pattern_kind"`
	Scope       string `json:"scope"`
	FileFilter  string `json:"file_filter"`
	Limit       int    `json:"limit"`
}

// SearchCodeMatch is one raw occurrence inside a group.
type SearchCodeMatch struct {
	Line    int    `json:"line"`
	Snippet string `json:"snippet"`
}

// SearchCodeBucket is the structural rank tier for a group. Stable strings
// ("definition"|"popular"|"test") so callers can compare against the wire
// payload without coupling to ordering.
type SearchCodeBucket string

const (
	bucketDefinition SearchCodeBucket = "definition"
	bucketPopular    SearchCodeBucket = "popular"
	bucketTest       SearchCodeBucket = "test"
)

// SearchCodeGroup is one containing symbol and every raw match inside it.
type SearchCodeGroup struct {
	NodeID         string             `json:"nodeId"`
	Kind           domain.NodeKind    `json:"kind"`
	Summary        string             `json:"summary"`
	File           string             `json:"file"`
	EnclosingRange domain.LineRange   `json:"enclosingRange"`
	MatchCount     int                `json:"matchCount"`
	Matches        []SearchCodeMatch  `json:"matches"`
	Bucket         SearchCodeBucket   `json:"bucket"`
}

// SearchCodeSchema is the JSON Schema for search_code.
var SearchCodeSchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "pattern":      { "type": "string" },
    "pattern_kind": { "enum": ["regex", "tree_sitter"], "default": "regex" },
    "scope":        { "type": "string" },
    "file_filter":  { "type": "string", "description": "Glob over file paths, e.g. 'src/auth/**/*.go'." },
    "limit":        { "type": "integer", "default": 50 }
  },
  "required": ["pattern"],
  "additionalProperties": false
}`)

// SearchCodeOutputSchema declares the envelope. Top-level type is `object`
// per the MCP structuredContent contract (validated at server boot via
// validateOutputSchema).
var SearchCodeOutputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "groups": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "nodeId":         { "type": "string" },
          "kind":           { "type": "string" },
          "summary":        { "type": "string" },
          "file":           { "type": "string" },
          "enclosingRange": {
            "type": "object",
            "properties": {
              "start": { "type": "integer" },
              "end":   { "type": "integer" }
            },
            "required": ["start", "end"]
          },
          "matchCount":     { "type": "integer" },
          "matches": {
            "type": "array",
            "items": {
              "type": "object",
              "properties": {
                "line":    { "type": "integer" },
                "snippet": { "type": "string" }
              },
              "required": ["line", "snippet"]
            }
          },
          "bucket":         { "type": "string", "enum": ["definition", "popular", "test"] }
        },
        "required": ["nodeId", "kind", "file", "matchCount", "matches", "bucket"]
      }
    },
    "provenance": {
      "type": "object",
      "properties": {
        "tool":      { "type": "string" },
        "version":   { "type": "string" },
        "fetchedAt": { "type": "string" }
      },
      "required": ["tool", "version", "fetchedAt"]
    }
  },
  "required": ["groups", "provenance"]
}`)

// SearchCode returns the MCP handler factory. Validates args at the
// boundary (pattern required + length-capped), runs the existing
// find_code scan with context on, groups by enclosing symbol, and ranks
// by structural importance.
func SearchCode(repo *store.Repo) func(ctx context.Context, args json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		var a SearchCodeArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("invalid search_code args: %w", err)
		}
		if a.Pattern == "" {
			return nil, fmt.Errorf("search_code: pattern is required")
		}
		if a.PatternKind == "" {
			a.PatternKind = "regex"
		}
		if a.Limit <= 0 {
			a.Limit = 50
		}

		// Pull the raw matches with context on (so each match carries
		// its enclosing symbol's NodeID). The find_code helpers are the
		// single source of truth for the scan + context computation —
		// search_code is purely the group + rank view.
		var matches []FindCodeMatch
		switch a.PatternKind {
		case "regex":
			if len(a.Pattern) > maxRegexPatternBytes {
				return nil, fmt.Errorf("search_code: pattern too long: %d > %d bytes", len(a.Pattern), maxRegexPatternBytes)
			}
			rx, err := regexp.Compile(a.Pattern)
			if err != nil {
				return nil, fmt.Errorf("search_code: invalid regex: %w", err)
			}
			matches = findCodeRegex(repo, a.Scope, a.FileFilter, rx, true, a.Limit*4)
			// ^ Overscan: a single function may produce several raw matches;
			// we don't know the dedup ratio up front. Cap at 4× the limit
			// for raw hits; the post-group limit below re-imposes the
			// caller's constraint on PER-GROUP output, which is the
			// shape the caller actually wants bounded.
		case "tree_sitter":
			if err := validateTreeSitterPattern(a.Pattern); err != nil {
				return nil, err
			}
			ts, err := findCodeTreeSitter(repo, a.Scope, a.FileFilter, a.Pattern, true, a.Limit*4)
			if err != nil {
				return nil, err
			}
			matches = ts
		default:
			return nil, fmt.Errorf("search_code: unknown pattern_kind %q", a.PatternKind)
		}

		groups := groupByEnclosingSymbol(matches, repo, a.Limit)
		return map[string]any{
			"groups":     groups,
			"provenance": domain.YacttProvenance(),
		}, nil
	}
}

// groupByEnclosingSymbol collapses FindCodeMatch rows into one
// SearchCodeGroup per enclosing NodeID, then ranks by structural
// importance. Matches without a context (file-level hits with no
// enclosing declaration) are dropped — surfacing them would require
// inventing a synthetic bucket, and issue #10's spec is about
// "containing functions".
func groupByEnclosingSymbol(matches []FindCodeMatch, repo *store.Repo, limit int) []SearchCodeGroup {
	type bucket struct {
		group SearchCodeGroup
		fanIn int
		// count tracked separately so we can sort by it without
		// recomputing len(matches) at every comparison.
		count int
	}
	byID := map[string]*bucket{}

	for _, m := range matches {
		if m.Context == nil {
			continue
		}
		b, ok := byID[m.Context.NodeID]
		if !ok {
			b = &bucket{
				group: SearchCodeGroup{
					NodeID:         m.Context.NodeID,
					Kind:           m.Context.Kind,
					Summary:        m.Context.Summary,
					File:           m.File,
					EnclosingRange: m.Context.EnclosingRange,
					Matches:        []SearchCodeMatch{},
				},
			}
			byID[m.Context.NodeID] = b
		}
		b.group.Matches = append(b.group.Matches, SearchCodeMatch{
			Line:    m.LineRange.Start,
			Snippet: m.Snippet,
		})
		b.count++
	}

	out := make([]SearchCodeGroup, 0, len(byID))
	for _, b := range byID {
		b.group.MatchCount = b.count
		b.group.Bucket = classifyBucket(b.group, repo)
		b.fanIn = fanInFor(b.group.NodeID, repo)
		out = append(out, b.group)
	}

	// Stable sort: bucket tier (definition=0, popular=1, test=2),
	// then match count desc (more matches = more relevant),
	// then fan-in desc (more callers = more central),
	// then NodeID asc for deterministic ties.
	sort.SliceStable(out, func(i, j int) bool {
		bi := bucketRank(out[i].Bucket)
		bj := bucketRank(out[j].Bucket)
		if bi != bj {
			return bi < bj
		}
		if out[i].MatchCount != out[j].MatchCount {
			return out[i].MatchCount > out[j].MatchCount
		}
		fi := fanInFor(out[i].NodeID, repo)
		fj := fanInFor(out[j].NodeID, repo)
		if fi != fj {
			return fi > fj
		}
		return out[i].NodeID < out[j].NodeID
	})

	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// classifyBucket assigns the structural-importance tier for a group.
//
// Definition: a real declaration kind (function/method/class/struct/
// interface/module) in a non-test file. These are what humans search
// for when they ask "where is X handled?".
//
// Test: any group whose file ends in `_test.go`. Tests are useful but
// rarely the answer to a "where is X in production" question.
//
// Popular: everything else — captures unknown-kind symbols and a
// future-proofing net for grammar changes.
func classifyBucket(g SearchCodeGroup, _ *store.Repo) SearchCodeBucket {
	if strings.HasSuffix(g.File, "_test.go") {
		return bucketTest
	}
	switch g.Kind {
	case domain.KindFunction, domain.KindMethod, domain.KindClass, domain.KindModule:
		return bucketDefinition
	}
	return bucketPopular
}

// bucketRank is the sort key for a bucket. Lower = appears sooner.
func bucketRank(b SearchCodeBucket) int {
	switch b {
	case bucketDefinition:
		return 0
	case bucketPopular:
		return 1
	case bucketTest:
		return 2
	}
	return 1 // unknown strings sort alongside popular (safe default)
}

// fanInFor returns the number of callers for a group's node ID, using
// the call-edge index populated during Load. Returns 0 when the index
// is empty or the symbol isn't indexed — caller count is a tiebreaker,
// not a hard requirement.
//
// The repo's call-edge index keys by unqualified name (see
// EdgesByCallee), so we strip the `<kind>:<pkg>.` prefix off the NodeID
// to derive it. For methods with a receiver the full name (e.g.
// "auth.User.Greet") isn't directly indexable this way — that's a
// known limitation; in practice methods still get a non-zero fan-in
// whenever any sibling uses the suffix-matched pattern because the
// edge key is the trailing dotted suffix. Cheap enough; if precision
// matters later, swap for a dedicated (pkg, receiver, name) lookup.
func fanInFor(nodeID string, repo *store.Repo) int {
	if nodeID == "" {
		return 0
	}
	name := nodeID
	if i := strings.Index(name, ":"); i >= 0 {
		name = name[i+1:]
	}
	// strip leading package prefix: "auth.Login" -> "Login"
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	return len(repo.EdgesByCallee(name))
}
