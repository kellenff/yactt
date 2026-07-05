// stubserver is a minimal JSON-RPC 2.0 stub used as a fake language server
// for collaboration tests in the parent lsp package. It speaks the LSP
// `initialize` / `initialized` handshake, then echoes back canned replies
// per the command-line args:
//
//	stubserver -name=test -version=v1 -fail=0 -sleep=0 -bad-init=0 -refs-file=PATH -refs-line=N -refs-col=N -log-stderr
//
//	-name           server name reported in InitializeResult.ServerInfo
//	-version        server version reported there
//	-fail=N         send a JSON-RPC error reply to request #N (1-indexed)
//	-sleep=N        sleep N milliseconds before replying to request #N
//	-bad-init       send an invalid shape on the initialize reply
//	-no-shutdown    never reply to shutdown; hang until killed
//	-hang-forever   never exit (sleeps the duration of the test)
//	-refs-file=PATH file URI to return in the canned textDocument/references reply
//	-refs-line=N    line number to return in the canned reply
//	-refs-col=N     character column to return in the canned reply
//	-log-stderr     pass through to stderr (default: discard)
//
// Reads LSP-framed messages from stdin (`Content-Length: N\r\n\r\n<N-byte
// JSON body>`) and writes one reply per request to stdout.
//
// Built and invoked only from internal/lsp/client_test.go and tests in
// internal/store and internal/tool that wire the LSP stub via
// `lsp.StartCommand`. Not a public tool.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

type frame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *wireError      `json:"error,omitempty"`
}

type wireError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// referencesParams mirrors the subset of textDocument/references params
// the stub needs to compose a Location reply. The full param shape is
// bigger; we just want the URI/line/col we were asked about so we can
// echo them back unchanged when no canned URI is configured.
type referencesParams struct {
	TextDocument struct {
		URI string `json:"uri"`
	} `json:"textDocument"`
	Position struct {
		Line      int `json:"line"`
		Character int `json:"character"`
	} `json:"position"`
}

func main() {
	fmt.Fprintf(os.Stderr, "[stubserver] enter; os.Args=%v\n", os.Args)
	name := flag.String("name", "stub-server", "server name in InitializeResult")
	version := flag.String("version", "v0.0.0", "server version in InitializeResult")
	failOn := flag.String("fail", "", "comma-separated 1-indexed request ids to fail")
	sleepOn := flag.String("sleep", "", "comma-separated id:ms pairs to sleep before replying")
	emptyHover := flag.String("hover-empty", "", "comma-separated request ids for which textDocument/hover returns nil")
	refsFile := flag.String("refs-file", "", "file URI to return in canned textDocument/references replies (empty = echo request URI)")
	refsLine := flag.Int("refs-line", 0, "line coordinate to return in canned textDocument/references replies")
	refsCol := flag.Int("refs-col", 0, "character coordinate to return in canned textDocument/references replies")
	badInit := flag.Bool("bad-init", false, "respond to initialize with an invalid shape")
	noShutdown := flag.Bool("no-shutdown", false, "ignore shutdown instead of exiting")
	hangForever := flag.Bool("hang-forever", false, "sleep until killed")
	logStderr := flag.Bool("log-stderr", false, "copy stderr to the controlling terminal for debugging")
	flag.Parse()

	fmt.Fprintf(os.Stderr, "[stubserver] up; argv=%v\n", os.Args[1:])

	if *logStderr {
		// Already inherited by default; this is an explicit marker for tests.
	}
	if *hangForever {
		time.Sleep(1 * time.Hour)
		return
	}

	failMap := parseIDList(*failOn)
	sleepMap := parseIDPairList(*sleepOn)
	emptyHoverMap := parseIDList(*emptyHover)

	// Special handling: if request id 1 (initialize) is in failMap, refuse.
	// The id is set by the client, but our protocol always expects it to
	// be an incrementing counter. Use the value the client sends.

	br := bufio.NewReader(os.Stdin)
	stdout := bufio.NewWriter(os.Stdout)
	defer stdout.Flush()

	var reqCount int
	for {
		// Read headers until blank line.
		var contentLen int
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				break
			}
			colon := strings.IndexByte(line, ':')
			if colon < 0 {
				continue
			}
			name := strings.ToLower(strings.TrimSpace(line[:colon]))
			value := strings.TrimSpace(line[colon+1:])
			if name == "content-length" {
				n, perr := strconv.Atoi(value)
				if perr != nil {
					return
				}
				contentLen = n
			}
		}
		if contentLen <= 0 {
			return
		}
		body := make([]byte, contentLen)
		if _, err := readFull(br, body); err != nil {
			return
		}
		var f frame
		if err := json.Unmarshal(body, &f); err != nil {
			fmt.Fprintf(os.Stderr, "stubserver: bad frame: %v\n", err)
			continue
		}
		if f.ID == nil {
			// Notification: skip.
			continue
		}
		reqCount++
		id := *f.ID

		// Honor configured delay.
		if d, ok := sleepMap[id]; ok {
			time.Sleep(d)
		}

		switch f.Method {
		case "initialize":
			if *badInit {
				writeFrame(stdout, map[string]any{
					"jsonrpc": "2.0",
					"id":      id,
					"result":  "this is not a valid InitializeResult",
				})
				continue
			}
			writeFrame(stdout, map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result": map[string]any{
					"serverInfo": map[string]string{
						"name":    *name,
						"version": *version,
					},
					"capabilities": map[string]any{},
				},
			})
		case "textDocument/references":
			var rp referencesParams
			_ = json.Unmarshal(f.Params, &rp)
			uri := *refsFile
			if uri == "" {
				uri = rp.TextDocument.URI
			}
			line := *refsLine
			col := *refsCol
			// Default: if no canned coordinates were configured, echo the
			// request's own position. Tests configure explicit coords when
			// they need a specific caller site.
			if line == 0 && col == 0 {
				line = rp.Position.Line
				col = rp.Position.Character
			}
			writeFrame(stdout, map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result": []map[string]any{
					{
						"uri": uri,
						"range": map[string]any{
							"start": map[string]any{"line": line, "character": col},
							"end":   map[string]any{"line": line, "character": col + 1},
						},
					},
				},
			})
		case "textDocument/hover":
			if _, empty := emptyHoverMap[id]; empty {
				writeFrame(stdout, map[string]any{
					"jsonrpc": "2.0",
					"id":      id,
					"result":  nil,
				})
				continue
			}
			writeFrame(stdout, map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result": map[string]any{
					"contents": map[string]any{
						"kind":  "markdown",
						"value": "func Login(user string, pass string) (Session, error)",
					},
					"range": map[string]any{
						"start": map[string]any{"line": 0, "character": 0},
						"end":   map[string]any{"line": 0, "character": 5},
					},
				},
			})
		case "shutdown":
			writeFrame(stdout, map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result":  nil,
			})
			if *noShutdown {
				time.Sleep(1 * time.Hour)
				return
			}
		case "exit":
			os.Exit(0)
		default:
			if _, bad := failMap[id]; bad {
				writeFrame(stdout, map[string]any{
					"jsonrpc": "2.0",
					"id":      id,
					"error": map[string]any{
						"code":    -32601,
						"message": "method not found",
					},
				})
				continue
			}
			writeFrame(stdout, map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result": map[string]any{
					"echo":  true,
					"count": reqCount,
				},
			})
		}
	}
}

// readFull reads exactly n bytes from r into buf.
func readFull(r *bufio.Reader, buf []byte) (int, error) {
	return io.ReadFull(r, buf)
}

// writeFrame writes one LSP-framed response to w. Flushes after every
// write so callers in another goroutine do not block on buffered writes.
func writeFrame(w *bufio.Writer, payload map[string]any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Content-Length: %d\r\n\r\n%s", len(b), b); err != nil {
		return err
	}
	return w.Flush()
}

func parseIDList(s string) map[int64]bool {
	out := make(map[int64]bool)
	if s == "" {
		return out
	}
	for _, p := range splitCSV(s) {
		if n, err := strconv.ParseInt(p, 10, 64); err == nil {
			out[n] = true
		}
	}
	return out
}

func parseIDPairList(s string) map[int64]time.Duration {
	out := make(map[int64]time.Duration)
	if s == "" {
		return out
	}
	for _, p := range splitCSV(s) {
		var id int64
		var ms int
		if _, err := fmt.Sscanf(p, "%d:%d", &id, &ms); err == nil {
			out[id] = time.Duration(ms) * time.Millisecond
		}
	}
	return out
}

func splitCSV(s string) []string {
	out := []string{}
	cur := ""
	for _, r := range s {
		if r == ',' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
		} else {
			cur += string(r)
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
