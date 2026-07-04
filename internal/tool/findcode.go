package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/store"
)

// FindCodeArgs is the typed input for find_code. Mirrors design §4.9.
type FindCodeArgs struct {
	Pattern        string `json:"pattern"`
	PatternKind    string `json:"pattern_kind"`
	Scope          string `json:"scope"`
	FileFilter     string `json:"file_filter"`
	IncludeContext bool   `json:"include_context"`
	Limit          int    `json:"limit"`
}

// FindCodeMatch is one hit across the corpus.
type FindCodeMatch struct {
	File      string           `json:"file"`
	LineRange domain.LineRange `json:"range"`
	Snippet   string           `json:"snippet"`
	Context   *FindCodeContext `json:"context,omitempty"`
}

// FindCodeContext provides enclosing function/class metadata.
type FindCodeContext struct {
	NodeID         string           `json:"nodeId"`
	Kind           domain.NodeKind  `json:"kind"`
	Summary        string           `json:"summary"`
	EnclosingRange domain.LineRange `json:"enclosingRange"`
}

// FindCodeSchema is the JSON Schema for find_code. Mirrors §4.9.
var FindCodeSchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "pattern":         { "type": "string" },
    "pattern_kind":    { "enum": ["regex", "tree_sitter"], "default": "regex" },
    "scope":           { "type": "string" },
    "file_filter":     { "type": "string", "description": "Glob over file paths, e.g. 'src/auth/**/*.go'." },
    "include_context": { "type": "boolean", "default": true },
    "limit":           { "type": "integer", "default": 50 }
  },
  "required": ["pattern"],
  "additionalProperties": false
}`)

// FindCode returns a Handler that emits AST-aware (or regex) matches.
//
// MVP supports regex (tree_sitter pattern is wired up via the Language
// interface but the runner itself is Phase 2 — see find_code §4.9).
func FindCode(repo *store.Repo) func(ctx context.Context, args json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		var a FindCodeArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("invalid find_code args: %w", err)
		}
		if a.Pattern == "" {
			return nil, fmt.Errorf("find_code: pattern is required")
		}
		if a.PatternKind == "" {
			a.PatternKind = "regex"
		}
		if a.Limit <= 0 {
			a.Limit = 50
		}
		switch a.PatternKind {
		case "regex":
			rx, err := regexp.Compile(a.Pattern)
			if err != nil {
				return nil, fmt.Errorf("find_code: invalid regex: %w", err)
			}
			return findCodeRegex(repo, a.Scope, a.FileFilter, rx, a.IncludeContext, a.Limit), nil
		case "tree_sitter":
			// Tree-sitter query runner is Phase 2 — return an explicit
			// "unsupported" so the caller knows to fall back to regex.
			return nil, fmt.Errorf("find_code: pattern_kind=tree_sitter is Phase 2; use regex")
		default:
			return nil, fmt.Errorf("find_code: unknown pattern_kind %q", a.PatternKind)
		}
	}
}

// findCodeRegex scans each matching file with a compiled regex. Enclosing
// context (when requested) is the closest enclosing function/class via the
// file's symbols list.
func findCodeRegex(repo *store.Repo, scope, fileFilter string, rx *regexp.Regexp, withContext bool, limit int) []FindCodeMatch {
	out := []FindCodeMatch{}
	for _, p := range repo.Files() {
		if scope != "" && !strings.HasPrefix(p, scope) {
			continue
		}
		if fileFilter != "" {
			ok, err := path.Match(fileFilter, p)
			if err == nil && !ok {
				continue
			}
		}
		f, err := repo.CachedFile(p)
		if err != nil {
			continue
		}
		lines, _ := f.Lines()
		for i, raw := range lines {
			line := string(raw)
			idxs := rx.FindAllStringIndex(line, -1)
			if len(idxs) == 0 {
				continue
			}
			match := FindCodeMatch{
				File:      p,
				LineRange: domain.LineRange{Start: i, End: i + 1},
				Snippet:   trimSnippet(line),
			}
			if withContext {
				ctx := enclosingContext(repo, p, i)
				if ctx != nil {
					match.Context = ctx
				}
			}
			out = append(out, match)
			if len(out) >= limit {
				return out
			}
		}
	}
	return out
}

func trimSnippet(s string) string {
	const max = 200
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// enclosingContext finds the smallest declaration in the file that contains
// `line`.
func enclosingContext(repo *store.Repo, path string, line int) *FindCodeContext {
	for _, s := range repo.Symbols(path) {
		if s.StartRow <= line && line < s.EndRow {
			return &FindCodeContext{
				NodeID:         symbolID(path, s, repo.Root()),
				Kind:           symbolKind(s),
				Summary:        symbolSummary(s),
				EnclosingRange: domain.LineRange{Start: s.StartRow, End: s.EndRow},
			}
		}
	}
	return nil
}
