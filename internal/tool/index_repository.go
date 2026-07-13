package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/kellenff/yactt/internal/audit"
	"github.com/kellenff/yactt/internal/parser"
	"github.com/kellenff/yactt/internal/project"
	"github.com/kellenff/yactt/internal/registry"
	"github.com/kellenff/yactt/internal/store"
)

// IndexRepositoryArgs is the typed boundary input for
// index_repository. `project` (file:// URI) is required; `name`
// is an optional display label that defaults to the basename;
// `mode` picks which indexing strategy to run (today only
// "full" is wired — see `mapMode`).
type IndexRepositoryArgs struct {
	Project string `json:"project"`
	Name    string `json:"name"`
	Mode    string `json:"mode"`
}

// IndexRepositoryResult is the structuredContent envelope for
// index_repository. Three independent fields:
//
//   - `entry` is the registry row (just-written on a cold call,
//     returned-as-is on a warm-cache short-circuit).
//   - `warnings` is the per-file error slice store.Load surfaces
//     on a cold call; 0 on a short-circuit (we did no walking).
//   - `reloaded` reports whether the handler actually walked the
//     source tree this call. True on cold cache, source-stale-
//     since-IndexedAt, or registry-row-missing; false on a
//     fresh-cache short-circuit. Agents that call index_repository
//     defensively before every code-intel tool use this to
//     distinguish "you just paid the index cost" from "you were
//     already up to date — go ahead".
type IndexRepositoryResult struct {
	Entry    registry.Entry `json:"entry"`
	Warnings int            `json:"warnings"`
	Reloaded bool           `json:"reloaded"`
}

// IndexRepositorySchema is the JSON Schema for index_repository.
// `project` is required; the other two are optional. We expose
// `mode` because the issue promises the knob, even though the
// only value honoured today is "full".
var IndexRepositorySchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["project"],
  "properties": {
    "project": { "type": "string", "description": "Absolute path as a file:// URI (e.g. file:///abs/path)." },
    "name":    { "type": "string", "description": "Optional display label; defaults to the basename of the project's path." },
    "mode":    { "type": "string", "enum": ["full", "moderate", "fast", "cross-repo-intelligence"], "description": "Indexing mode (ponytail: only ` + "`full`" + ` is wired today; the string is accepted for forward compatibility)." }
  },
  "additionalProperties": false
}`)

// IndexRepositoryOutputSchema declares the structuredContent shape
// of index_repository.
var IndexRepositoryOutputSchema = json.RawMessage(`{
  "type": "object",
  "required": ["entry", "warnings", "reloaded"],
  "properties": {
    "entry":    { "type": "object" },
    "warnings": { "type": "integer", "minimum": 0 },
    "reloaded": { "type": "boolean", "description": "True when the handler walked the source tree this call; false when the registry entry was already fresh and the call short-circuited." }
  },
  "additionalProperties": false
}`)

// IndexRepository returns a Handler that walks the project at
// `args.Project` (a file:// URI), records an entry in `reg`,
// and returns the persisted row. On the FIRST successful index
// per process it invokes emitStartup (audit line) and warnTrust
// (install TOFU) — both nil-safe and memoised.
//
// The handler short-circuits on a warm cache (entry exists with
// the same Mode AND disk cache is fresh AND no source mtime is
// newer than IndexedAt) and returns the existing entry with
// Reloaded=false. The short-circuit is bypassed when:
//   - the caller supplies a non-empty `name` that differs from
//     the cached entry's Name (custom-name requests must take
//     effect even on a warm cache — `name` is a per-call label
//     override, not a stored property of the cache);
//   - the cached entry's Mode differs from the requested Mode
//     (forward-compat knob today; mandatory reload once modes
//     branch).
// Otherwise agents that call index_repository defensively pay the
// store.Load cost on every invocation.
//
// ponytail: the disk cache + per-repo subdir that store.Load
// touches via registry.LoadOptsWithDiskCache is the same path
// the serve command uses, so index_repository has the
// side-effect the issue promises: "standalone indexing" actually
// primes the per-repo cache that index_status then verifies.
func IndexRepository(reg *registry.Registry, emitStartup func(audit.Startup) error, warnTrust func()) func(ctx context.Context, args json.RawMessage) (any, error) {
	var once sync.Once
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		var a IndexRepositoryArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("invalid index_repository args: %w", err)
		}
		ref, err := project.ParseRef(a.Project)
		if err != nil {
			return nil, fmt.Errorf("index_repository: %w", err)
		}
		abs := ref.Path
		if _, err := os.Stat(abs); err != nil {
			return nil, fmt.Errorf("index_repository: %w", err)
		}
		// Sanity-check the mode string up front. Future slices
		// will branch on this value; for now we just validate
		// and ignore anything except "full".
		mode := a.Mode
		if mode == "" {
			mode = "full"
		}
		if !isKnownMode(mode) {
			return nil, fmt.Errorf("index_repository: unknown mode %q (want full|moderate|fast|cross-repo-intelligence)", mode)
		}

		// Short-circuit when the registry already has a fresh
		// entry for `abs`. The cold-path store.Load below walks
		// every file in the tree (file count + per-file grammar
		// parse + LSP warm-up); on the hot path this is pure
		// latency. The freshness rule mirrors index_status's
		// checkCacheState: the cache directory must exist AND
		// no source file mtime may be newer than IndexedAt. A
		// missing registry row also forces a reload so we
		// re-persist it.
		//
		// The audit + TOFU hooks below are memoised by sync.Once
		// so the short-circuit can never accidentally double-emit.
		// The short-circuit is bypassed when:
		//   - the caller supplied a non-empty `name` that
		//     differs from the cached entry's Name — `name` is
		//     a per-call label override, not a stored property
		//     of the cache, so a custom-name call must take
		//     effect even on a warm cache.
		//   - the cached entry's Mode differs from the requested
		//     Mode (forward-compat knob today; mandatory reload
		//     once modes branch).
		if existing, hasEntry := reg.GetByPath(abs); hasEntry && existing.Mode == mode {
			if a.Name == "" || a.Name == existing.Name {
				cacheDir := registry.CacheDirForRoot(abs)
				if _, serr := os.Stat(cacheDir); serr == nil {
					newest, werr := newestMTime(abs)
					if werr == nil && !newest.After(existing.IndexedAt) {
						return &IndexRepositoryResult{
							Entry:    existing,
							Warnings: 0,
							Reloaded: false,
						}, nil
					}
				}
			}
		}

		// One-shot store.Load to count files + detect languages.
		repo, errs, lerr := store.Load(abs, registry.LoadOptsWithDiskCache(abs)...)
		if lerr != nil {
			return nil, fmt.Errorf("index_repository: load: %w", lerr)
		}
		// Audit + TOFU emit on the first successful index per
		// process. Memoised by `once`; both closures are
		// nil-safe so tests can pass nil and skip these
		// side effects.
		once.Do(func() {
			if warnTrust != nil {
				warnTrust()
			}
			if emitStartup != nil {
				info := audit.Startup{
					RepoRoot:     repo.Root(),
					MaxFiles:     store.DefaultMaxFiles,
					LoadedFiles:  len(repo.Files()),
					Grammars:     repoGrammars(),
					LSP:          repoLSP(repo),
				}
				if err := emitStartup(info); err != nil {
					fmt.Fprintf(os.Stderr, "warning: startup audit emit: %v\n", err)
				}
			}
		})
		_ = repo.Close()

		name := a.Name
		if name == "" {
			name = filepath.Base(abs)
		}

		entry := registry.Entry{
			Name:      name,
			Path:      abs,
			IndexedAt: time.Now().UTC(),
			Files:     len(repo.Files()),
			Languages: detectLanguages(repo),
			Mode:      mode,
		}
		if uerr := reg.Upsert(entry); uerr != nil {
			return nil, fmt.Errorf("index_repository: upsert: %w", uerr)
		}
		return &IndexRepositoryResult{
			Entry:    entry,
			Warnings: len(errs),
			Reloaded: true,
		}, nil
	}
}

// detectLanguages reads the languages actually populated in the
// loaded Repo. Mirrors audit.Startup's grammar list (it also
// walks parser.All()), but filters to the subset the repo
// actually has — an empty Languages field is unhelpful in the
// registry. Sorting keeps the slice deterministic across runs.
func detectLanguages(repo *store.Repo) []string {
	files := repo.Files()
	hits := make(map[string]struct{}, len(parser.All()))
	for _, p := range files {
		lang, err := parser.Detect(p)
		if err != nil {
			continue
		}
		hits[string(lang.Name())] = struct{}{}
	}
	out := make([]string, 0, len(hits))
	for l := range hits {
		out = append(out, l)
	}
	// Inline insertion sort; languages count is ≤ a dozen so a
	// full sort.Slice is overkill.
	for i := 1; i < len(out); i++ {
		j := i
		for j > 0 && out[j-1] > out[j] {
			out[j-1], out[j] = out[j], out[j-1]
			j--
		}
	}
	return out
}

// isKnownMode reports whether the mode string is one we accept
// on the wire. Day-one only "full" actually influences the
// load; the rest are accepted for forward compatibility.
func isKnownMode(m string) bool {
	switch m {
	case "full", "moderate", "fast", "cross-repo-intelligence":
		return true
	}
	return false
}

// repoGrammars returns the list of grammar names supported by
// the loader. Mirrors the field on audit.Startup; the audit
// package can't compute this without a parser import so the
// helper lives here.
func repoGrammars() []string {
	out := make([]string, 0)
	for _, l := range parser.All() {
		out = append(out, string(l.Name()))
	}
	return out
}

// repoLSP returns the per-language LSP availability for the
// given repo. Used to populate the audit startup record.
func repoLSP(repo *store.Repo) []audit.LSPEntry {
	var out []audit.LSPEntry
	for _, l := range parser.All() {
		_, toolName, ver := repo.LSPForLang(l.Name())
		out = append(out, audit.LSPEntry{
			Language: string(l.Name()),
			Tool:     toolName,
			Version:  ver,
		})
	}
	return out
}
