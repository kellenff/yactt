package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kellenff/yactt/internal/audit"
	"github.com/kellenff/yactt/internal/mcp"
	httptransport "github.com/kellenff/yactt/internal/mcp/transport/http"
	"github.com/kellenff/yactt/internal/project"
	"github.com/kellenff/yactt/internal/registry"
	"github.com/kellenff/yactt/internal/tool"

	"github.com/spf13/cobra"
)

func newCmdMCP() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Run the MCP server (stdio or HTTP)",
		Long: "mcp hosts the federated code-intelligence tool surface as an\n" +
			"Model Context Protocol server. `mcp serve` runs on stdio for\n" +
			"agent launchers; `mcp serve-http` runs as a persistent HTTP\n" +
			"daemon with bearer-token auth and graceful shutdown.",
	}
	cmd.AddCommand(newCmdMCPServe(), newCmdMCPServeHTTP())
	return cmd
}

const mcpServeLong = `mcp serve runs the MCP server on stdio. After the project-reference
migration, the server runs in registry mode only — there is no
single-repo boot path. Agents call index_repository with a file://
URI to load a project before invoking any code-intel tool.

Flags:
  --audit-log <path>   Write one JSON audit line per tools/call dispatch
                       to <path>. The file is created with mode 0600 and
                       appended on subsequent invocations. Omit to disable
                       per-tool audit; the startup line still goes to stderr
                       on the first index_repository success per process.
`

func newCmdMCPServe() *cobra.Command {
	var auditPath string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the MCP server on stdio",
		Long:  mcpServeLong,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMCPServe(cmd, auditPath)
		},
	}
	cmd.Flags().StringVar(&auditPath, "audit-log", "", "Path to append per-tool audit JSON lines (mode 0600)")
	return cmd
}

// runMCPServe starts the MCP server on stdio. The registry handle
// is the only long-lived state. Code-intel tools resolve project
// URIs through it on every call.
func runMCPServe(cmd *cobra.Command, auditPath string) error {
	// Cobra gives a non-empty audit path here; the empty case is
	// filtered by the flag default.
	if auditPath == "" {
		// Allow the empty value to pass through unchanged; the
		// downstream audit.NewFileLogger check would have rejected
		// an empty path in the old parser. Treat empty as "audit
		// disabled" (no error).
	}

	regPath := registry.DefaultPath()
	if regPath == "" {
		return errors.New("mcp serve: cannot resolve $XDG_CACHE_HOME or $HOME; set XDG_CACHE_HOME")
	}
	reg := registry.New(regPath)

	var (
		auditLogger *audit.Logger
		auditCloser io.Closer
	)
	if auditPath != "" {
		var lerr error
		auditLogger, auditCloser, lerr = audit.NewFileLogger(auditPath)
		if lerr != nil {
			return fmt.Errorf("open audit log %s: %w", auditPath, lerr)
		}
		defer func() {
			if auditCloser != nil {
				_ = auditCloser.Close()
			}
		}()
	}

	srv := mcp.NewServer(
		"yactt",
		version,
		mcp.ProtocolVersion,
		cmd.OutOrStdout(),
		func() (io.Reader, error) { return os.Stdin, nil },
	)
	if auditLogger != nil {
		srv.WithAudit(auditLogger, audit.ExtractPaths)
	}

	// Build the audit + TOFU hooks for index_repository. They run
	// at most once per process (memoised inside IndexRepository).
	binSHA, _ := audit.BinarySHA256(binaryPath())
	emitStartup := func(info audit.Startup) error {
		info.Version = version
		info.BinarySHA256 = binSHA
		return audit.EmitStartup(cmd.ErrOrStderr(), info)
	}
	warnTrust := func() { warnInstallTrustChain(version, binSHA) }

	tool.RegisterAllTools(srv, reg, emitStartup, warnTrust)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	err := srv.Serve(ctx)
	// Reap pinned warm-index LSP children on clean shutdown.
	if idx := project.IndexFor(reg); idx != nil {
		_ = idx.Close()
		project.BindIndex(reg, nil)
	}
	// io.EOF and context.Canceled are clean termination: don't
	// surface them to the caller.
	if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

const mcpServeHTTPLong = `mcp serve-http runs the MCP server as a persistent HTTP daemon.

Endpoints:
  POST   /mcp                  — JSON-RPC request (tools resolve project via file:// URI in args)
  GET    /mcp                  — open SSE stream (requires session)
  DELETE /mcp                  — terminate session
  GET    /healthz              — liveness probe (unauthenticated)

By default the daemon binds to 127.0.0.1 and requires no auth. Use --bind=0.0.0.0
together with --auth-token to expose to a network; without --auth-token on a
non-loopback bind the daemon refuses all requests with 403 Forbidden.
`

func newCmdMCPServeHTTP() *cobra.Command {
	var (
		port          int
		bind          string
		token         string
		auditPath     string
		registryPath  string
		shutdownGrace time.Duration
		maxSessions   int
		idleTimeout   time.Duration
	)
	cmd := &cobra.Command{
		Use:   "serve-http",
		Short: "Run the MCP server as a persistent HTTP daemon",
		Long:  mcpServeHTTPLong,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMCPServeHTTP(cmd, port, bind, token, auditPath, registryPath, shutdownGrace, maxSessions, idleTimeout)
		},
	}
	cmd.Flags().IntVar(&port, "port", 8080, "TCP port to listen on (0 lets the kernel pick)")
	cmd.Flags().StringVar(&bind, "bind", "127.0.0.1", "Bind address (use 0.0.0.0 for non-loopback)")
	cmd.Flags().StringVar(&token, "auth-token", "", "Require \"Authorization: Bearer <token>\" on every request")
	cmd.Flags().StringVar(&auditPath, "audit-log", "", "Append one JSON line per tool call to <path> (mode 0600)")
	cmd.Flags().StringVar(&registryPath, "registry", "", "Registry file location (default $XDG_CACHE_HOME/yactt/projects.json)")
	cmd.Flags().DurationVar(&shutdownGrace, "shutdown-grace", 10*time.Second, "Grace window for in-flight requests on SIGTERM")
	cmd.Flags().IntVar(&maxSessions, "max-sessions", 256, "Cap on concurrent sessions")
	cmd.Flags().DurationVar(&idleTimeout, "idle-timeout", 5*time.Minute, "Idle reap threshold")
	return cmd
}

// runMCPServeHTTP parses the daemon flags, builds the server, and
// blocks until SIGINT/SIGTERM or a fatal error.
func runMCPServeHTTP(cmd *cobra.Command, port int, bind, token, auditPath, registryPath string, shutdownGrace time.Duration, maxSessions int, idleTimeout time.Duration) error {
	cfg := httptransport.ServerConfig{
		ProtocolName:  "yactt",
		Version:       version,
		ProtocolVer:   httptransport.ExpectedProtocol,
		Port:          port,
		Bind:          bind,
		MaxSessions:   maxSessions,
		IdleTimeout:   idleTimeout,
		ShutdownGrace: shutdownGrace,
		Token:         token,
	}

	if registryPath == "" {
		registryPath = registry.DefaultPath()
		if registryPath == "" {
			return errors.New("mcp serve-http: cannot resolve registry path; set XDG_CACHE_HOME or pass --registry")
		}
	}
	reg := registry.New(registryPath)

	// Bind/auth warning — operator owns the decision.
	if cfg.Bind != "" && cfg.Bind != "127.0.0.1" && cfg.Bind != "::1" && cfg.Bind != "localhost" && cfg.Token == "" {
		fmt.Fprintln(cmd.ErrOrStderr(),
			"WARNING: bound to non-loopback address without --auth-token; the daemon is unauthenticated. "+
				"Set --auth-token or reverse-proxy through an authenticated gateway.")
	}

	var auditLogger *audit.Logger
	var auditCloser io.Closer
	if auditPath != "" {
		var err error
		auditLogger, auditCloser, err = audit.NewFileLogger(auditPath)
		if err != nil {
			return fmt.Errorf("open audit log %s: %w", auditPath, err)
		}
		defer func() {
			if auditCloser != nil {
				_ = auditCloser.Close()
			}
		}()
	}

	fmt.Fprintf(cmd.ErrOrStderr(),
		"yactt mcp serve-http listening on %s:%d protocol=%s registry=%s sessions=%d idle=%s grace=%s\n",
		cfg.Bind, cfg.Port, cfg.ProtocolVer, registryPath, cfg.MaxSessions, cfg.IdleTimeout, cfg.ShutdownGrace,
	)

	daemon := httptransport.NewServer(cfg, reg)
	if auditLogger != nil {
		daemon.WithAudit(auditLogger)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- daemon.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), cfg.ShutdownGrace)
		defer cancelShutdown()
		_ = daemon.Shutdown(shutdownCtx)
		<-serveErr
		return nil
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
