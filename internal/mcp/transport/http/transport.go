package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/kellenff/yactt/internal/audit"
	"github.com/kellenff/yactt/internal/mcp"
	"github.com/kellenff/yactt/internal/project"
	"github.com/kellenff/yactt/internal/registry"
	"github.com/kellenff/yactt/internal/tool"
)

// ServerConfig configures one HTTP daemon.
type ServerConfig struct {
	Token         string
	Bind          string
	Port          int
	MaxSessions   int
	IdleTimeout   time.Duration
	ShutdownGrace time.Duration
	ProtocolName  string
	Version       string
	ProtocolVer   string
}

// Server is the HTTP daemon. Construct with NewServer, then
// either Handler() (for httptest) or ListenAndServe (for a real
// listener). The Server is safe for concurrent use after
// construction; the reaper goroutine starts in NewServer and is
// stopped by Shutdown.
type Server struct {
	cfg      ServerConfig
	reg      *registry.Registry
	auth     *AuthChecker
	sessions *SessionManager
	index    *project.Index // process-lifetime warm project index

	mu        sync.Mutex
	audit     *audit.Logger
	mcpServer *mcp.Server // shared across all sessions, built lazily

	httpServer *http.Server
}

// NewServer constructs a daemon wired against the given
// registry, builds the shared *mcp.Server, and starts the
// session reaper goroutine. The reaper is stopped when Shutdown
// is called.
func NewServer(cfg ServerConfig, reg *registry.Registry) *Server {
	if cfg.MaxSessions == 0 {
		cfg.MaxSessions = 256
	}
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = 5 * time.Minute
	}
	if cfg.ShutdownGrace == 0 {
		cfg.ShutdownGrace = 10 * time.Second
	}
	if cfg.Bind == "" {
		cfg.Bind = "127.0.0.1"
	}
	if cfg.ProtocolVer == "" {
		cfg.ProtocolVer = ExpectedProtocol
	}
	s := &Server{
		cfg:      cfg,
		reg:      reg,
		auth:     NewAuthChecker(cfg.Token, cfg.Bind),
		sessions: NewSessionManager(cfg.IdleTimeout, cfg.MaxSessions),
	}
	s.mcpServer = s.buildMCPServer()
	go s.reaperLoop()
	return s
}

// buildMCPServer constructs the shared *mcp.Server with all 21
// tools registered. The HTTP and audit hooks are nil because
// the daemon lifecycle is different from stdio — we don't emit
// a startup line and don't warn on trust-chain mismatch.
// The returned Index is retained on s so Shutdown can ForceClose
// pinned repos.
func (s *Server) buildMCPServer() *mcp.Server {
	srv := mcp.NewServer(s.cfg.ProtocolName, s.cfg.Version, s.cfg.ProtocolVer, io.Discard, nil)
	s.index = tool.RegisterAllTools(srv, s.reg, nil, nil)
	return srv
}

// WithAudit attaches a per-tool audit logger. The HTTP transport
// attaches a per-request HTTPMeta via the dispatch context
// (audit.WithHTTPMeta); the stdio path passes context.Background()
// and the meta fields are omitted.
func (s *Server) WithAudit(logger *audit.Logger) {
	s.mu.Lock()
	s.audit = logger
	s.mu.Unlock()
	if logger != nil {
		s.mcpServer.WithAudit(logger, audit.ExtractPaths)
	}
}

// reaperLoop ticks every 30s and reaps idle sessions.
func (s *Server) reaperLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		s.sessions.Reap()
	}
}

// Handler returns the http.Handler that dispatches requests.
// Returned handler is safe for concurrent use.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/mcp", s.handleMCP)
	return s.withMiddleware(mux)
}

// withMiddleware wraps the mux with auth.
func (s *Server) withMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		if err := s.auth.Check(r); err != nil {
			switch {
			case errors.Is(err, ErrUnauthorized):
				http.Error(w, "unauthorized", http.StatusUnauthorized)
			case errors.Is(err, ErrAuthRequired):
				http.Error(w, "auth required on non-loopback bind", http.StatusForbidden)
			default:
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}
		next.ServeHTTP(w, r)
	})
}

// handleHealthz responds 200 OK {"status":"ok"}. Unauthenticated.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleMCP routes /mcp across POST/GET/DELETE.
func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.handlePost(w, r)
	case http.MethodGet:
		s.handleGet(w, r)
	case http.MethodDelete:
		s.handleDelete(w, r)
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handlePost dispatches a JSON-RPC request into the shared
// *mcp.Server. On `initialize`, it allocates a fresh session
// and stamps the Mcp-Session-Id header.
func (s *Server) handlePost(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	var req mcp.Request
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSONRPCError(w, nil, -32700, "parse error", err.Error())
		return
	}
	if req.JSONRPC != "2.0" {
		writeJSONRPCError(w, req.ID, -32600, "jsonrpc must be 2.0", nil)
		return
	}

	// Method-version gate (skip for initialize, which establishes
	// the session and may not echo the protocol version yet).
	if req.Method != "initialize" {
		if SessionIDFromRequest(r) == "" {
			http.Error(w, "missing Mcp-Session-Id", http.StatusBadRequest)
			return
		}
		if ProtocolVersionFromRequest(r) != s.cfg.ProtocolVer {
			http.Error(w,
				fmt.Sprintf("unsupported protocol version; server expects %s", s.cfg.ProtocolVer),
				http.StatusBadRequest)
			return
		}
	}

	// initialize: allocate session, dispatch.
	if req.Method == "initialize" {
		sess, err := s.sessions.Allocate(r.RemoteAddr)
		if err != nil {
			if errors.Is(err, ErrSessionCap) {
				writeJSONRPCErrorWithData(w, req.ID, -32603, "session cap reached", map[string]any{"reason": "session_cap"})
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set(HeaderSessionID, sess.ID)

		// Dispatch initialize with the per-request context
		// carrying HTTPMeta so the audit shim (when enabled)
		// captures session_id + client_addr.
		ctx := audit.WithHTTPMeta(r.Context(), audit.HTTPMeta{
			SessionID:  sess.ID,
			ClientAddr: sess.ClientAddr,
		})
		resp := s.mcpServer.Dispatch(ctx, req)
		writeJSONRPCResponse(w, resp)
		return
	}

	// Non-initialize: session is required.
	sid := SessionIDFromRequest(r)
	sess, ok := s.sessions.Lookup(sid)
	if !ok {
		http.Error(w, "unknown session", http.StatusBadRequest)
		return
	}
	s.sessions.Touch(sid)

	// Dispatch with the per-request context carrying HTTPMeta.
	ctx := audit.WithHTTPMeta(r.Context(), audit.HTTPMeta{
		SessionID:  sess.ID,
		ClientAddr: sess.ClientAddr,
	})
	resp := s.mcpServer.Dispatch(ctx, req)

	if AcceptsEventStream(r) {
		writeSSEResponse(w, resp)
	} else {
		writeJSONRPCResponse(w, resp)
	}
}

// handleGet opens an SSE stream for server-initiated messages.
// The session's ctx is the lifecycle; when the session is
// reaped / DELETEd / the daemon shuts down, the stream emits
// the server_shutdown event and closes.
func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	sid := SessionIDFromRequest(r)
	sess, ok := s.sessions.Lookup(sid)
	if !ok {
		http.Error(w, "unknown session", http.StatusBadRequest)
		return
	}
	if ProtocolVersionFromRequest(r) != s.cfg.ProtocolVer {
		http.Error(w,
			fmt.Sprintf("unsupported protocol version; server expects %s", s.cfg.ProtocolVer),
			http.StatusBadRequest)
		return
	}
	s.sessions.Touch(sid)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	sw := newSSEWriterWithContext(sess.Ctx(), w)
	_ = sw.WriteComment("open")
	if flusher != nil {
		flusher.Flush()
	}

	// Block until EITHER the session is cancelled (DELETE, reap,
	// shutdown) OR the client disconnects.
	select {
	case <-sess.Ctx().Done():
		// Use a fresh SSE writer for the shutdown event because
		// the session ctx (which the original writer observed) is
		// the very thing that triggered this path.
		shutdownSw := newSSEWriter(w)
		_ = shutdownSw.WriteShutdownEvent()
	case <-r.Context().Done():
		// Client went away; no shutdown event needed.
	}
	if flusher != nil {
		flusher.Flush()
	}
}

// handleDelete terminates a session. Returns 204 on success,
// 400 on missing/unknown session.
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	sid := SessionIDFromRequest(r)
	if sid == "" {
		http.Error(w, "missing Mcp-Session-Id", http.StatusBadRequest)
		return
	}
	if _, ok := s.sessions.Lookup(sid); !ok {
		http.Error(w, "unknown session", http.StatusBadRequest)
		return
	}
	s.sessions.Delete(sid)
	w.WriteHeader(http.StatusNoContent)
}

// ListenAndServe binds the configured address and serves until
// Shutdown is called or the listener fails.
func (s *Server) ListenAndServe() error {
	addr := netJoinHostPort(s.cfg.Bind, s.cfg.Port)
	s.httpServer = &http.Server{
		Addr:    addr,
		Handler: s.Handler(),
	}
	return s.httpServer.ListenAndServe()
}

// Shutdown stops accepting new connections, drains in-flight
// requests up to cfg.ShutdownGrace, cancels sessions, and
// ForceClose's the warm project index (reaping pinned LSP
// children). Idempotent.
func (s *Server) Shutdown(ctx context.Context) error {
	var errs []error
	if s.httpServer != nil {
		if err := s.httpServer.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	s.sessions.Stop()
	if s.index != nil {
		if err := s.index.Close(); err != nil {
			errs = append(errs, err)
		}
		project.BindIndex(s.reg, nil)
		s.index = nil
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// netJoinHostPort joins host:port the same way as net.JoinHostPort
// (which is what http.Server expects) but tolerates port=0 (lets
// the kernel pick — useful in tests).
func netJoinHostPort(host string, port int) string {
	if port == 0 {
		return host + ":0"
	}
	return host + ":" + strconv.Itoa(port)
}

// writeJSONRPCResponse writes a JSON-RPC Response as JSON.
// Notifications (no ID, no error) become 204 No Content.
func writeJSONRPCResponse(w http.ResponseWriter, r mcp.Response) {
	if len(r.ID) == 0 && r.Error == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(r)
}

func writeJSONRPCError(w http.ResponseWriter, id json.RawMessage, code int, msg string, data any) {
	resp := mcp.EncodeError(id, code, msg, data)
	writeJSONRPCResponse(w, resp)
}

func writeJSONRPCErrorWithData(w http.ResponseWriter, id json.RawMessage, code int, msg string, data map[string]any) {
	resp := mcp.EncodeError(id, code, msg, nil)
	if resp.Error != nil {
		resp.Error.Data = data
	}
	writeJSONRPCResponse(w, resp)
}

// writeSSEResponse wraps a JSON-RPC response in a single SSE event.
func writeSSEResponse(w http.ResponseWriter, r mcp.Response) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	sw := newSSEWriter(w)
	body, _ := json.Marshal(r)
	_ = sw.WriteEvent("message", string(body))
	_ = sw.WriteComment("end")
}