// Command yactt is the federated code intelligence CLI.
//
// Subcommands are wired up via Cobra in their own files:
//
//	yactt overview <path>       Print the top of the tree for a repo.
//	yactt chunk --repo ...      Emit AST-bounded NDJSON chunks to stdout.
//	yactt hybrid --repo ...     Run hybrid retrieval (structural + BM25 + vector).
//	yactt mcp serve             Run the MCP server on stdio.
//	yactt mcp serve-http        Run the MCP server as an HTTP daemon.
//
// The CLI is intentionally thin: it loads a repo, then either prints a tree
// or wires tools into an mcp.Server. Anything with logic lives in internal/.
//
// Cobra owns the parser, --help / --version / unknown-command behavior, and
// per-command flag binding. The Execute() function in root.go maps Cobra's
// argparse errors to exit code 2 and runtime errors to exit code 1, matching
// the contract the hand-rolled parser used to provide.
package main

import (
	"os"
)

// version is stamped onto the binary at build time via
// -ldflags="-X main.version=<tag>". Default "dev" covers `go build`
// outside CI; release CI overrides it with the git tag. Root command
// reads this via rootCmd.Version (see root.go).
var version = "dev"

func main() {
	os.Exit(Execute())
}
