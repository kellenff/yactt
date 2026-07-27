package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/kellenff/yactt/internal/chunker"
	"github.com/kellenff/yactt/internal/hybrid"
	"github.com/kellenff/yactt/internal/store"

	"github.com/spf13/cobra"
)

const hybridLong = `yactt hybrid — fan a query out to structural + BM25 + vector channels and merge via RRF.

Output:
  Default: JSON object {"results": [...]}. Each hit carries id, rrf-score,
  channel, and (for bm25/vector) the underlying chunk payload.
  With --explain: JSON object {"structural":[...],"bm25":[...],"vector":[...],
  "rrf":[...]}, so the per-channel and merged rankings are visible side by side.

Notes:
  - The vector channel uses a stdlib bag-of-tokens reference backend. It's
    good enough to demo the merge and run the benchmark, not competitive
    with a real embedding model. See docs/hybrid-retrieval.md for the
    plug-in shape (LangChain, LlamaIndex, pgvector, Chroma, ...).
  - Per-channel failures degrade gracefully: a failing vector backend
    doesn't kill the merge; the structural + BM25 hits still surface.
`

func newCmdHybrid() *cobra.Command {
	var (
		repoPath   string
		query      string
		limit      int
		channelsCS string
		withTests  bool
		explain    bool
	)
	cmd := &cobra.Command{
		Use:   "hybrid",
		Short: "Run hybrid retrieval (structural + BM25 + vector) merged with RRF",
		Long:  hybridLong,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHybrid(cmd, repoPath, query, limit, channelsCS, withTests, explain)
		},
	}
	cmd.Flags().StringVar(&repoPath, "repo", "", "Repository root (required)")
	cmd.Flags().StringVar(&query, "query", "", "Query string (required); tokenized on whitespace; quoted phrases become single terms")
	cmd.Flags().IntVar(&limit, "limit", 10, "Final top-K")
	cmd.Flags().StringVar(&channelsCS, "channels", "", "Comma-separated channel list to enable; default enables structural,bm25,vector")
	cmd.Flags().BoolVar(&withTests, "with-tests", false, "Include test files in the BM25 + vector corpus")
	cmd.Flags().BoolVar(&explain, "explain", false, "Print per-channel rankings alongside the merged list")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("query")
	return cmd
}

func runHybrid(cmd *cobra.Command, repoPath, query string, limit int, channelsCS string, withTests, explain bool) error {
	opts := hybrid.Options{
		Query:       query,
		Limit:       limit,
		ChunkerOpts: chunker.Options{WithTests: withTests},
	}

	channels, err := parseChannels(channelsCS)
	if err != nil {
		return err
	}
	opts.Channels = channels
	// The CLI ships the stdlib reference vector backend. Production
	// users embed yactt as a Go library — the CLI keeps the
	// dependency surface small and works out of the box.
	if opts.Channels.Vector {
		opts.Vector = hybrid.NewBagOfTokens()
	}

	abs, err := filepath.Abs(repoPath)
	if err != nil {
		return err
	}
	repo, errs, err := store.Load(abs, loadOptsWithDiskCache(abs)...)
	if err != nil {
		return fmt.Errorf("load: %w", err)
	}
	defer func() { _ = repo.Close() }()
	if len(errs) > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "load: %d file errors\n", len(errs))
	}
	opts.Repo = repo

	return runHybridPipeline(repo, opts, explain, cmd.OutOrStdout(), cmd.ErrOrStderr())
}

// parseChannels parses the comma-separated channel list. Empty
// string → all three on (production default). Unknown channels are
// rejected with a clear error so typos surface immediately.
func parseChannels(s string) (hybrid.Channels, error) {
	if s == "" {
		return hybrid.AllChannels(), nil
	}
	c := hybrid.Channels{}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		switch p {
		case "":
			continue
		case "structural":
			c.Structural = true
		case "bm25":
			c.BM25 = true
		case "vector":
			c.Vector = true
		default:
			return c, fmt.Errorf("hybrid: unknown channel %q (want structural|bm25|vector)", p)
		}
	}
	if !c.Structural && !c.BM25 && !c.Vector {
		return c, errors.New("hybrid: --channels must include at least one of structural|bm25|vector")
	}
	return c, nil
}

// runHybridPipeline is the testable core of runHybrid. It runs the
// orchestrator and writes either the merged JSON or the per-channel
// explain view. Splitting it out keeps the CLI's flag-parsing out
// of the unit-test path.
func runHybridPipeline(r *store.Repo, opts hybrid.Options, explain bool, stdout, stderr io.Writer) error {
	if explain {
		out, err := hybrid.Explain(context.Background(), opts)
		if err != nil {
			return fmt.Errorf("hybrid: %w", err)
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Fprintln(stdout, string(b))
		fmt.Fprintf(stderr, "hybrid: %d channels, %d merged hits\n", len(out)-1, len(out["rrf"]))
		return nil
	}
	hits, err := hybrid.Run(context.Background(), opts)
	if err != nil {
		return fmt.Errorf("hybrid: %w", err)
	}
	b, _ := json.MarshalIndent(map[string]any{"results": hits}, "", "  ")
	fmt.Fprintln(stdout, string(b))
	fmt.Fprintf(stderr, "hybrid: %d hits\n", len(hits))
	return nil
}
