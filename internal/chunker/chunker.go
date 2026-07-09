// Package chunker turns a parsed store.Repo into AST-bounded chunks
// suitable for vector-store ingest. See issue #34.
//
// One chunk per function/method (PolicyFunction, default), per class
// (PolicyClass), or per file (PolicyModule). Output is NDJSON: one
// chunk per line, terminator "\n", no trailing summary. The package
// is a leaf layer over the existing store/entity/parser stacks — no
// new parsing, no I/O of its own beyond the writer the caller hands
// Run, no embedding generation, no MCP server.
//
// The chunker does not cache and does not mutate the repo. Run is
// safe to call repeatedly with the same arguments; the result is
// byte-deterministic for a given (commit, policy, options) tuple.
package chunker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/parser"
	"github.com/kellenff/yactt/internal/source"
	"github.com/kellenff/yactt/internal/store"
)

// Policy selects the granularity of emitted chunks.
type Policy string

const (
	// PolicyFunction: one chunk per function/method body. Default.
	PolicyFunction Policy = "function"
	// PolicyClass: one chunk per class; methods listed by signature.
	PolicyClass Policy = "class"
	// PolicyModule: one chunk per file.
	PolicyModule Policy = "module"
)

// Chunk is the on-the-wire shape. Field names are stable; the
// LangChain/LlamaIndex adapters (deferred, see issue #34 plan) read
// them directly. JSON tags are the contract.
type Chunk struct {
	ID            string   `json:"id"`
	QualifiedName string   `json:"qualified_name"`
	Kind          string   `json:"kind"`
	Language      string   `json:"language"`
	File          string   `json:"file"`
	StartLine     int      `json:"start_line"` // 1-based, inclusive
	EndLine       int      `json:"end_line"`   // 1-based, inclusive
	Signature     string   `json:"signature"`
	Text          string   `json:"text"`
	Callers       []string `json:"callers"`
	Callees       []string `json:"callees"`
	Policy        Policy   `json:"policy"`
}

// Options tunes a single Run call. The zero value is valid and means
// "everything, function-level, no test files".
type Options struct {
	// Policy selects granularity. Empty == PolicyFunction.
	Policy Policy

	// Languages filters to the given parser.Name list (e.g. "go",
	// "typescript"). Empty == all wired languages.
	Languages []parser.Name

	// Include/Exclude are filepath.Match globs evaluated against the
	// path *relative to the repo root*. Exclude is applied after Include.
	// Empty slices mean "no filter on that side". Test files are hidden
	// by default; pass WithTests=true to include them.
	Include []string
	Exclude []string

	// WithTests includes *_test.go (and per-language equivalents) files.
	// Default false — test files bloat recall sets with redundant impls.
	WithTests bool
}

// Run walks the repo once and writes one NDJSON line per chunk to w.
// Returns the number of chunks emitted and the first non-fatal error.
// A write error short-circuits and is returned.
//
// ctx is honored between files; cancellation between symbols within a
// file is not (chunker is short-horizon and per-file work is bounded).
func Run(ctx context.Context, r *store.Repo, opts Options, w io.Writer) (int, error) {
	if r == nil {
		return 0, fmt.Errorf("chunker: nil repo")
	}
	if w == nil {
		return 0, fmt.Errorf("chunker: nil writer")
	}
	if opts.Policy == "" {
		opts.Policy = PolicyFunction
	}
	enc := json.NewEncoder(w)
	// Default behavior of json.Encoder escapes HTML (& -> \u0026 etc).
	// Chunks contain source code and may legitimately contain "&" in
	// generic type parameters; disabling the escape keeps the wire
	// output byte-identical to the source.
	enc.SetEscapeHTML(false)

	langs := make(map[parser.Name]bool, len(opts.Languages))
	for _, l := range opts.Languages {
		langs[l] = true
	}
	filterLanguages := len(langs) > 0

	// Sort files for deterministic output: store.Files() is map-
	// iteration order and would otherwise produce a different chunk
	// sequence on every call.
	files := r.Files()
	sort.Strings(files)

	count := 0
	for _, path := range files {
		if err := ctx.Err(); err != nil {
			return count, err
		}
		if !shouldInclude(path, r.Root(), opts) {
			continue
		}
		f, err := r.CachedFile(path)
		if err != nil || f == nil {
			continue
		}
		if filterLanguages && !langs[f.Grammar.Name()] {
			continue
		}

		if opts.Policy == PolicyModule {
			c, ok := moduleChunk(r, f)
			if !ok {
				continue
			}
			if err := enc.Encode(c); err != nil {
				return count, fmt.Errorf("chunker: encode: %w", err)
			}
			count++
			continue
		}

		// Sort symbols for determinism: by (startRow, name) so the
		// emitted order matches source top-to-bottom.
		syms := append([]parser.Symbol(nil), r.Symbols(path)...)
		sort.Slice(syms, func(i, j int) bool {
			if syms[i].StartRow != syms[j].StartRow {
				return syms[i].StartRow < syms[j].StartRow
			}
			return syms[i].Name < syms[j].Name
		})

		for _, s := range syms {
			var (
				c   Chunk
				err error
			)
			switch opts.Policy {
			case PolicyFunction:
				if !isFunctionLike(s) {
					continue
				}
				c, err = functionChunk(r, f, s)
			case PolicyClass:
				if !isClassLike(s) {
					continue
				}
				c, err = classChunk(r, f, s, syms)
			default:
				return count, fmt.Errorf("chunker: unknown policy %q", opts.Policy)
			}
			if err != nil {
				// Skip the chunk but keep going; partial output is
				// more useful than no output for an ingest pipeline.
				continue
			}
			if err := enc.Encode(c); err != nil {
				return count, fmt.Errorf("chunker: encode: %w", err)
			}
			count++
		}
	}
	return count, nil
}

// shouldInclude applies Include/Exclude/WithTests filters. Paths are
// matched relative to the repo root. An empty Include means "include
// everything"; an empty Exclude means "exclude nothing" (except
// test files when WithTests is false).
func shouldInclude(path, root string, opts Options) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = path
	}
	if len(opts.Include) > 0 && !matchAny(rel, opts.Include) {
		return false
	}
	if matchAny(rel, opts.Exclude) {
		return false
	}
	if !opts.WithTests && isTestFile(rel) {
		return false
	}
	return true
}

func matchAny(rel string, globs []string) bool {
	if len(globs) == 0 {
		return false
	}
	for _, g := range globs {
		// filepath.Match returns false for ErrBadPattern, which is what
		// we want (the glob doesn't match, move on).
		if ok, _ := filepath.Match(g, rel); ok {
			return true
		}
		// Also try the base name — users often write "*.go" and mean
		// "any file ending in .go anywhere", not "any file in a top-
		// level .go" (which would only match literal "./<name>.go").
		if ok, _ := filepath.Match(g, filepath.Base(rel)); ok {
			return true
		}
	}
	return false
}

// isTestFile returns true for the per-language test-file conventions.
// We rely on filename suffix; the chunker is conservative — false
// positives are fine (we'd over-include), false negatives lose chunks
// silently.
func isTestFile(rel string) bool {
	base := filepath.Base(rel)
	switch {
	case strings.HasSuffix(base, "_test.go"),
		strings.HasSuffix(base, ".test.ts"),
		strings.HasSuffix(base, ".test.tsx"),
		strings.HasSuffix(base, ".test.js"),
		strings.HasSuffix(base, ".test.jsx"),
		strings.HasSuffix(base, ".spec.ts"),
		strings.HasSuffix(base, ".spec.tsx"),
		strings.HasSuffix(base, ".spec.js"),
		strings.HasSuffix(base, ".spec.jsx"),
		strings.HasSuffix(base, "_test.py"),
		strings.HasSuffix(base, "test.rs"),
		strings.HasSuffix(base, "Test.php"):
		return true
	}
	return false
}

// isFunctionLike reports whether the grammar kind is a function or
// method declaration. Matches parser.SymbolKind's FUNCTION/METHOD
// buckets.
func isFunctionLike(s parser.Symbol) bool {
	return s.Kind == "function_declaration" || s.Kind == "method_declaration"
}

// isClassLike reports whether the grammar kind is a class-shaped
// declaration. Matches parser.SymbolKind's CLASS bucket — broad on
// purpose: type_declaration, class_declaration, struct_declaration,
// interface_declaration, type_alias_declaration, enum_declaration,
// trait_declaration all fold into CLASS.
func isClassLike(s parser.Symbol) bool {
	switch s.Kind {
	case "type_declaration", "class_declaration",
		"interface_declaration", "type_alias_declaration",
		"enum_declaration", "struct_declaration",
		"trait_declaration":
		return true
	}
	return false
}

// functionChunk builds the per-function (or per-method) chunk. Text
// is the symbol body; signature is the first line of the declaration
// (prepended with the doc comment when one exists).
func functionChunk(r *store.Repo, f *source.File, sym parser.Symbol) (Chunk, error) {
	body, err := sliceBody(r, f, sym)
	if err != nil {
		return Chunk{}, err
	}
	doc := r.DocComment(f.Path, sym)
	sig := signatureLine(r.Signature(f.Path, sym), body)
	qualifiedName := qualifiedName(r, f.Path, sym)
	id, err := r.SymbolID(f.Path, sym)
	if err != nil {
		return Chunk{}, err
	}
	kind := string(parser.SymbolKind(sym))
	if kind == "FUNCTION" && sym.Receiver != "" {
		kind = "METHOD"
	}
	return Chunk{
		ID:            id,
		QualifiedName: qualifiedName,
		Kind:          kind,
		Language:      languageFor(f),
		File:          f.Path,
		StartLine:     sym.StartRow + 1, // 0-based row -> 1-based line
		EndLine:       sym.EndRow,       // EndRow is exclusive in tree-sitter
		Signature:     signatureWithDoc(doc, sig),
		Text:          body,
		Callers:       resolveCallers(r, sym.Name),
		Callees:       resolveCallees(r, f.Path, sym),
		Policy:        PolicyFunction,
	}, nil
}

// classChunk builds a per-class chunk. Text is the class block plus
// a "Methods:" section listing method signatures in declaration
// order. Callers/Callees are the union over the class's methods
// (deduped).
func classChunk(r *store.Repo, f *source.File, sym parser.Symbol, allSyms []parser.Symbol) (Chunk, error) {
	body, err := sliceBody(r, f, sym)
	if err != nil {
		return Chunk{}, err
	}
	doc := r.DocComment(f.Path, sym)
	sig := firstLine(body)
	if sig == "" {
		sig = "type " + sym.Name
	}
	qualifiedName := qualifiedName(r, f.Path, sym)
	id, err := r.SymbolID(f.Path, sym)
	if err != nil {
		return Chunk{}, err
	}
	var methodSigs []string
	callees := map[string]struct{}{}
	callers := map[string]struct{}{}
	for _, m := range allSyms {
		if m.Kind != "method_declaration" {
			continue
		}
		if m.Receiver != sym.Name {
			continue
		}
		// Methods usually follow the class block in source order, not
		// inside its row range. Constrain by file (already true here)
		// and a StartRow >= the class's StartRow; the receiver-name
		// check above is what actually disambiguates same-file
		// same-named methods on different types.
		if m.StartRow < sym.StartRow {
			continue
		}
		mBody, _ := sliceBody(r, f, m)
		mSig := signatureLine(r.Signature(f.Path, m), mBody)
		if mSig == "" {
			mSig = "func " + sym.Name + "." + m.Name + "(...)"
		}
		methodSigs = append(methodSigs, mSig)
		for _, c := range resolveCallees(r, f.Path, m) {
			callees[c] = struct{}{}
		}
		for _, c := range resolveCallers(r, m.Name) {
			callers[c] = struct{}{}
		}
	}
	text := body
	if len(methodSigs) > 0 {
		text = strings.TrimRight(body, "\n") + "\n\nMethods:\n" + strings.Join(methodSigs, "\n")
	}
	return Chunk{
		ID:            id,
		QualifiedName: qualifiedName,
		Kind:          string(parser.SymbolKind(sym)),
		Language:      languageFor(f),
		File:          f.Path,
		StartLine:     sym.StartRow + 1,
		EndLine:       sym.EndRow,
		Signature:     signatureWithDoc(doc, sig),
		Text:          text,
		Callers:       sortedKeys(callers),
		Callees:       sortedKeys(callees),
		Policy:        PolicyClass,
	}, nil
}

// moduleChunk builds a per-file chunk. The full file bytes become
// the text; callers/callees are the union over the file's symbols.
func moduleChunk(r *store.Repo, f *source.File) (Chunk, bool) {
	body, err := f.Slice(domain.LineRange{Start: 0, End: f.LineCount()})
	if err != nil {
		return Chunk{}, false
	}
	pkg := r.PackageOf(f.Path)
	rel, _ := filepath.Rel(r.Root(), f.Path)
	id := "module:" + joinDotted(pkg, rel)
	qualifiedName := joinDotted(pkg, rel)
	if qualifiedName == "" {
		qualifiedName = rel
	}
	callees := map[string]struct{}{}
	callers := map[string]struct{}{}
	for _, s := range r.Symbols(f.Path) {
		for _, c := range resolveCallees(r, f.Path, s) {
			callees[c] = struct{}{}
		}
		for _, c := range resolveCallers(r, s.Name) {
			callers[c] = struct{}{}
		}
	}
	return Chunk{
		ID:            id,
		QualifiedName: qualifiedName,
		Kind:          "MODULE",
		Language:      languageFor(f),
		File:          f.Path,
		StartLine:     1,
		EndLine:       f.LineCount(),
		Signature:     rel,
		Text:          body,
		Callers:       sortedKeys(callers),
		Callees:       sortedKeys(callees),
		Policy:        PolicyModule,
	}, true
}

// resolveCallers looks up callers via the persisted call-edge index.
// Returns canonical IDs (entity.Entity.ID() form) when resolvable.
// Empty slice when there are none or the index is empty.
func resolveCallers(r *store.Repo, name string) []string {
	if name == "" {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	for _, e := range r.EdgesByCallee(name) {
		if e.Caller.Name == "" {
			continue
		}
		id, err := r.SymbolID(e.File, e.Caller)
		if err != nil {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// resolveCallees looks up the callees for a given caller symbol. Each
// edge's Callee is a bare name; the chunker disambiguates by
// preferring the symbol in the same file as the caller, then any
// package match, then synthesises a fn:<pkg>.<name> ID as a last
// resort so the wire shape stays parseable.
func resolveCallees(r *store.Repo, callerFile string, sym parser.Symbol) []string {
	if sym.Name == "" {
		return nil
	}
	callerPkg := r.PackageOf(callerFile)
	seen := map[string]struct{}{}
	var out []string
	for _, e := range r.EdgesByCaller(callerFile, sym) {
		if e.Callee == "" {
			continue
		}
		id := disambiguateCallee(r, e.Callee, callerFile, callerPkg)
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// disambiguateCallee picks the best canonical ID for a bare-name
// callee. Same-file first (most likely the call target), then any
// package match, then a synthetic fn:<pkg>.<name> so the wire shape
// stays valid even when resolution fails.
func disambiguateCallee(r *store.Repo, name, callerFile, callerPkg string) string {
	cands := r.Lookup("", name)
	if len(cands) > 0 {
		for _, c := range cands {
			if c.File == callerFile {
				if id, err := r.SymbolID(c.File, c.Sym); err == nil {
					return id
				}
			}
		}
		if callerPkg != "" {
			for _, c := range cands {
				if r.PackageOf(c.File) == callerPkg {
					if id, err := r.SymbolID(c.File, c.Sym); err == nil {
						return id
					}
				}
			}
		}
		for _, c := range cands {
			if id, err := r.SymbolID(c.File, c.Sym); err == nil {
				return id
			}
		}
	}
	// Unresolved: still emit a parseable ID so downstream parsers
	// don't choke. fn:<pkg>.<name> is the canonical form for free
	// functions.
	if callerPkg != "" {
		return "fn:" + callerPkg + "." + name
	}
	return "fn:." + name
}

// qualifiedName builds the "<pkg>.<name>" form used in the qualified
// field. For methods the receiver is included: "<pkg>.<recv>.<name>".
// Matches the package, file, function, method, class, module ID
// grammar in the id package.
func qualifiedName(r *store.Repo, path string, sym parser.Symbol) string {
	pkg := r.PackageOf(path)
	name := sym.Name
	if sym.Receiver != "" {
		name = sym.Receiver + "." + name
	}
	return joinDotted(pkg, name)
}

func languageFor(f *source.File) string {
	if f == nil || f.Grammar == nil {
		return ""
	}
	return string(f.Grammar.Name())
}

// sliceBody returns the byte slice for a symbol's row range. When a
// doc comment exists the slice is widened backwards to include it,
// so the leading prose stays with the chunk for embedding purposes.
// The doc comment is also exposed as a separate Signature field.
func sliceBody(r *store.Repo, f *source.File, sym parser.Symbol) (string, error) {
	start := sym.StartRow
	doc := r.DocComment(f.Path, sym)
	if doc != "" {
		start = start - docLineCount(doc)
		if start < 0 {
			start = 0
		}
	}
	end := sym.EndRow
	if end > f.LineCount() {
		end = f.LineCount()
	}
	if start >= end {
		return "", nil
	}
	return f.Slice(domain.LineRange{Start: start, End: end})
}

// docLineCount returns the number of lines in the doc comment,
// matching the format Repo.DocComment returns (one comment per line,
// joined by "\n").
func docLineCount(doc string) int {
	if doc == "" {
		return 0
	}
	return strings.Count(doc, "\n") + 1
}

// signatureWithDoc prepends a doc comment to a signature line, joined
// by a blank line. When doc is empty the signature is returned as-is.
func signatureWithDoc(doc, sig string) string {
	if doc == "" {
		return sig
	}
	return doc + "\n\n" + sig
}

// signatureLine returns the declaration line (e.g. "func Login(...)
// error") for a symbol. Prefers the store-level Signature accessor
// output when it isn't an LSP markdown fence; falls back to the
// first non-doc-comment line of the body otherwise. The chunker
// treats the body as the source of truth for the decl line because
// the LSP's hover answer is sometimes markdown-fenced prose
// (e.g. "```go\nfunc Foo() error\n```…") that has no parseable
// signature line on its own.
func signatureLine(storeSig, body string) string {
	trimmedStore := strings.TrimSpace(storeSig)
	if trimmedStore != "" && !strings.HasPrefix(trimmedStore, "```") {
		// Tree-sitter path (or any non-fenced source). First line wins.
		if i := strings.IndexByte(trimmedStore, '\n'); i >= 0 {
			return trimmedStore[:i]
		}
		return trimmedStore
	}
	// LSP-fenced or empty: walk the body, skip doc comments and blanks.
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		return trimmed
	}
	return ""
}

func firstLine(s string) string {
	if s == "" {
		return ""
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// joinDotted joins non-empty parts with ".". Mirrors id.JoinDotted
// without taking the import — the chunker doesn't need the rest of
// the id package and avoiding the import keeps the dep surface small.
func joinDotted(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, ".")
}
