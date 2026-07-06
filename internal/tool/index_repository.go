package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/kellenff/yactt/internal/parser"
	"github.com/kellenff/yactt/internal/registry"
	"github.com/kellenff/yactt/internal/store"
)

// IndexRepositoryArgs is the typed boundary input for
// index_repository. `path` is required; `name` is an optional
// display label that defaults to the basename; `mode` picks
// which indexing strategy to run (today only "full" is wired —
// see `mapMode`).
type IndexRepositoryArgs struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Mode string `json:"mode"`
}

// IndexRepositoryResult is the structuredContent envelope for
// index_repository. `entry` is the freshly-written registry row;
// `warnings` is the per-file error slice store.Load surfaces,
// kept here so the agent can spot a partially-loaded repo.
type IndexRepositoryResult struct {
	Entry    registry.Entry `json:"entry"`
	Warnings int            `json:"warnings"`
}

// IndexRepositorySchema is the JSON Schema for index_repository.
// `path` is required; the other two are optional. We expose
// `mode` because the issue promises the knob, even though the
// only value honoured today is "full".
var IndexRepositorySchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["path"],
  "properties": {
    "path": { "type": "string", "description": "Absolute or cwd-relative path to the repo root." },
    "name": { "type": "string", "description": "Optional display label; defaults to the basename of ` + "`path`" + `." },
    "mode": { "type": "string", "enum": ["full", "moderate", "fast", "cross-repo-intelligence"], "description": "Indexing mode (ponytail: only ` + "`full`" + ` is wired today; the string is accepted for forward compatibility)." }
  },
  "additionalProperties": false
}`)

// IndexRepositoryOutputSchema declares the structuredContent shape
// of index_repository.
var IndexRepositoryOutputSchema = json.RawMessage(`{
  "type": "object",
  "required": ["entry", "warnings"],
  "properties": {
    "entry":    { "type": "object" },
    "warnings": { "type": "integer", "minimum": 0 }
  },
  "additionalProperties": false
}`)

// IndexRepository returns a Handler that walks `args.Path`,
// records an entry in `reg`, and returns the persisted row.
// The handler is repo-independent at construction time so it
// works in both single-repo and registry-only modes — the
// handler walks its own temporary Repo just to count files and
// detect languages; nothing about that walk is shared with
// the MCP server's "currently loaded" repo.
//
// ponytail: the disk cache + per-repo subdir that store.Load
// touches via loadOptsWithDiskCache lives in cmd/yactt/main.go.
// We deliberately do not pass that helper down here — the
// common path is "use the same cache scheme as everything
// else", and pulling it into a single importable place would
// grow that helper's surface area for one caller. The cache
// shape this tool produces is therefore "whatever the user's
// serve-time load would have produced", which is exactly what
// we want.
func IndexRepository(reg *registry.Registry) func(ctx context.Context, args json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		var a IndexRepositoryArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("invalid index_repository args: %w", err)
		}
		if a.Path == "" {
			return nil, errors.New("index_repository: path is required")
		}
		abs, err := filepath.Abs(a.Path)
		if err != nil {
			return nil, fmt.Errorf("index_repository: resolve path: %w", err)
		}
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

		// One-shot store.Load to count files + detect languages.
		// We pass the same disk-cache LoadOptions the serve
		// command does, so index_repository has the side-effect
		// the issue promises: "standalone indexing" actually
		// primes the per-repo cache that index_status then
		// verifies. Closing the returned Repo before writing
		// the entry matters: store.Load may have spun up LSP
		// clients which otherwise would hold the loaded tree
		// alive past this handler's return.
		repo, errs, lerr := store.Load(abs, registry.LoadOptsWithDiskCache(abs)...)
		if lerr != nil {
			return nil, fmt.Errorf("index_repository: load: %w", lerr)
		}
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
