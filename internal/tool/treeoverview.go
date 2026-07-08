// Package tool contains the 10 MCP tool handlers from design §4.
//
// Each tool is a tiny package-level file:
//  1. Schema() returns the JSON Schema (2020-12 dialect) for arguments.
//  2. Handler returns a closure suitable for mcp.ToolDef.Handler. Tool
//     handlers take a parsed typed argument struct (parsed once at the
//     boundary per "parse don't validate") and return a typed result that
//     mcp.Server marshals into the wire response.
//
// The handlers are the only "messy" part of the codebase — they translate
// JSON shapes into typed domain requests. Everything downstream (store,
// domain, parser) talks in precise types.
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/kellenff/yactt/internal/domain"
	"github.com/kellenff/yactt/internal/entity"
	"github.com/kellenff/yactt/internal/store"
)

// TreeOverviewArgs is the typed boundary input for tree_overview.
type TreeOverviewArgs struct {
	Repo          string   `json:"repo"`
	Scope         string   `json:"scope"`
	Depth         int      `json:"depth"`
	IncludeLayers []string `json:"include_layers"`
}

// TreeOverviewResult is one tree node in the response. The actual JSON shape
// is recursive.
type TreeOverviewResult struct {
	ID         string               `json:"id"`
	Kind       domain.NodeKind      `json:"kind"`
	Summary    string               `json:"summary,omitempty"`
	Provenance *domain.Provenance   `json:"provenance,omitempty"`
	// Receiver is the bounded on-wire view of this entity's receiver
	// (only set for methods whose receiver resolves to a class/module in
	// the indexed set). Omitted for non-methods and unresolved
	// receivers — see entity.ReceiverView for the four-field shape.
	Receiver *entity.ReceiverView `json:"receiver,omitempty"`
	Children []TreeOverviewResult `json:"children,omitempty"`
	// Warning is set on the root only, when buildOverviewTree had to stop
	// descending because the response would have exceeded maxResponseBytes.
	// ponytail: this exists because callers need a "I missed something" signal
	// that survives json round-trip; lift to a richer truncation object only if
	// the budget starts truncating on real repos in surprising places.
	Warning string `json:"warning,omitempty"`
}

// maxResponseBytes caps the JSON size of a single tree_overview response.
// Var (not const) so tests can shrink it. ponytail: 16KB ≈ 4K tokens by
// len(json)/4 rule. Lift to a tool argument when a caller actually needs to
// tune it — none have yet.
var maxResponseBytes = 16 * 1024

// truncatedWarning is the fixed string stamped on root.Warning when the
// budget causes the tree to be cut short. Constant so tests can match exactly.
const truncatedWarning = "tree_overview: response truncated at 16KB budget; reduce depth or scope to see more"

// TreeOverviewSchema is the JSON Schema for tree_overview. Mirrors the
// design's spec in §4.1.
var TreeOverviewSchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "repo":           { "type": "string", "description": "Absolute path or repo alias" },
    "scope":          { "type": "string", "description": "Optional absolute path under repo root; narrows the walk to a package or subdirectory. Mirrors search.Search's q.Scope." },
    "depth":          { "type": "integer", "default": 2, "minimum": 1, "maximum": 6 },
    "include_layers": {
      "type": "array",
      "items": { "enum": ["summary", "structure", "signature"] },
      "default": ["summary", "structure"]
    }
  },
  "required": ["repo"],
  "additionalProperties": false
}`)

// TreeOverviewOutputSchema declares the structuredContent shape of
// tree_overview. The handler already returns a single TreeOverviewResult
// (an object), so the schema is minimally typed — full property
// descriptions live on TreeOverviewResult.
var TreeOverviewOutputSchema = json.RawMessage(`{
  "type": "object",
  "required": ["id", "kind"],
  "properties": {
    "id":         { "type": "string" },
    "kind":       { "type": "string", "description": "Domain kind (FUNCTION/METHOD/CLASS/MODULE). For the grammar-form and id-prefix mapping, call get_graph_schema and inspect kindMap." },
    "summary":    { "type": "string" },
    "provenance": { "type": "object" },
    "receiver":   { "type": ["object", "null"] },
    "children":   { "type": "array" },
    "warning":    { "type": "string" }
  },
  "additionalProperties": false
}`)

// TreeOverview returns a Handler that produces the top-N levels of the tree
// rooted at `repo`, with only the requested layer set populated.
func TreeOverview(repo *store.Repo) func(ctx context.Context, args json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		var a TreeOverviewArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("invalid tree_overview args: %w", err)
		}
		// Empty `repo` is treated as "use the server's rooted repo" — the CLI
		// already opened one, so the field is more of a label than a path.
		// We still call it out if the design requires a literal repo: a
		// contract test elsewhere asserts the schema lists `repo` as required.
		if repo == nil {
			return nil, fmt.Errorf("tree_overview: no repo bound to handler")
		}
		if a.Depth <= 0 {
			a.Depth = 2
		}
		if a.Depth > 6 {
			a.Depth = 6
		}
		// Scope: optional absolute path under repo root. Empty means whole
		// repo. We path-clean and reject paths outside the root so callers
		// get a clear error instead of a silently empty tree.
		if a.Scope != "" {
			a.Scope = filepath.Clean(a.Scope)
			if !strings.HasPrefix(a.Scope, repo.Root()) {
				return nil, fmt.Errorf("tree_overview: scope %q is outside repo root %q", a.Scope, repo.Root())
			}
		}
		root, truncated, err := buildOverviewTree(repo, repo.Root(), a.Depth, a.Scope)
		if err != nil {
			return nil, err
		}
		if truncated {
			root.Warning = truncatedWarning
		}
		return root, nil
	}
}

// buildOverviewTree walks the file index and produces the depth-N tree.
// depth=1 means "repo + children" but not their children; depth=2 adds one
// more level (files + functions). We do this synthetically since the repo
// index only stores (file, symbols[]) pairs.
//
// `scope` is an optional absolute path under rootPath; non-empty means
// only files whose path is under scope contribute to the tree. The
// synthetic root is still rooted at rootPath so the response is always
// parseable as a tree from the repo root.
//
// Returns the tree plus a `truncated` flag; the handler stamps the warning
// onto the root when the budget ran out.
func buildOverviewTree(repo *store.Repo, rootPath string, depth int, scope string) (TreeOverviewResult, bool, error) {
	b := &overviewBuilder{budget: maxResponseBytes, scope: scope}
	summary := "Repository: " + rootPath
	if scope != "" {
		summary += " (scoped to " + scope + ")"
	}
	root, err := b.build(repo, rootPath, rootPath, depth, domain.KindRepo, summary)
	if err != nil {
		return TreeOverviewResult{}, false, err
	}
	return root, b.truncated, nil
}

// overviewBuilder tracks the JSON-byte budget as a depth-N tree is built
// synthetically. ponytail: global lock around the byte counter is fine —
// tree_overview is request-scoped and serialized per MCP call.
type overviewBuilder struct {
	budget    int
	used      int
	truncated bool
	scope     string // optional path prefix; empty = whole repo
}

// nodeOverhead is the fixed per-node JSON cost we charge before adding a
// child: id + summary text + kind enum + provenance object + children
// container + object wrapper. ponytail: this is a hand-tuned constant, not
// a real marshal. Good enough because truncation only needs to be
// approximate, never precise.
const nodeOverhead = 256

// reserve estimates the JSON bytes a node will cost, charges them up front,
// and reports whether they fit. If not, it flips b.truncated and returns false.
// The caller drops the child in that case.
func (b *overviewBuilder) reserve(id, summary string) bool {
	if b.truncated {
		return false
	}
	cost := len(id) + len(summary) + nodeOverhead
	if b.used+cost > b.budget {
		b.truncated = true
		return false
	}
	b.used += cost
	return true
}

// build constructs one node and (when allowed by depth) its children.
// `pathID` becomes the node ID; `kind` and `summary` come from the caller.
// When depth < 1 the function returns a leaf-shaped node with no children.
// The root node is reserved against the budget so the size estimate stays
// honest at the top of the tree.
func (b *overviewBuilder) build(repo *store.Repo, rootPath, nodePath string, depth int, kind domain.NodeKind, summary string) (TreeOverviewResult, error) {
	n := TreeOverviewResult{
		ID:         nodePath,
		Kind:       kind,
		Summary:    summary,
		Provenance: prov(),
	}
	if depth < 1 {
		return n, nil
	}
	if !b.reserve(nodePath, summary) {
		return n, nil
	}
	// Group files by package (their parent directory under root).
	pkgMap := make(map[string][]string)
	for _, p := range repo.Files() {
		if b.scope != "" && !strings.HasPrefix(p, b.scope) {
			continue
		}
		pkg := packagePath(rootPath, p)
		pkgMap[pkg] = append(pkgMap[pkg], p)
	}
	for pkg, files := range pkgMap {
		pkgID := pkg
		if pkg == "" {
			pkgID = rootPath
		}
		if !b.reserve("pkg:"+pkgID, "Package: "+pkg) {
			break
		}
		child := TreeOverviewResult{
			ID:         "pkg:" + pkgID,
			Kind:       domain.KindPackage,
			Summary:    "Package: " + pkg,
			Provenance: prov(),
		}
		for _, f := range files {
			fileID := "file:" + relPath(rootPath, f)
			if !b.reserve(fileID, "File: "+baseName(f)) {
				break
			}
			fileNode := TreeOverviewResult{
				ID:         fileID,
				Kind:       domain.KindFile,
				Summary:    "File: " + baseName(f),
				Provenance: prov(),
			}
			if depth >= 2 {
				syms := repo.Symbols(f)
				for _, s := range syms {
					ent := entityFromSymbol(s, f, repo)
					symID := ent.ID()
					if !b.reserve(symID, ent.Summary()) {
						break
					}
					symNode := TreeOverviewResult{
						ID:         symID,
						Kind:       ent.DomainKind(),
						Summary:    ent.Summary(),
						Provenance: prov(),
						Receiver:   ent.ReceiverView(),
					}
					fileNode.Children = append(fileNode.Children, symNode)
				}
			}
			child.Children = append(child.Children, fileNode)
		}
		n.Children = append(n.Children, child)
	}
	return n, nil
}
