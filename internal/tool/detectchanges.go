package tool

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/id"
	"github.com/kellenff/yactt/internal/parser"
	"github.com/kellenff/yactt/internal/store"
)

// Cost caps for detect_changes. Mirrors query_graph's per-tool ceiling
// pattern — bounded work, deterministic truncation marker.
const (
	detectChangesDefaultLimit = 20
	detectChangesMaxLimit     = 100
	detectChangesMaxResults   = 200
	detectChangesMaxRuntime   = 10 * time.Second
)

// DetectChangesArgs is the typed input for detect_changes. The caller
// supplies either {base, head} (two refs) or {since} (one ref, defaults
// head="HEAD"). Both base and since are mutually exclusive — the handler
// rejects if both are passed.
type DetectChangesArgs struct {
	Base  string `json:"base"`
	Head  string `json:"head"`
	Since string `json:"since"`
	Limit int    `json:"limit"`
}

// SymbolRef is a minimal pointer to the symbol affected by a hunk. Mirrors
// the columns exposed by find_symbol/get_code_snippet — enough to drive a
// follow-up call without re-resolving.
type SymbolRef struct {
	ID      string          `json:"id"`
	Kind    domain.NodeKind `json:"kind"`
	Summary string          `json:"summary"`
}

// Change is one affected symbol plus the fan-out of callers / tests /
// overrides computed for it. Multiple hunks inside the same declaration
// collapse into a single Change row.
type Change struct {
	File      string             `json:"file"`
	Ranges    []domain.LineRange `json:"ranges"`
	Symbol    *SymbolRef         `json:"symbol,omitempty"`
	Callers   []NodeEdgesResult  `json:"callers,omitempty"`
	Tests     []NodeEdgesResult  `json:"tests,omitempty"`
	Overrides []NodeEdgesResult  `json:"overrides,omitempty"`
}

// FileSummary is the per-file aggregate from the diff. Surfaced under
// `files` so callers can answer "how big is this diff?" without
// re-running git.
type FileSummary struct {
	File    string `json:"file"`
	Added   int    `json:"addedLines"`
	Deleted int    `json:"deletedLines"`
	Hunks   int    `json:"hunks"`
}

// DetectChangesResult is the structuredContent envelope. Count ==
// len(Changes). Truncated is set when the answer was clipped by the
// result cap. Base/Head echo the normalised input so clients can confirm
// what ran.
type DetectChangesResult struct {
	Changes    []Change          `json:"changes"`
	Files      []FileSummary     `json:"files"`
	Base       string            `json:"base"`
	Head       string            `json:"head"`
	Truncated  bool              `json:"truncated"`
	Provenance domain.Provenance `json:"provenance"`
}

// DetectChangesSchema is the JSON Schema for detect_changes. Either
// `base` or `since` is required (anyOf mirrors get_code_snippet's "id OR
// name_path" pattern). `head` defaults to "HEAD" when omitted.
var DetectChangesSchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "base":  { "type": "string", "description": "Base ref (branch, tag, SHA). Use 'since' instead for a one-sided diff." },
    "head":  { "type": "string", "description": "Head ref. Defaults to HEAD." },
    "since": { "type": "string", "description": "Shortcut for {base: <since>, head: HEAD}. Mutually exclusive with 'base'." },
    "limit": { "type": "integer", "description": "Per-symbol callers/tests/overrides cap. Default 20, max 100." }
  },
  "anyOf": [
    { "required": ["base"] },
    { "required": ["since"] }
  ],
  "additionalProperties": false
}`)

// DetectChangesOutputSchema declares the structuredContent shape.
// Top-level type:"object" satisfies the MCP contract
// (server.go:validateOutputSchema).
var DetectChangesOutputSchema = json.RawMessage(`{
  "type": "object",
  "required": ["changes", "files", "base", "head", "truncated", "provenance"],
  "properties": {
    "changes": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["file", "ranges"],
        "properties": {
          "file":      { "type": "string" },
          "ranges":    { "type": "array", "items": { "type": "object" } },
          "symbol":    { "type": "object" },
          "callers":   { "type": "array", "items": { "type": "object" } },
          "tests":     { "type": "array", "items": { "type": "object" } },
          "overrides": { "type": "array", "items": { "type": "object" } }
        }
      }
    },
    "files":      { "type": "array", "items": { "type": "object" } },
    "base":       { "type": "string" },
    "head":       { "type": "string" },
    "truncated":  { "type": "boolean" },
    "provenance": { "type": "object" }
  },
  "additionalProperties": false
}`)

// DetectChanges returns a Handler that takes two git refs (or a `since`
// shorthand) and reports the impact of the diff between them: changed
// files, hunks, the enclosing function/method for each hunk, and the
// callers / tests / overrides for each affected symbol.
//
// MVP scope: pure additions/modifications/copies. Renames (git's `-M`
// detection) surface as separate old+new entries in v1; cross-file parent
// resolution for OVERRIDES remains deferred to Phase 1.5 (see
// scanOverrides' own comment). The git-diff subprocess is the only
// shell-out in the tool — bounded by a 10 s wallclock and the per-tool
// result cap.
func DetectChanges(repo *store.Repo) func(ctx context.Context, args json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		var a DetectChangesArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("invalid detect_changes args: %w", err)
		}
		base, head, err := normaliseRefs(a.Base, a.Since, a.Head)
		if err != nil {
			return nil, fmt.Errorf("detect_changes: %w", err)
		}
		if a.Limit < 0 {
			return nil, fmt.Errorf("detect_changes: limit must be >= 0")
		}
		if a.Limit == 0 {
			a.Limit = detectChangesDefaultLimit
		}
		if a.Limit > detectChangesMaxLimit {
			a.Limit = detectChangesMaxLimit
		}

		rctx, cancel := context.WithTimeout(ctx, detectChangesMaxRuntime)
		defer cancel()

		diffOut, err := runGitDiff(rctx, repo.Root(), base, head)
		if err != nil {
			return nil, fmt.Errorf("detect_changes: git diff %s...%s: %w", base, head, err)
		}
		hunks, files := parseUnifiedDiff(diffOut)

		// Build an absolute-path lookup so we can convert git's
		// relative `+++ b/...` paths (which may not be relative to
		// yactt's root if the user runs the tool from outside the
		// repo) back to the path the store indexes under. The repo's
		// Files() returns absolute paths; we key by both the absolute
		// path and the git-relative basename to avoid filesystem I/O
		// for the common single-repo case.
		absByRel := map[string]string{}
		for _, abs := range repo.Files() {
			if rel, rerr := filepath.Rel(repo.Root(), abs); rerr == nil {
				absByRel[rel] = abs
			}
			absByRel[filepath.Base(abs)] = abs
		}

		// Bucket hunks by file → by enclosing symbol. We need both
		// `parser.Symbol` (to drive the scanners) and the stable id
		// (to surface to callers), so we keep them on the bucket
		// rather than re-locating via id after the fact.
		type bucket struct {
			file   string
			sym    parser.Symbol
			symID  string
			ranges []domain.LineRange
		}
		symBuckets := map[string]*bucket{}

		// Track per-file hunks that didn't land in any declaration so
		// we can emit one Change per file when nothing matched. Keyed
		// by absolute path.
		fileOnlyHunks := map[string][]domain.LineRange{}
		fileOrder := []string{}

		for _, h := range hunks {
			absPath, ok := absByRel[h.file]
			if !ok {
				// File not indexed by the store (e.g. inside a
				// directory the walker skipped). Surface as
				// file-only with the relative path we got from git.
				if _, seen := fileOnlyHunks[h.file]; !seen {
					fileOrder = append(fileOrder, h.file)
				}
				fileOnlyHunks[h.file] = append(fileOnlyHunks[h.file], h.rng)
				continue
			}
			sym, symID, ok := enclosingSymbol(repo, absPath, h.rng.Start, h.rng.End)
			if !ok {
				if _, seen := fileOnlyHunks[absPath]; !seen {
					fileOrder = append(fileOrder, absPath)
				}
				fileOnlyHunks[absPath] = append(fileOnlyHunks[absPath], h.rng)
				continue
			}
			b, exists := symBuckets[symID]
			if !exists {
				b = &bucket{file: absPath, sym: sym, symID: symID}
				symBuckets[symID] = b
			}
			b.ranges = append(b.ranges, h.rng)
		}

		// Provenance stamped on every edge the scanners emit and on
		// the top-level envelope. Composed in-process — YacttProvenance
		// (mirrors query_graph / search_code / architecture / graphschema).
		prov := domain.YacttProvenance()

		truncated := false
		changes := []Change{}

		// Emit file-only changes first (sorted by file path), then
		// per-symbol changes. This keeps the deterministic ordering
		// test-friendly without a final sort.
		for _, file := range fileOrder {
			if len(changes) >= detectChangesMaxResults {
				truncated = true
				break
			}
			changes = append(changes, Change{
				File:   file,
				Ranges: mergeRanges(fileOnlyHunks[file]),
			})
		}

		if !truncated {
			// Sort symbol keys so output is deterministic.
			symKeys := make([]string, 0, len(symBuckets))
			for k := range symBuckets {
				symKeys = append(symKeys, k)
			}
			sort.Strings(symKeys)
			for _, symID := range symKeys {
				if len(changes) >= detectChangesMaxResults {
					truncated = true
					break
				}
				b := symBuckets[symID]
				changes = append(changes, Change{
					File:      b.file,
					Ranges:    mergeRanges(b.ranges),
					Symbol:    &SymbolRef{ID: b.symID, Kind: symbolKind(b.sym), Summary: symbolSummary(b.sym)},
					Callers:   scanCallers(repo, b.file, b.sym, a.Limit, &prov),
					Tests:     scanTests(repo, b.file, b.sym, a.Limit, &prov),
					Overrides: scanOverrides(repo, b.file, b.sym, a.Limit, &prov),
				})
			}
		}

		return &DetectChangesResult{
			Changes:    changes,
			Files:      files,
			Base:       base,
			Head:       head,
			Truncated:  truncated,
			Provenance: prov,
		}, nil
	}
}

// normaliseRefs applies the base-vs-since XOR and defaults head="HEAD".
// Returns the resolved (base, head) pair.
func normaliseRefs(base, since, head string) (string, string, error) {
	if since != "" && base != "" {
		return "", "", fmt.Errorf("base and since are mutually exclusive")
	}
	if since != "" {
		base = since
	}
	if base == "" {
		return "", "", fmt.Errorf("base (or since) is required")
	}
	if head == "" {
		head = "HEAD"
	}
	return base, head, nil
}

// runGitDiff shells out to `git diff --unified=0 base...head` and returns
// stdout. The `...` form is symmetric difference — matches cbm's
// detect_changes behaviour. --diff-filter=ACM skips pure deletions (no
// enclosing symbol can be found for a line that no longer exists).
func runGitDiff(ctx context.Context, root, base, head string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git",
		"-C", root,
		"diff",
		"--no-color",
		"--no-ext-diff",
		"--unified=0",
		"--diff-filter=ACM",
		base+"..."+head,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// diffHunk is one parsed @@ hunk header, scoped to its new-side file.
type diffHunk struct {
	file string
	rng  domain.LineRange
}

// hunkRe matches the unified-diff @@ header. The single-line form
// (e.g. `@@ -1 +1 @@`) is supported by leaving the count groups empty
// — strconv.Atoi("") returns 0, which we then treat as "default 1 line".
var hunkRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// parseUnifiedDiff walks git's unified-diff output line-by-line,
// tracking the current new-side file path and extracting every @@
// hunk. Pure deletions (newCount == 0) are skipped — there's no
// new-side line range to map to a symbol.
func parseUnifiedDiff(out []byte) ([]diffHunk, []FileSummary) {
	hunks := []diffHunk{}
	files := []FileSummary{}
	var curFile string
	var cur FileSummary

	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "+++ b/"):
			if curFile != "" {
				files = append(files, cur)
			}
			curFile = strings.TrimPrefix(line, "+++ b/")
			cur = FileSummary{File: curFile}
		case strings.HasPrefix(line, "@@"):
			m := hunkRe.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			newStart, _ := strconv.Atoi(m[3])
			newCount, _ := strconv.Atoi(m[4])
			if newCount == 0 && m[4] == "" {
				// Single-line form (`@@ -2 +2 @@`) — git omits the
				// count when it's 1, but Atoi("") returns 0. Treat
				// the implicit count as 1.
				newCount = 1
			}
			oldCount, _ := strconv.Atoi(m[2])
			if oldCount == 0 && m[2] == "" {
				oldCount = 1
			}
			if newCount == 0 {
				// Pure deletion — skip; nothing maps to a symbol.
				// (Old-count stats are still aggregated below for
				// the file summary.)
				cur.Deleted += oldCount
				continue
			}
			if newStart == 0 {
				newStart = 1
			}
			cur.Hunks++
			cur.Added += newCount
			cur.Deleted += oldCount
			// Convert git's 1-based newStart to the 0-based byte-row
			// convention used by parser.Symbol.StartRow / EndRow
			// (the rest of yactt surfaces 0-based LineRanges — see
			// findcode.go:EnclosingRange for the canonical example).
			start0 := newStart - 1
			end0 := start0 + newCount
			hunks = append(hunks, diffHunk{
				file: curFile,
				rng:  domain.LineRange{Start: start0, End: end0},
			})
		}
	}
	if curFile != "" {
		files = append(files, cur)
	}
	return hunks, files
}

// mergeRanges sorts the slice by start and collapses overlapping or
// adjacent ranges. Caller passes in any order; output is in [start, end)
// form, ascending.
func mergeRanges(rs []domain.LineRange) []domain.LineRange {
	if len(rs) <= 1 {
		return rs
	}
	sort.Slice(rs, func(i, j int) bool {
		if rs[i].Start != rs[j].Start {
			return rs[i].Start < rs[j].Start
		}
		return rs[i].End < rs[j].End
	})
	out := []domain.LineRange{rs[0]}
	for _, r := range rs[1:] {
		last := &out[len(out)-1]
		if r.Start <= last.End {
			if r.End > last.End {
				last.End = r.End
			}
			continue
		}
		out = append(out, r)
	}
	return out
}

// enclosingSymbol returns the smallest parser.Symbol whose [StartRow,
// EndRow) range fully contains [start, end). Returns ("", "", false)
// when no enclosing declaration is found (e.g. a hunk in import-only
// code, a constant block, or a file the parser didn't index).
//
// "Smallest containing" is the right semantics for an edit-impact tool:
// when a hunk lives inside a method inside a class, the method is the
// blast radius, not the class.
func enclosingSymbol(repo *store.Repo, file string, start, end int) (parser.Symbol, string, bool) {
	var best *parser.Symbol
	bestSize := -1
	syms := repo.Symbols(file)
	for i := range syms {
		s := syms[i]
		if int(s.StartRow) <= start && end <= int(s.EndRow) {
			size := int(s.EndRow - s.StartRow)
			if size > bestSize {
				best = &syms[i]
				bestSize = size
			}
		}
	}
	if best == nil {
		return parser.Symbol{}, "", false
	}
	return *best, id.For(*best, packagePath(repo.Root(), file)), true
}