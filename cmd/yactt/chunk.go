package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/kellenff/yactt/internal/chunker"
	"github.com/kellenff/yactt/internal/parser"
	"github.com/kellenff/yactt/internal/store"

	"github.com/spf13/cobra"
)

const chunkLong = `yactt chunk — emit AST-bounded NDJSON chunks for vector-store ingest.

Usage:
  yactt chunk --repo <path> [options]

Output is one Chunk per line in NDJSON. The shape is documented on
internal/chunker.Chunk. Errors during the load step are reported on
stderr; per-chunk decode errors are skipped (partial output is
preferred to no output for ingest pipelines).
`

func newCmdChunk() *cobra.Command {
	var (
		repoPath   string
		policy     string
		languages  string
		include    []string
		exclude    []string
		withTests  bool
		outputPath string
	)
	cmd := &cobra.Command{
		Use:   "chunk",
		Short: "Emit AST-bounded NDJSON chunks for vector-store ingest",
		Long:  chunkLong,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runChunk(cmd, repoPath, policy, languages, include, exclude, withTests, outputPath)
		},
	}
	cmd.Flags().StringVar(&repoPath, "repo", "", "Path to the repository root (required)")
	cmd.Flags().StringVar(&policy, "policy", "", "Chunking policy: function (default), class, module")
	cmd.Flags().StringVar(&languages, "languages", "", "Comma-separated language filter (e.g. \"go,typescript\")")
	cmd.Flags().StringSliceVar(&include, "include", nil, "filepath.Match glob; can be repeated")
	cmd.Flags().StringSliceVar(&exclude, "exclude", nil, "filepath.Match glob; can be repeated; applied after --include")
	cmd.Flags().BoolVar(&withTests, "with-tests", false, "Include *_test.go and per-language test files")
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "Output file; default stdout")
	_ = cmd.MarkFlagRequired("repo")
	return cmd
}

// runChunk loads the repo and writes NDJSON chunks to the chosen
// writer. The CLI's flag-parsing style is now driven by Cobra/pflag
// via newCmdChunk; this function only validates values and runs the
// pipeline. Splitting it out keeps the validation logic out of the
// unit-test path while letting tests assert on the exact NDJSON bytes
// emitted (via runChunkPipeline).
func runChunk(cmd *cobra.Command, repoPath, policy, languages string, include, exclude []string, withTests bool, outputPath string) error {
	opts := chunker.Options{
		Include:   include,
		Exclude:   exclude,
		WithTests: withTests,
	}
	if policy != "" {
		switch chunker.Policy(policy) {
		case chunker.PolicyFunction, chunker.PolicyClass, chunker.PolicyModule:
			opts.Policy = chunker.Policy(policy)
		default:
			return fmt.Errorf("chunk: unknown policy %q (want function|class|module)", policy)
		}
	}
	for _, l := range splitCSV(languages) {
		opts.Languages = append(opts.Languages, parser.Name(l))
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

	out := io.Writer(cmd.OutOrStdout())
	if outputPath != "" {
		f, ferr := os.Create(outputPath)
		if ferr != nil {
			return fmt.Errorf("chunk: create output: %w", ferr)
		}
		defer func() { _ = f.Close() }()
		out = f
	}

	return runChunkPipeline(repo, opts, out, cmd.ErrOrStderr())
}

// runChunkPipeline is the testable core of runChunk: run the
// chunker, write to stdout, log to stderr. Splitting it out keeps
// the CLI's flag-parsing code out of the unit-test path while
// letting tests assert on the exact NDJSON bytes emitted.
func runChunkPipeline(r *store.Repo, opts chunker.Options, stdout, stderr io.Writer) error {
	if r == nil {
		return errors.New("chunk: nil repo")
	}
	n, err := chunker.Run(context.Background(), r, opts, stdout)
	if err != nil {
		return fmt.Errorf("chunk: %w", err)
	}
	fmt.Fprintf(stderr, "chunk: %d chunks written\n", n)
	return nil
}

// splitCSV splits a comma-separated string into trimmed non-empty
// tokens. Empty fields are skipped; "a,,b" yields ["a", "b"]. Used
// by the chunk/hybrid commands to normalize --languages /
// --channels.
func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
