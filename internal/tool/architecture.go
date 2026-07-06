package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/parser"
	"github.com/kellenff/yactt/internal/store"
)

// GetArchitectureArgs is the typed boundary input for get_architecture.
// `Top` controls the hotspot and package-list cap (default 10). Set
// `include_cycles` to false to skip the Tarjan SCC pass on very large
// repos — it scales linearly with the file count, but a 50k-file tree
// can still take a few hundred ms to walk.
//
// `include_cycles` defaults to true when omitted. We use a pointer so
// the handler can tell "caller said false" from "caller didn't say".
type GetArchitectureArgs struct {
	Top           int   `json:"top"`
	IncludeCycles *bool `json:"include_cycles"`
}

// ArchitectureResult is the structuredContent envelope for get_architecture.
// Ponytail: every field is built from one pass over `repo.Files()` + the
// existing resolver indices. No new index, no async work, no LLM calls.
type ArchitectureResult struct {
	RootPackage       string                  `json:"rootPackage"`
	Summary           ArchSummary             `json:"summary"`
	Languages         []ArchLanguage          `json:"languages"`
	TopPackages       []ArchPackage           `json:"topPackages"`
	Hotspots          []ArchHotspot           `json:"hotspots"`
	DeadCode          []ArchDeadCode          `json:"deadCode"`
	ImportCycles      []ArchCycle             `json:"importCycles"`
	Provenance        domain.Provenance       `json:"provenance"`
}

// ArchSummary is the per-kind count roll-up. All counts are over the
// top-level declarations (`SymbolsByPath`); nested declarations are not
// double-counted.
type ArchSummary struct {
	FileCount       int `json:"fileCount"`
	PackageCount    int `json:"packageCount"`
	FunctionCount   int `json:"functionCount"`
	MethodCount     int `json:"methodCount"`
	ClassCount      int `json:"classCount"`
	ModuleCount     int `json:"moduleCount"`
}

// ArchLanguage describes one language detected in the index.
type ArchLanguage struct {
	Name      string `json:"name"`
	FileCount int    `json:"fileCount"`
}

// ArchPackage is one entry in the "biggest packages by file count" list.
type ArchPackage struct {
	Name      string `json:"name"`
	FileCount int    `json:"fileCount"`
}

// ArchHotspot is one entry in the "most-called symbols" ranking. Score is
// the number of distinct call sites pointing at the symbol.
type ArchHotspot struct {
	ID     string          `json:"id"`
	Kind   domain.NodeKind `json:"kind"`
	Name   string          `json:"name"`
	File   string          `json:"file"`
	Caller int             `json:"callerCount"`
}

// ArchDeadCode is one symbol with zero callers. For Go, exported symbols
// (uppercase first letter) are filtered out — they may be part of the
// public API and a non-caller call site isn't a "dead" finding.
type ArchDeadCode struct {
	ID       string          `json:"id"`
	Kind     domain.NodeKind `json:"kind"`
	Name     string          `json:"name"`
	File     string          `json:"file"`
	Exported bool            `json:"exported"`
}

// ArchCycle is one strongly-connected component in the file-level import
// graph with size > 1 (or a self-loop with size 1). Files inside the cycle
// are listed in stable path-sorted order.
type ArchCycle struct {
	Files []string `json:"files"`
}

// GetArchitectureSchema is the JSON Schema for get_architecture.
var GetArchitectureSchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "top":            { "type": "integer", "minimum": 1, "maximum": 100, "default": 10 },
    "include_cycles": { "type": "boolean", "default": true }
  },
  "additionalProperties": false
}`)

// GetArchitectureOutputSchema declares the structuredContent shape. All
// sections are always present (possibly empty) so clients can render the
// answer without nil checks.
var GetArchitectureOutputSchema = json.RawMessage(`{
  "type": "object",
  "required": ["summary", "languages", "topPackages", "hotspots", "deadCode", "importCycles", "provenance"],
  "properties": {
    "rootPackage":  { "type": "string" },
    "summary":      { "type": "object" },
    "languages":    { "type": "array" },
    "topPackages":  { "type": "array" },
    "hotspots":     { "type": "array" },
    "deadCode":     { "type": "array" },
    "importCycles": { "type": "array" },
    "provenance":   { "type": "object" }
  },
  "additionalProperties": false
}`)

// Architecture cycle / cap defaults.
const (
	defaultArchTop     = 10
	maxDeadCodeEntries = 50
	maxCycleFiles      = 20 // per cycle, in path-sorted order
	maxCyclesReturned  = 10
)

// GetArchitecture returns a Handler that emits a structural summary.
// Reads walk `Files()` + the persisted call-edge / imports indices once,
// then runs an in-tree Tarjan SCC pass when cycles are requested.
func GetArchitecture(repo *store.Repo) func(ctx context.Context, args json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		var a GetArchitectureArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("invalid get_architecture args: %w", err)
		}
		if a.Top <= 0 {
			a.Top = defaultArchTop
		}
		if a.Top > 100 {
			a.Top = 100
		}
		// Default IncludeCycles to true — the SCC pass is bounded by file
		// count and cheap on fixture-scale repos. Callers opt out by
		// explicitly setting `include_cycles: false`.
		includeCycles := true
		if a.IncludeCycles != nil {
			includeCycles = *a.IncludeCycles
		}

		result := &ArchitectureResult{
			RootPackage: repo.RootPackage(),
			Provenance:  *prov(),
			// Always non-nil empty slices so clients don't need to nil-check
			// per section.
			ImportCycles: []ArchCycle{},
		}
		result.Summary, result.Languages, result.TopPackages = countsAndPackages(repo)
		result.Hotspots, result.DeadCode = hotspotsAndDeadCode(repo, a.Top)
		if includeCycles {
			result.ImportCycles = importCycles(repo)
		}
		return result, nil
	}
}

// countsAndPackages is one pass over repo.Files() + SymbolsByPath() that
// produces the summary roll-up, language breakdown, and top packages.
func countsAndPackages(repo *store.Repo) (ArchSummary, []ArchLanguage, []ArchPackage) {
	files := repo.Files()
	symsByPath := repo.SymbolsByPath()

	sum := ArchSummary{
		FileCount: len(files),
	}
	packageSet := map[string]struct{}{}
	packageCount := map[string]int{}
	langSet := map[parser.Name]struct{}{}
	langCount := map[parser.Name]int{}

	for _, f := range files {
		// Package roll-up
		pkg := store.PackagePath(repo.Root(), f)
		if pkg != "" {
			packageSet[pkg] = struct{}{}
			packageCount[pkg]++
		}
		// Language detection (per-file, via the existing parser registry).
		// Skip files we don't know — those contribute to fileCount but not
		// to any language bucket.
		if lang, err := parser.Detect(f); err == nil {
			langSet[lang.Name()] = struct{}{}
			langCount[lang.Name()]++
		}
	}
	sum.PackageCount = len(packageSet)

	// Symbol counts. `SymbolsByPath` returns top-level declarations per file.
	for _, syms := range symsByPath {
		for _, s := range syms {
			switch parser.SymbolKind(s) {
			case domain.KindFunction:
				sum.FunctionCount++
			case domain.KindMethod:
				sum.MethodCount++
			case domain.KindClass:
				sum.ClassCount++
			case domain.KindModule:
				sum.ModuleCount++
			}
		}
	}

	// Languages, sorted by file_count desc.
	langs := make([]ArchLanguage, 0, len(langSet))
	for name := range langSet {
		langs = append(langs, ArchLanguage{Name: string(name), FileCount: langCount[name]})
	}
	sort.Slice(langs, func(i, j int) bool {
		if langs[i].FileCount != langs[j].FileCount {
			return langs[i].FileCount > langs[j].FileCount
		}
		return langs[i].Name < langs[j].Name
	})

	pkgs := sortedPackages(packageCount, defaultArchTop)
	return sum, langs, pkgs
}

func sortedPackages(counts map[string]int, top int) []ArchPackage {
	out := make([]ArchPackage, 0, len(counts))
	for name, n := range counts {
		out = append(out, ArchPackage{Name: name, FileCount: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].FileCount != out[j].FileCount {
			return out[i].FileCount > out[j].FileCount
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > top {
		out = out[:top]
	}
	return out
}

// hotspotsAndDeadCode walks every symbol once and splits them by caller
// count. Hotspots = top-N by caller count. Dead = zero callers (Go
// exported-names filtered).
//
// Single pass per file: the persisted call-edge index keys by callee name,
// so we count `EdgesByCaller(file, sym)` for the candidate and use the
// length as the score.
func hotspotsAndDeadCode(repo *store.Repo, top int) ([]ArchHotspot, []ArchDeadCode) {
	type cand struct {
		id     string
		kind   domain.NodeKind
		name   string
		file   string
		caller int
	}
	var all []cand

	for file, syms := range repo.SymbolsByPath() {
		for _, s := range syms {
			kind := parser.SymbolKind(s)
			if !kind.IsCode() {
				continue
			}
			callers := len(repo.EdgesByCaller(file, s))
			all = append(all, cand{
				id:     symbolID(file, s, repo.Root()),
				kind:   kind,
				name:   s.Name,
				file:   file,
				caller: callers,
			})
		}
	}

	// Hotspots: descending by caller count. Ties broken by id for stable output.
	hot := make([]cand, len(all))
	copy(hot, all)
	sort.Slice(hot, func(i, j int) bool {
		if hot[i].caller != hot[j].caller {
			return hot[i].caller > hot[j].caller
		}
		return hot[i].id < hot[j].id
	})
	hotspots := make([]ArchHotspot, 0, top)
	for _, c := range hot {
		if c.caller == 0 {
			break // sorted desc — first zero is the boundary
		}
		hotspots = append(hotspots, ArchHotspot{
			ID: c.id, Kind: c.kind, Name: c.name, File: c.file, Caller: c.caller,
		})
		if len(hotspots) >= top {
			break
		}
	}

	// Dead-code: callers == 0, filtered for Go exported.
	var dead []ArchDeadCode
	for _, c := range all {
		if c.caller != 0 {
			continue
		}
		exported := isExportedGo(c.name)
		lang := langForFile(fileExt(c.file))
		if lang == parser.LangGo && exported {
			continue
		}
		dead = append(dead, ArchDeadCode{
			ID: c.id, Kind: c.kind, Name: c.name, File: c.file, Exported: exported,
		})
		if len(dead) >= maxDeadCodeEntries {
			break
		}
	}
	sort.Slice(dead, func(i, j int) bool { return dead[i].ID < dead[j].ID })

	// Convert hotspots to []ArchHotspot preserving ordered result.
	hs := make([]ArchHotspot, len(hotspots))
	copy(hs, hotspots)
	return hs, dead
}

// importCycles builds a file-level import graph and runs Tarjan SCC.
// Returns strongly-connected components of size > 1 (true multi-node
// cycles), plus size-1 SCCs that have a self-loop (file imports its own
// package — also a cycle per Go semantics).
//
// The graph uses file paths as nodes; the import resolution is package-
// level (each file imports a package; we map that package back to a
// representative file via `PackagePath`). This is deliberately simple —
// only local (intra-repo) imports get edges. External imports land in
// no node and contribute nothing.
func importCycles(repo *store.Repo) []ArchCycle {
	files := repo.Files()
	// Map package path -> any one file in that package (representative).
	pkgRep := map[string]string{}
	for _, f := range files {
		p := store.PackagePath(repo.Root(), f)
		if _, ok := pkgRep[p]; !ok {
			pkgRep[p] = f
		}
	}

	// Adjacency list keyed by *file*. Value is the set of distinct files
	// that file imports.
	adj := map[string]map[string]bool{}
	for _, f := range files {
		imports := repo.ImportsIn(f)
		if len(imports) == 0 {
			continue
		}
		out := map[string]bool{}
		for _, imp := range imports {
			// External / unrecognized imports contribute no edges.
			target, ok := resolveImport(repo, pkgRep, f, imp.Path)
			if !ok {
				continue
			}
			out[target] = true
		}
		if len(out) > 0 {
			adj[f] = out
		}
	}

	sccs := tarjanSCC(files, adj)
	cycles := make([]ArchCycle, 0, len(sccs))
	for _, comp := range sccs {
		isCycle := len(comp) > 1
		if !isCycle && len(comp) == 1 {
			f := comp[0]
			if adj[f][f] {
				isCycle = true
			}
		}
		if !isCycle {
			continue
		}
		// Stable ordering: sort files in the component.
		sorted := append([]string(nil), comp...)
		sort.Strings(sorted)
		if len(sorted) > maxCycleFiles {
			sorted = sorted[:maxCycleFiles]
		}
		cycles = append(cycles, ArchCycle{Files: sorted})
		if len(cycles) >= maxCyclesReturned {
			break
		}
	}
	return cycles
}

// resolveImport maps an ImportEntry path string back to a file in
// `pkgRep`. Strategy:
//
//   - Exact package-path match.
//   - Suffix match: file whose package path ends with "/<import>".
//   - Relative Go import ("." or "./x"): resolve relative to the
//     importing file's directory, look up by `PackagePath`.
//
// Returns the file's absolute path and ok=true on hit; ( "", false ) on
// miss. Pure local-resolution — external Go modules land here as miss.
func resolveImport(repo *store.Repo, pkgRep map[string]string, importingFile, importPath string) (string, bool) {
	if importPath == "" {
		return "", false
	}
	// Exact match.
	if f, ok := pkgRep[importPath]; ok {
		return f, true
	}
	// Suffix match: any pkg ending with "/<importPath>" or ".<importPath>".
	for pkg, f := range pkgRep {
		if strings.HasSuffix(pkg, "/"+importPath) || strings.HasSuffix(pkg, "."+importPath) {
			return f, true
		}
	}
	// Relative go import: "./x" or "../x". Build an absolute path,
	// match against `pkgRep` keys that share the prefix.
	if strings.HasPrefix(importPath, "./") || strings.HasPrefix(importPath, "../") {
		base := filepath.Dir(importingFile)
		rel := filepath.Join(base, importPath)
		rel = filepath.Clean(rel)
		pkg := store.PackagePath(repo.Root(), rel)
		if f, ok := pkgRep[pkg]; ok {
			return f, true
		}
	}
	return "", false
}

// Tarjan's SCC — iterative recursion is fine here; graph is small.
func tarjanSCC(nodes []string, adj map[string]map[string]bool) [][]string {
	type frame struct {
		node   string
		it     int
		children []string
	}
	indexOf := map[string]int{}
	lowlink := map[string]int{}
	onStack := map[string]bool{}
	var stack []string
	var sccs [][]string
	idx := 0
	// Order the nodes deterministically (sorted) so output is stable.
	sortedNodes := append([]string(nil), nodes...)
	sort.Strings(sortedNodes)

	for _, n := range sortedNodes {
		if _, ok := indexOf[n]; ok {
			continue
		}
		// Iterative DFS using a work stack.
		callStack := []frame{{node: n, children: keysSorted(adj[n])}}
		indexOf[n] = idx
		lowlink[n] = idx
		idx++
		stack = append(stack, n)
		onStack[n] = true
		for len(callStack) > 0 {
			top := &callStack[len(callStack)-1]
			if top.it < len(top.children) {
				w := top.children[top.it]
				top.it++
				if _, seen := indexOf[w]; !seen {
					indexOf[w] = idx
					lowlink[w] = idx
					idx++
					stack = append(stack, w)
					onStack[w] = true
					callStack = append(callStack, frame{node: w, children: keysSorted(adj[w])})
				} else if onStack[w] {
					if indexOf[w] < lowlink[top.node] {
						lowlink[top.node] = indexOf[w]
					}
				}
			} else {
				// Post-order: pop and report SCC.
				v := top.node
				if lowlink[v] == indexOf[v] {
					var comp []string
					for {
						w := stack[len(stack)-1]
						stack = stack[:len(stack)-1]
						onStack[w] = false
						comp = append(comp, w)
						if w == v {
							break
						}
					}
					sort.Strings(comp)
					sccs = append(sccs, comp)
				}
				callStack = callStack[:len(callStack)-1]
				if len(callStack) > 0 {
					parent := callStack[len(callStack)-1].node
					if lowlink[v] < lowlink[parent] {
						lowlink[parent] = lowlink[v]
					}
				}
			}
		}
	}
	return sccs
}

func keysSorted(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// isExportedGo reports whether `name` is exported per Go convention
// (uppercase first letter). Used to filter false positives from the
// dead-code list.
func isExportedGo(name string) bool {
	if name == "" {
		return false
	}
	c := name[0]
	return c >= 'A' && c <= 'Z'
}

// fileExt returns the lowercased file extension (no leading dot).
// Empty string for files with no extension.
func fileExt(path string) string {
	ext := filepath.Ext(path)
	if ext == "" {
		return ""
	}
	return strings.ToLower(ext[1:])
}

// langForFile maps an extension to a parser.Name. Empty string when the
// extension is unsupported — cycles/dead-code then skip the Go-specific
// exported-name heuristic.
func langForFile(ext string) parser.Name {
	switch ext {
	case "go":
		return parser.LangGo
	case "ts", "tsx":
		return parser.LangTypeScript
	case "js", "jsx":
		return parser.LangJavaScript
	}
	return ""
}