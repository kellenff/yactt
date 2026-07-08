package tool

import (
	"sort"
	"strings"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/entity"
	"github.com/kellenff/yactt/internal/parser"
	"github.com/kellenff/yactt/internal/store"
)

// prov returns the per-layer tree-sitter provenance line. Single source of
// truth lives in domain; this is a thin alias for handler call sites.
func prov() *domain.Provenance { return domain.TreeSitterProvenancePtr() }

// packagePath returns the dotted package path for a file under root. Delegates
// to store — single source of truth.
func packagePath(root, p string) string { return store.PackagePath(root, p) }

// relPath returns the repo-relative path for an absolute file path.
func relPath(root, p string) string {
	rel := strings.TrimPrefix(p, root)
	return strings.TrimPrefix(rel, "/")
}

// baseName is the last path element, used for display labels.
func baseName(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// entityFromSymbol is the tool layer's single source of truth for
// lifting a parser.Symbol into an entity.Entity. It builds the entity
// (kind triple + identity) and resolves the receiver via the repo's
// symbol index, so callers get one value with everything pre-computed.
//
// ponytail: the receiver lookup is best-effort (try-with-pkg, then
// without) — unresolvable receivers stay nil and the wire omits the
// receiver field. Don't narrow this until a real consumer hits misses.
func entityFromSymbol(s parser.Symbol, file string, repo *store.Repo) entity.Entity {
	e := entity.FromParser(s, file, packagePath(repo.Root(), file))
	if s.Receiver == "" {
		return e
	}
	pkg := packagePath(repo.Root(), file)
	// Try with the enclosing package first; fall back to a package-less
	// search for TS/JS-style receivers and Go receivers in same-package
	// types. Mirrors findsymbol.go's matchByNamePattern two-pass logic.
	for _, m := range repo.Lookup(pkg, s.Receiver) {
		cand := entity.FromParser(m.Sym, m.File, packagePath(repo.Root(), m.File))
		if cand.DomainKind().IsCode() {
			return *e.WithReceiver(&cand)
		}
	}
	for _, m := range repo.Lookup("", s.Receiver) {
		cand := entity.FromParser(m.Sym, m.File, packagePath(repo.Root(), m.File))
		if cand.DomainKind().IsCode() {
			return *e.WithReceiver(&cand)
		}
	}
	return e
}

// joinDotted combines pkg + name into a dotted id body, dropping empty
// components so we never end up with leading/trailing dots.
func joinDotted(pkg, name string) string {
	if pkg == "" {
		return name
	}
	if name == "" {
		return pkg
	}
	return pkg + "." + name
}

// splitNamePath splits a qualified name into (pkg, name) using the
// rightmost '.' or '/' as the package separator. Mirrors the dotted-Go
// form (auth.Login) and the slash form (auth/Login) so a model trained
// on either gets the same answer. Single source of truth for both
// find_symbol and get_code_snippet.
//
// ponytail: rightmost-wins means a method on a type — `TestUser.Login` —
// parses as pkg="TestUser", name="Login", which is the same ambiguous
// answer the slash form gives today. Don't widen this until a real
// fixture demands multi-segment dotted paths.
func splitNamePath(s string) (pkg, name string) {
	for i := len(s) - 1; i >= 0; i-- {
		if c := s[i]; c == '.' || c == '/' {
			return strings.TrimPrefix(s[:i], "./"), s[i+1:]
		}
	}
	return "", s
}

// suggestNamesMaxScan caps the candidate pool when computing "did you
// mean X" suggestions. Repos above this size pay O(cap) per miss; below
// it they pay O(index size). 10k symbols is ~5x the largest real-world
// repo yactt has indexed in tests, so the constant is comfortable.
const suggestNamesMaxScan = 10_000

// suggestNamesThreshold is the edit-distance cutoff. 2 catches one
// transposed/missing/extra character in a typical identifier (the classic
// `Loggin` → `Login` case) without flooding the agent with low-quality
// matches on shorter names.
const suggestNamesThreshold = 2

// suggestNames returns up to k symbol names from the repo index whose
// last-segment name is within edit-distance suggestNamesThreshold of
// `name`. The candidate pool is bounded by suggestNamesMaxScan.
//
// Order: ascending edit-distance, then alphabetical for ties.
//
// Used by find_symbol / get_code_snippet on miss to give the agent a
// recovery hint instead of a silent `{symbols: []}` / error.
//
// ponytail: edit-distance over the *index names* (not the full string
// set) is the right shape — the index is already built at Load time,
// so this is a bounded scan over typed data. We don't try fuzzy match
// against file paths or full name-paths; symbol names are what an
// agent types when it misspells.
func suggestNames(repo *store.Repo, name string, k int) []string {
	if name == "" || k <= 0 {
		return nil
	}
	// Use just the last path segment of `name` (splitNamePath-style)
	// so suggestions reflect what the agent actually typed.
	_, base := splitNamePath(name)
	if base == "" {
		return nil
	}
	candidates := repo.Lookup("", "")
	if len(candidates) > suggestNamesMaxScan {
		candidates = candidates[:suggestNamesMaxScan]
	}
	type scored struct {
		name  string
		score int
	}
	seen := make(map[string]bool)
	out := make([]scored, 0, k*2)
	for _, c := range candidates {
		if c.Sym.Name == "" {
			continue
		}
		if seen[c.Sym.Name] {
			continue
		}
		seen[c.Sym.Name] = true
		d := editDistance(base, c.Sym.Name, suggestNamesThreshold)
		if d <= suggestNamesThreshold {
			out = append(out, scored{c.Sym.Name, d})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score < out[j].score
		}
		return out[i].name < out[j].name
	})
	if len(out) > k {
		out = out[:k]
	}
	if len(out) == 0 {
		return nil
	}
	names := make([]string, len(out))
	for i, s := range out {
		names[i] = s.name
	}
	return names
}

// editDistance is the Levenshtein distance between a and b, with an
// early-exit when the running minimum already exceeds `ceiling`. Returns
// ceiling + 1 when the distance is known to exceed ceiling.
//
// Standard 2-row DP — O(len(a) * len(b)) time, O(min(len(a), len(b)))
// memory. No external dep.
//
// ponytail: ceiling-based early exit keeps this safe on long strings
// (we never enumerate a full table for two 200-char strings when we
// only care about distance ≤ 2). The constant `2` on suggestNamesThreshold
// is what makes this hot — almost every suggestion finishes after
// scanning the first 2-3 columns.
func editDistance(a, b string, ceiling int) int {
	if a == b {
		return 0
	}
	la, lb := len(a), len(b)
	if la == 0 {
		if lb > ceiling {
			return ceiling + 1
		}
		return lb
	}
	if lb == 0 {
		if la > ceiling {
			return ceiling + 1
		}
		return la
	}
	if absInt(la-lb) > ceiling {
		return ceiling + 1
	}
	// Ensure b is the shorter string — keeps the DP row small.
	if la < lb {
		a, b = b, a
		la, lb = lb, la
	}
	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr[0] = i
		rowMin := curr[0]
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			// Three transitions:
			//   - delete a[i-1]:   prev[j] + 1
			//   - insert b[j-1]:   curr[j-1] + 1
			//   - substitute:      prev[j-1] + (0 if match else 1)
			del := prev[j] + 1
			ins := curr[j-1] + 1
			sub := prev[j-1] + cost
			v := del
			if ins < v {
				v = ins
			}
			if sub < v {
				v = sub
			}
			curr[j] = v
			if v < rowMin {
				rowMin = v
			}
		}
		if rowMin > ceiling {
			return ceiling + 1
		}
		prev, curr = curr, prev
	}
	if prev[lb] > ceiling {
		return ceiling + 1
	}
	return prev[lb]
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}