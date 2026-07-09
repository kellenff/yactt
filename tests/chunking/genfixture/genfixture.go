// Package genfixture builds a deterministic synthetic Go repository
// for the chunker benchmark. The fixture is intentionally minimal:
//
//   - 100 source files across 3 packages (auth, payments, users).
//   - ~5 declarations per file (mix of types, methods, top-level funcs).
//   - Doc comments on every declaration so the chunker has signal.
//   - Cross-package calls so the persisted call-edge index has entries.
//   - One large file (~10 small methods) where the char-count baseline
//     loses recall vs. AST-aware chunking.
//
// Regeneration: see tests/chunking/README.md.
package genfixture

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
)

// Seed is the fixed PRNG seed. Changing it changes the fixture
// (and the auto-derived recall_set.jsonl). Pin it so benchmarks are
// reproducible.
const Seed = 0x1AC1AC1A

// Tunables for fixture shape. The defaults meet the issue-34
// success criterion of "100-file repo chunks in ≤10s" and produce
// ~500 declarations.
const (
	NumFiles        = 100
	MethodsPerType  = 3  // average; varies 2-5
	TypesPerFile    = 2  // average; varies 1-4
	LargeFileEveryN = 10 // every Nth file is "large" (one type, many methods)
)

// Domain describes one package's vocabulary. The generator picks a
// domain per file and uses its types/methods/functions as the source
// pool. The three packages the fixture covers share an outer
// module so cross-package calls resolve syntactically.
type Domain struct {
	Pkg       string
	Types     []string // e.g. {"Session", "Token"}
	Methods   []string // e.g. {"Validate", "Refresh"}
	Functions []string // e.g. {"Sanitize", "Normalize"}
}

// Domains is the fixed set of packages the fixture covers. The
// per-file allocator picks a domain by hash so file-to-package
// assignment is deterministic.
var Domains = []Domain{
	{
		Pkg:       "auth",
		Types:     []string{"Session", "Token", "Key", "Login", "Attempt"},
		Methods:   []string{"Validate", "Refresh", "Expire", "Revoke", "Rollback"},
		Functions: []string{"Sanitize", "Normalize", "Aggregate"},
	},
	{
		Pkg:       "payments",
		Types:     []string{"Charge", "Refund", "Invoice", "Order", "Receipt"},
		Methods:   []string{"Process", "Finalize", "Cancel", "Retry"},
		Functions: []string{"Compute", "Reconcile", "Settle"},
	},
	{
		Pkg:       "users",
		Types:     []string{"User", "Account", "Profile", "Preference", "Role"},
		Methods:   []string{"Reset", "Update", "Deactivate", "Promote"},
		Functions: []string{"Lookup", "Search", "Index"},
	},
}

// Symbol is one declaration emitted into the fixture. Returned by
// Write so the caller (the benchmark) can derive a recall set
// without re-parsing the repo.
type Symbol struct {
	ID            string // canonical: fn:auth.Session.Refresh, class:auth.Session, etc.
	QualifiedName string
	Kind          string // FUNCTION | METHOD | CLASS
	Package       string
	File          string
	StartLine     int
	EndLine       int
}

// Write builds the synthetic repo under root. Returns the list of
// every declaration the generator emitted, in source order. The
// list is the seed for the auto-derived recall set; the benchmark
// test pins the questions to these IDs.
func Write(root string) ([]Symbol, error) {
	rng := rand.New(rand.NewSource(Seed))

	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir root: %w", err)
	}
	// go.mod so the store's inferRootPackage returns a non-empty
	// module path (helps the AST chunker produce well-formed
	// canonical IDs even for the per-file allocation).
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/synthetic\n\ngo 1.22\n"), 0o644); err != nil {
		return nil, fmt.Errorf("write go.mod: %w", err)
	}

	var symbols []Symbol

	for i := 0; i < NumFiles; i++ {
		dom := Domains[i%len(Domains)]
		// Use a different file basename per (domain, index) so each
		// file's symbols get unique canonical IDs. The basename
		// also doubles as a noun phrase in the recall set generator.
		base := fmt.Sprintf("%s_%02d.go", dom.Pkg, i/len(Domains))
		rel := filepath.Join(dom.Pkg, base)
		abs := filepath.Join(root, rel)

		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return nil, err
		}

		// Large-file mix: every Nth file has one type with many
		// methods (the case where the baseline char-count chunker
		// combines adjacent small methods in a 1000-char window).
		var nTypes, nMethods int
		if i%LargeFileEveryN == 0 {
			nTypes = 1
			nMethods = 8
		} else {
			nTypes = 1 + rng.Intn(3)                    // 1-3 types
			nMethods = MethodsPerType + rng.Intn(2) - 1 // 2-4 methods/type
		}

		var body strings.Builder
		fmt.Fprintf(&body, "package %s\n\n", dom.Pkg)

		// Pick `nTypes` types and `nMethods` methods per type.
		pickedTypes := pickN(rng, dom.Types, nTypes)
		firstLine := 3 // rough; corrected after generation
		_ = firstLine

		for tIdx, typeName := range pickedTypes {
			fmt.Fprintf(&body, "// %s represents the %s in the %s domain.\n", typeName, strings.ToLower(typeName), dom.Pkg)
			fmt.Fprintf(&body, "type %s struct {\n\tID string\n\tValue int\n}\n\n", typeName)

			// One class-level chunk.
			symbols = append(symbols, Symbol{
				ID:            fmt.Sprintf("class:%s.%s", dom.Pkg, typeName),
				QualifiedName: fmt.Sprintf("%s.%s", dom.Pkg, typeName),
				Kind:          "CLASS",
				Package:       dom.Pkg,
				File:          abs,
				StartLine:     0, // back-filled below
				EndLine:       0,
			})

			methods := pickN(rng, dom.Methods, nMethods)
			for _, mname := range methods {
				fmt.Fprintf(&body, "// %s the %s's %s state.\n", mname, strings.ToLower(typeName), strings.ToLower(mname))
				// Cross-package call: every 3rd method calls a
				// function in one of the other packages. This
				// gives the call-edge index real cross-package
				// entries to surface.
				other := Domains[(tIdx+1)%len(Domains)]
				otherFunc := other.Functions[rng.Intn(len(other.Functions))]
				fmt.Fprintf(&body, "func (r *%s) %s() error {\n", typeName, mname)
				if tIdx%3 == 0 {
					fmt.Fprintf(&body, "\tif err := %s.%s(r.ID); err != nil {\n\t\treturn err\n\t}\n", other.Pkg, otherFunc)
				} else {
					fmt.Fprintf(&body, "\treturn nil\n")
				}
				fmt.Fprintf(&body, "}\n\n")
				symbols = append(symbols, Symbol{
					ID:            fmt.Sprintf("meth:%s.%s.%s", dom.Pkg, typeName, mname),
					QualifiedName: fmt.Sprintf("%s.%s.%s", dom.Pkg, typeName, mname),
					Kind:          "METHOD",
					Package:       dom.Pkg,
					File:          abs,
					StartLine:     0, // back-filled
					EndLine:       0,
				})
			}
		}

		// 50% of files also get a top-level function. This exercises
		// the function policy's per-file filter and the call-edge
		// index for non-method functions.
		if rng.Intn(2) == 0 {
			fnName := dom.Functions[rng.Intn(len(dom.Functions))]
			fmt.Fprintf(&body, "// %s handles the %s pipeline for the %s domain.\n", fnName, strings.ToLower(fnName), dom.Pkg)
			other := Domains[(i+1)%len(Domains)]
			otherFunc := other.Functions[rng.Intn(len(other.Functions))]
			fmt.Fprintf(&body, "func %s(input string) error {\n", fnName)
			fmt.Fprintf(&body, "\tif err := %s.%s(input); err != nil {\n\t\treturn err\n\t}\n", other.Pkg, otherFunc)
			fmt.Fprintf(&body, "\treturn nil\n")
			fmt.Fprintf(&body, "}\n")
			symbols = append(symbols, Symbol{
				ID:            fmt.Sprintf("fn:%s.%s", dom.Pkg, fnName),
				QualifiedName: fmt.Sprintf("%s.%s", dom.Pkg, fnName),
				Kind:          "FUNCTION",
				Package:       dom.Pkg,
				File:          abs,
				StartLine:     0,
				EndLine:       0,
			})
		}

		if err := os.WriteFile(abs, []byte(body.String()), 0o644); err != nil {
			return nil, fmt.Errorf("write %s: %w", rel, err)
		}
	}

	// Back-fill line ranges by re-parsing the files. We re-read so
	// the benchmark can match chunk IDs to source line ranges
	// without re-implementing the tree-sitter walk.
	return fillLineRanges(root, symbols)
}

// pickN returns up to n items from src in random order. Duplicates
// are allowed when n > len(src) — that's rare here (we pick ≤ 8
// from 5-item pools) but the function tolerates it.
func pickN(rng *rand.Rand, src []string, n int) []string {
	if n > len(src) {
		n = len(src)
	}
	pool := append([]string(nil), src...)
	rng.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	return pool[:n]
}

// fillLineRanges re-reads the emitted files and walks the AST to
// pin the line range of each generated symbol. The fixture's
// canonical IDs match the chunker/store's expected shape, so the
// benchmark can use them directly when computing recall.
//
// The walk is a simple line-based scan (find "func ...", "func (r *T) ...",
// "type T struct"). Tree-sitter would be more accurate but this
// fixture is generated, not parsed, so a hand-rolled walk is fine.
func fillLineRanges(root string, syms []Symbol) ([]Symbol, error) {
	// Group by file for one-read-per-file.
	byFile := map[string][]int{}
	for i, s := range syms {
		byFile[s.File] = append(byFile[s.File], i)
	}
	for path, idxs := range byFile {
		bytes, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		lines := strings.Split(string(bytes), "\n")
		for _, i := range idxs {
			s := syms[i]
			s.StartLine, s.EndLine = findRange(lines, s)
			syms[i] = s
		}
	}
	return syms, nil
}

// findRange locates the line range for a generated symbol. The
// generator's templates are uniform enough that substring match is
// reliable: "func (r *T) M" for methods, "func F" for top-level
// functions, "type T struct" for classes.
func findRange(lines []string, s Symbol) (int, int) {
	want := ""
	switch s.Kind {
	case "CLASS":
		want = "type " + strings.SplitN(s.QualifiedName, ".", 2)[1] + " struct"
	case "METHOD":
		recv := strings.SplitN(s.QualifiedName, ".", 3)
		// recv[0]=pkg, recv[1]=Type, recv[2]=Method
		want = "func (r *" + recv[1] + ") " + recv[2]
	case "FUNCTION":
		name := strings.SplitN(s.QualifiedName, ".", 2)[1]
		want = "func " + name + "("
	}
	start := -1
	for i, line := range lines {
		if start < 0 && strings.Contains(line, want) {
			start = i
			continue
		}
		if start >= 0 && !strings.HasPrefix(strings.TrimSpace(lines[i]), "//") {
			// End of the body (next non-comment, non-blank decl).
			// We treat the closing brace row as the end. For
			// generated code the body is always `return ...; }`.
			if strings.HasPrefix(strings.TrimSpace(lines[i]), "}") {
				return start + 1, i + 1 // 1-based, inclusive
			}
		}
	}
	if start < 0 {
		return 0, 0
	}
	return start + 1, len(lines)
}
