package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// rootLong is the rich introductory text shown above the auto-generated
// command list in `yactt --help`. It used to be the entirety of the
// root usage string; the per-command help sections now live on each
// command's Long as well.
const rootLong = `yactt — federated code intelligence for AI agents

Usage:
  yactt overview <path>                   Print the top of the tree for a repo.
  yactt mcp serve [--audit-log=F]         Run the MCP server on stdio.
                                          The server is stateless across tool calls; tools
                                          identify their target project by a file:// URI
                                          passed in each tool's args (e.g. "file:///abs/path").
                                          Call list_projects to discover indexed repos,
                                          index_repository ({"project": "file:///abs/path"})
                                          to load a new one, then any code-intel tool
                                          with {"project": "file:///abs/path", ...}.
                                          --audit-log=F writes one JSON line per tool call to F.
  yactt chunk --repo <path> [options]     Emit AST-bounded NDJSON chunks to stdout.
                                          See "yactt chunk --help" for options.
  yactt hybrid --repo <path> --query Q    Run hybrid retrieval (structural + BM25 + vector,
                                          merged with RRF). See "yactt hybrid --help".
  yactt version                           Print version info (also: --version / -v flag).
  yactt help                              Show this message.

Project references on the wire
------------------------------
Every targeting tool (tree_overview, node_get, find_symbol,
search_code, query_graph, detect_changes, etc.) requires a
` + "`project`" + ` field shaped as a file:// URI:

  {"project": "file:///abs/path", ...}

The URI must be absolute and must point at a directory that has
been registered via index_repository. Relative paths and
schemes other than file:// are rejected with a clear error.

tree_overview retains a deprecated ` + "`repo`" + ` field as an alias
for ` + "`project`" + ` (a one-release grace period). Stderr emits a
deprecation notice every time it fires; remove it before v0.2.0.
`

var rootCmd = newRootCmd()

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "yactt",
		Short:         "federated code intelligence for AI agents",
		Long:          rootLong,
		Version:       version,
		SilenceUsage:  false,
		SilenceErrors: true,
		// No RunE: bare `yactt` and `yactt help` print help.
	}
	cmd.SetVersionTemplate("yactt {{.Version}}\n")
	cmd.AddCommand(
		newCmdOverview(),
		newCmdChunk(),
		newCmdHybrid(),
		newCmdMCP(),
	)
	return cmd
}

// Execute runs the CLI and returns the process exit code. main() is
// the only thing that calls os.Exit; tests can inspect the integer
// directly.
//
// Exit code contract (preserved from the hand-rolled parser):
//
//	0  success (including --help / --version)
//	1  runtime error (handler returned a non-parse error)
//	2  parse / usage error (Cobra's argparse failure)
func Execute() int {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		var fe *flagError
		if errors.As(err, &fe) {
			return 2
		}
		// Cobra's argparse errors are unwrapped; fall back to the
		// substrings we know they start with. This keeps the
		// contract stable regardless of whether the user typed
		// `yactt` with no args, an unknown subcommand, or a bad
		// flag.
		msg := err.Error()
		switch {
		case startsWith(msg, "unknown command"),
			startsWith(msg, "unknown shorthand"),
			startsWith(msg, "unknown flag"),
			startsWith(msg, "flag needs an argument"),
			startsWith(msg, "no such flag"),
			startsWith(msg, "invalid argument"),
			startsWith(msg, "required flag(s)"),
			startsWith(msg, "requires at most"),
			startsWith(msg, "requires at least"),
			startsWith(msg, "expected "),
			startsWith(msg, "accepts "):
			return 2
		}
		return 1
	}
	return 0
}

// flagError is a sentinel error type returned by Cobra's arg-validators
// (e.g. cobra.ExactArgs) when the user passes the wrong number of
// positionals. By carrying type information we can distinguish it
// from runtime errors without string matching.
type flagError struct{ err error }

func (f *flagError) Error() string { return f.err.Error() }
func (f *flagError) Unwrap() error { return f.err }

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
