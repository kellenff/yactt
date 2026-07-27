package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/kellenff/yactt/internal/registry"
	"github.com/kellenff/yactt/internal/store"
	"github.com/kellenff/yactt/internal/tool"

	"github.com/spf13/cobra"
)

func newCmdOverview() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "overview <path>",
		Short: "Print the top of the tree for a repo",
		Long: "overview loads the repository at <path> and prints a tree\n" +
			"representation of the top two levels as JSON. The output shape\n" +
			"matches the MCP tree_overview tool; the CLI seeds an ephemeral\n" +
			"registry so the handler can resolve the project the same way\n" +
			"it does inside the server.",
		Args: cobra.ExactArgs(1),
		RunE: runOverview,
	}
	return cmd
}

// runOverview loads the repo at args[0] and prints a tree
// representation of the top-2 levels in JSON. Extracted from the
// hand-rolled parser; behavior is identical, only the entrypoint
// changed from `func([]string) error` to `cobra.RunE`.
func runOverview(cmd *cobra.Command, args []string) error {
	abs, err := filepath.Abs(args[0])
	if err != nil {
		return err
	}
	repo, errs, err := store.Load(abs, loadOptsWithDiskCache(abs)...)
	if err != nil {
		return fmt.Errorf("load: %w", err)
	}
	if len(errs) > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "load: %d file errors\n", len(errs))
	}
	// The tree_overview tool now takes a *registry.Registry and a
	// project (file:// URI) in args. The CLI seeds an ephemeral
	// registry so the handler can resolve the project the same way
	// it does in the MCP server.
	regPath := filepath.Join(os.TempDir(), "yactt-overview-registry.json")
	reg := registry.New(regPath)
	if err := reg.Upsert(registry.Entry{
		Name:      filepath.Base(abs),
		Path:      abs,
		IndexedAt: time.Now().UTC(),
		Files:     len(repo.Files()),
	}); err != nil {
		_ = repo.Close()
		return fmt.Errorf("overview: seed registry: %w", err)
	}
	handler := tool.TreeOverview(reg)
	out, herr := handler(context.Background(), json.RawMessage(fmt.Sprintf(`{"project":%q,"depth":2}`, "file://"+abs)))
	_ = repo.Close()
	if herr != nil {
		return herr
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Fprintln(cmd.OutOrStdout(), string(b))
	return nil
}
