package http

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/kellenff/yactt/internal/registry"
)

// External-client benchmarks: a dedicated *http.Client talks to a real
// loopback TCP listener serving the MCP HTTP daemon. This is closer to
// how an MCP HTTP client (or agent harness) reaches `serve-http` than
// the httptest benches in transport_bench_test.go, which share the
// process but skip a full dial/keep-alive path.
//
// Same project-size matrix as BenchmarkHTTP_ToolsCall:
//   - small  — repofixture
//   - medium — genfixture (100 files)
//
// Run:
//
//	go test -bench='^BenchmarkHTTPClient' -benchmem ./internal/mcp/transport/http/...

// mcpHTTPClient is a minimal Streamable-HTTP MCP client used only by
// benchmarks. It owns one *http.Client with a keep-alive Transport so
// connection setup is paid once, matching a long-lived agent session.
type mcpHTTPClient struct {
	client  *http.Client
	baseURL string // e.g. http://127.0.0.1:PORT/mcp
	sid     string
}

func newMCPHTTPClient(baseURL string) *mcpHTTPClient {
	return &mcpHTTPClient{
		client: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        8,
				MaxIdleConnsPerHost: 8,
				IdleConnTimeout:     90 * time.Second,
				DisableKeepAlives:   false,
			},
		},
		baseURL: baseURL,
	}
}

func (c *mcpHTTPClient) closeIdle() {
	c.client.CloseIdleConnections()
}

func (c *mcpHTTPClient) post(body []byte) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPost, c.baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.sid != "" {
		req.Header.Set(HeaderSessionID, c.sid)
		req.Header.Set(HeaderProtocolVersion, ExpectedProtocol)
	}
	return c.client.Do(req)
}

func (c *mcpHTTPClient) initialize() error {
	const body = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"bench-client","version":"1.0"}}}`
	resp, err := c.post([]byte(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	sid := resp.Header.Get(HeaderSessionID)
	if sid == "" {
		return fmt.Errorf("initialize missing %s", HeaderSessionID)
	}
	c.sid = sid
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func (c *mcpHTTPClient) deleteSession() error {
	req, err := http.NewRequest(http.MethodDelete, c.baseURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set(HeaderSessionID, c.sid)
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("DELETE status = %d, want 204", resp.StatusCode)
	}
	c.sid = ""
	return nil
}

func (c *mcpHTTPClient) toolsList() error {
	const body = `{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{}}`
	resp, err := c.post([]byte(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("tools/list status = %d", resp.StatusCode)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func (c *mcpHTTPClient) toolsCall(name, argsJSON string) error {
	body := fmt.Sprintf(
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":%q,"arguments":%s}}`,
		name, argsJSON,
	)
	resp, err := c.post([]byte(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("tools/call %s status = %d", name, resp.StatusCode)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// clientBenchDaemon is a daemon on a real loopback TCP port plus an
// MCP HTTP client pointed at it.
type clientBenchDaemon struct {
	daemon *Server
	http   *http.Server
	ln     net.Listener
	client *mcpHTTPClient
	url    string
}

func startClientBenchDaemon(b *testing.B, fx benchFixture) *clientBenchDaemon {
	b.Helper()
	cfg := ServerConfig{
		Token:        "",
		Bind:         "127.0.0.1",
		Port:         0,
		MaxSessions:  64,
		IdleTimeout:  5 * time.Minute,
		ProtocolName: "yactt-bench",
		Version:      "test",
		ProtocolVer:  ExpectedProtocol,
	}
	dir := b.TempDir()
	reg := registry.New(filepath.Join(dir, "projects.json"))
	if err := reg.Upsert(registry.Entry{
		Name: filepath.Base(fx.root),
		Path: fx.root,
	}); err != nil {
		b.Fatalf("registry upsert: %v", err)
	}

	daemon := NewServer(cfg, reg)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatalf("listen: %v", err)
	}
	httpSrv := &http.Server{Handler: daemon.Handler()}
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- httpSrv.Serve(ln)
	}()

	url := "http://" + ln.Addr().String() + "/mcp"
	client := newMCPHTTPClient(url)

	b.Cleanup(func() {
		client.closeIdle()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
		_ = daemon.Shutdown(shutdownCtx)
		select {
		case <-serveErr:
		case <-time.After(2 * time.Second):
		}
	})

	if err := client.initialize(); err != nil {
		b.Fatalf("initialize: %v", err)
	}
	if err := client.toolsCall("index_repository",
		fmt.Sprintf(`{"project":%q}`, fx.projectURI)); err != nil {
		b.Fatalf("warm index_repository: %v", err)
	}

	return &clientBenchDaemon{
		daemon: daemon,
		http:   httpSrv,
		ln:     ln,
		client: client,
		url:    url,
	}
}

// BenchmarkHTTPClient_Initialize measures initialize via a dedicated
// *http.Client against a real TCP listener. Sessions are DELETEd
// outside the timed path so MaxSessions is never exhausted.
func BenchmarkHTTPClient_Initialize(b *testing.B) {
	cfg := ServerConfig{
		Token:        "",
		Bind:         "127.0.0.1",
		Port:         0,
		MaxSessions:  256,
		IdleTimeout:  5 * time.Minute,
		ProtocolName: "yactt-bench",
		Version:      "test",
		ProtocolVer:  ExpectedProtocol,
	}
	dir := b.TempDir()
	reg := registry.New(filepath.Join(dir, "projects.json"))
	daemon := NewServer(cfg, reg)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatalf("listen: %v", err)
	}
	httpSrv := &http.Server{Handler: daemon.Handler()}
	go func() { _ = httpSrv.Serve(ln) }()
	b.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
		_ = daemon.Shutdown(shutdownCtx)
	})

	url := "http://" + ln.Addr().String() + "/mcp"
	client := newMCPHTTPClient(url)
	b.Cleanup(client.closeIdle)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := client.initialize(); err != nil {
			b.Fatalf("initialize: %v", err)
		}
		b.StopTimer()
		if err := client.deleteSession(); err != nil {
			b.Fatalf("delete session: %v", err)
		}
		b.StartTimer()
	}
}

// BenchmarkHTTPClient_ToolsList measures tools/list through the
// external Go HTTP client on an established session.
func BenchmarkHTTPClient_ToolsList(b *testing.B) {
	d := startClientBenchDaemon(b, benchSmallFixture(b))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := d.client.toolsList(); err != nil {
			b.Fatalf("tools/list: %v", err)
		}
	}
}

// BenchmarkHTTPClient_ToolsCall measures tools/call round-trips through
// a dedicated *http.Client for small and medium project sizes.
func BenchmarkHTTPClient_ToolsCall(b *testing.B) {
	fixtures := []func(*testing.B) benchFixture{
		benchSmallFixture,
		benchMediumFixture,
	}
	tools := []struct {
		name string
		call func(fx benchFixture) (tool string, args string)
	}{
		{
			name: "list_projects",
			call: func(fx benchFixture) (string, string) {
				return "list_projects", `{}`
			},
		},
		{
			name: "tree_overview",
			call: func(fx benchFixture) (string, string) {
				return "tree_overview", fmt.Sprintf(`{"project":%q,"depth":2}`, fx.projectURI)
			},
		},
		{
			name: "find_symbol",
			call: func(fx benchFixture) (string, string) {
				return "find_symbol", fmt.Sprintf(`{"project":%q,"name_path":%q,"limit":10}`, fx.projectURI, fx.findName)
			},
		},
		{
			name: "find_code",
			call: func(fx benchFixture) (string, string) {
				return "find_code", fmt.Sprintf(
					`{"project":%q,"pattern":%q,"pattern_kind":"regex","scope":%q,"limit":20}`,
					fx.projectURI, fx.findPat, fx.root,
				)
			},
		},
	}

	for _, setup := range fixtures {
		fx := setup(b)
		for _, tc := range tools {
			b.Run(fx.name+"/"+tc.name, func(b *testing.B) {
				d := startClientBenchDaemon(b, fx)
				toolName, args := tc.call(fx)
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if err := d.client.toolsCall(toolName, args); err != nil {
						b.Fatalf("tools/call: %v", err)
					}
				}
			})
		}
	}
}
