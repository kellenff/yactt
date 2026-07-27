# Persistent MCP server over HTTP streaming — design (revised for file://)

> **Status:** approved 2026-07-13 (post-rebase). Implementation lives on `persistent-mcp-http-server`.
> **Scope:** `yactt mcp serve-http` — a persistent multi-session HTTP daemon speaking MCP `2025-03-26` Streamable HTTP. The stdio transport is a frozen wire contract and is **not** touched.
> **Protocol version:** `2025-03-26` (Streamable HTTP).
> **Replaces:** nothing. Adds a peer transport alongside stdio.

> **Revisions from initial draft (2026-07-13 morning):** the upstream `file://` project-URI migration (commits landed on `main` after the initial draft) reshaped how tools resolve repositories — every code-intel tool now resolves its project via `project.Resolve(reg, args.Project)` on every call rather than holding a pre-loaded `*store.Repo`. This invalidates the in-memory `RepoCache` design from the initial draft and simplifies the URL surface (one `/mcp` endpoint instead of `/mcp/registry` + `/mcp/repos/{id}`). The HTTP wire shape, session lifecycle, auth, graceful shutdown, and `session_id`/`client_addr` audit attribution are unchanged.

---

## 1. Motivation

`yactt mcp serve` is stdio-only and one-process-per-client. As agents run longer and per-repo graphs grow, there's no way for multiple agents to share a parsed graph, and reconnecting re-parses.

The MCP `2025-03-26` revision added the **Streamable HTTP** transport — POST + GET + DELETE on a single endpoint per session, SSE for server-initiated streams, `Mcp-Session-Id` for session continuity. This spec adds a persistent HTTP daemon that:

1. Holds one shared `*mcp.Server` per daemon (per-process).
2. Serves many concurrent MCP clients over Streamable HTTP.
3. Lets each call resolve its project via the registry (file:// URI in args).
4. Preserves the stdio transport, registry file shape, and audit log shape — additive only.

---

## 2. Goals & non-goals

**Goals**

- One daemon process, many concurrent clients; one shared `*mcp.Server` reused across all sessions.
- MCP `2025-03-26` Streamable HTTP wire shape, conformance-tested.
- Localhost-only by default, opt-in token + bind for hardening.
- Add zero new module dependencies (`log/slog` is stdlib).
- Stdio transport, plugin configs, registry file, and audit log: additive changes only.

**Non-goals**

- WebSocket transport. Cluster / multi-host mode. TLS termination. Per-repo auth tokens. Server-initiated notifications. Hot registry reload. LRU/size-cap on the repo cache.
- Changing the stdio transport's behavior or wire shape in any way.
- Bumping the stdio transport's advertised `protocolVersion` (stays `2024-11-05`).
- In-memory parsed-graph cache — deferred; tools already cache via the disk cache plus per-call `project.Resolve`.

---

## 3. Architecture

```
┌────────────────────────────────────────────────────────────────┐
│ cmd/yactt/main.go                                              │
│   └── runMCPServeHTTP() — flag parsing, signal handling,       │
│       registry load, daemon supervision                        │
├────────────────────────────────────────────────────────────────┤
│ internal/mcp/transport/http/        (NEW package)              │
│   ├── transport.go — net/http server, single /mcp route,       │
│   │   auth middleware, session manager integration             │
│   ├── session.go   — Mcp-Session-Id lifecycle, idle reap       │
│   ├── headers.go   — header parsing (Mcp-Session-Id,            │
│   │                  Mcp-Protocol-Version, Accept)              │
│   ├── sse.go       — SSE writer                                │
│   └── auth.go      — token + loopback gating                    │
├────────────────────────────────────────────────────────────────┤
│ internal/mcp/server.go            (UNCHANGED public surface)   │
│   Reuses the dispatch loop + tool registry. The HTTP transport │
│   calls Server.Dispatch(ctx, req) — same API as stdio's        │
│   Serve(ctx). auditLogger gains ctx + HTTPMeta (additive).     │
├────────────────────────────────────────────────────────────────┤
│ internal/tool/register.go           (NEW — extracted)          │
│   RegisterAllTools + RegisterPersistedQuery — shared by both   │
│   stdio (runMCPServe) and HTTP (transport).                     │
├────────────────────────────────────────────────────────────────┤
│ internal/registry, internal/audit, internal/mcp, internal/tool  │
│ (existing — additive changes only)                            │
└────────────────────────────────────────────────────────────────┘
```

**Locked-in choices**

| Decision | Choice |
|---|---|
| Transport | MCP Streamable HTTP, protocol version `2025-03-26` |
| Daemon scope | Multi-repo: registry-driven via `*registry.Registry` shared by all tools |
| URL surface | `POST\|GET\|DELETE /mcp` (single endpoint) + `GET /healthz` |
| Bind / auth | `127.0.0.1` default; opt-in `--bind=0.0.0.0` and `--auth-token=<t>` |
| CLI | `yactt mcp serve-http [--port=...] [--bind=...] [--auth-token=...] ...` |
| Server model | One shared `*mcp.Server` per daemon (built once at startup) |
| Stdio transport | Unchanged (still `2024-11-05`) |
| Logger | `log/slog` not used directly; audit emit stays JSON-line |
| New deps | None |

**Three principles**

1. **The stdio transport is a frozen wire contract** — we don't change its behavior, error codes, or response shapes.
2. **Tools resolve projects per-call** via the registry; the daemon doesn't pin parsed graphs in memory (the on-disk cache covers warm-path latency).
3. **Sessions are dispatch metadata** — one `*mcp.Server`, many concurrent clients, HTTPMeta attached via `context.Context` per request.

---

## 4. Wire protocol — Streamable HTTP transport

### Endpoints

| Method + Path | Purpose | Session required? |
|---|---|---|
| `POST /mcp` | Client → server request | No (created on `initialize`) |
| `GET /mcp` | Open SSE stream for server-initiated messages | Yes |
| `DELETE /mcp` | Terminate a session explicitly | Yes |
| `GET /healthz` | Liveness probe — `200 OK {"status":"ok"}` | No |

### Headers

- `Content-Type: application/json` on POST bodies.
- `Accept: application/json, text/event-stream` on POST to enable both response modes.
- `Mcp-Session-Id: <uuid>` — server-set on the `initialize` response; client must echo on every subsequent POST/GET/DELETE.
- `Mcp-Protocol-Version: 2025-03-26` — client-set on every request after `initialize`. Missing → `400`. Mismatch → `400`.
- `Authorization: Bearer <token>` — required only when `--auth-token` was set. Loopback + no token → no header check.
- `Last-Event-ID` on GET — supported for SSE resumption.

### Response modes for POST

- **JSON-only**: `200 OK` + `Content-Type: application/json` + single JSON-RPC response object. Default.
- **SSE**: `200 OK` + `Content-Type: text/event-stream` + event stream. Returned when the client sets `Accept: text/event-stream`, even for synchronous results.
- **Error**: `4xx`/`5xx` + JSON-RPC error envelope.

### `initialize` flow

1. Client `POST`s without `Mcp-Session-Id`. Server allocates a UUID, registers a fresh session record, responds with `Mcp-Session-Id` and `200 OK` JSON-RPC `result` carrying `protocolVersion`, `serverInfo`, `capabilities`.
2. Client echoes `Mcp-Session-Id` + `Mcp-Protocol-Version` on every subsequent request.
3. Tool calls resolve their project via `args.project` (a `file://` URI) on the code-intel tools; registry tools (`list_projects`, `index_repository`, `index_status`, `delete_project`) don't take a project.

### Unknown repo-id / missing project

Tool handlers surface project-resolution failures as JSON-RPC `-32603` with `data.loadError` (or `data.notIndexed` depending on the failure). The HTTP transport doesn't pre-validate — projects are resolved per-call inside each tool handler.

### Session termination

- Client `DELETE`s with `Mcp-Session-Id` → server removes the session, returns `204 No Content`.
- SSE client disconnect → server reaps the session on the next event-loop tick.
- Daemon SIGTERM → server emits a final SSE event `event: server_shutdown data: {"reason":"shutdown"}` on every open stream, then closes. Active POST requests get up to `--shutdown-grace`.

### Rejected methods

- Anything outside `POST/GET/DELETE` on `/mcp` → `405 Method Not Allowed`.
- Anything outside `GET` on `/healthz` → `405 Method Not Allowed`.
- Anything not under `/mcp` or `/healthz` → `404 Not Found`.

---

## 5. State — sessions and lifecycle

### Sessions

A session is the server-side state for one MCP client connection, identified by `Mcp-Session-Id` (UUID v4). Each session owns:

- The Mcp-Session-Id UUID
- The remote `host:port`
- A child context (cancelled on reap / DELETE / shutdown)
- A monotonically-updated `LastSeen` timestamp
- An in-flight request counter

**Lifecycle:**

```
allocate (initialize) ──► active ──► idle (no request for 5 min) ──► reap
                                  ├──► explicit DELETE ──► reap
                                  ├──► SSE client disconnect ──► reap
                                  └──► daemon shutdown ──► drain → reap
```

- **Idle timeout:** 5 minutes. Reset on every authenticated request.
- **Max concurrent sessions:** 256. New `initialize` beyond cap → HTTP `503` with JSON-RPC `-32603` carrying `data.reason="session_cap"`.
- **Map:** `sync.Map` for reads; exclusive mutex for reap iteration. Reap goroutine ticks every 30 s.

### Per-request dispatch flow

1. Transport looks up the session by `Mcp-Session-Id` (or allocates one for `initialize`).
2. Transport builds a child context with `audit.WithHTTPMeta(ctx, audit.HTTPMeta{SessionID, ClientAddr})`.
3. Transport calls `server.Dispatch(ctx, req)` — the existing public API, only the audit shim's signature grew.
4. `Server.dispatch` runs as today. When the audit emit fires, the logger pulls `HTTPMeta` from the context to populate `session_id` + `client_addr` on the JSON line.
5. Response goes back through the transport.

Tool handlers receive the same context but don't need to know about HTTP. They keep working as-is.

### Concurrency

- One goroutine per HTTP request.
- One reap goroutine per daemon (30 s tick).
- One shutdown-drain watcher per daemon.

### Graceful shutdown

On `SIGTERM`/`SIGINT`:

1. `http.Server.Shutdown(ctx)` — stop accepting new TCP; finish in-flight requests up to `--shutdown-grace` (default 10 s).
2. Send `event: server_shutdown` SSE event to every open stream, then close.
3. Cancel sessions via the session manager's parent context.
4. Exit 0.

---

## 6. CLI surface, flags, audit extensions

### Subcommand

```
yactt mcp serve-http [--port=...] [--bind=...] [--auth-token=...] \
                     [--audit-log=...] [--registry=...]              \
                     [--shutdown-grace=...] [--max-sessions=...]     \
                     [--idle-timeout=...] [--help]
```

Subcommand lives under `mcp` so all transports stay grouped. Flag parsing follows the existing positional/`--key=value`/`--key value` style from `runChunk` / `runHybrid` / `runMCPServe`.

### Flags

| Flag | Default | Meaning |
|---|---|---|
| `--port=<n>` | `8080` | TCP port. `0` lets the kernel pick (printed to stderr). |
| `--bind=<addr>` | `127.0.0.1` | Bind address. Set to `0.0.0.0` for non-loopback. |
| `--auth-token=<t>` | `""` | When non-empty, every request must carry `Authorization: Bearer <t>`. Compared in constant time. |
| `--audit-log=<path>` | `""` | When non-empty, append one JSON line per `tools/call` to `<path>` (mode `0600`). |
| `--registry=<path>` | `$XDG_CACHE_HOME/yactt/projects.json` | Registry file location. Override for tests / multi-tenant. |
| `--shutdown-grace=<dur>` | `10s` | Grace window for in-flight requests on SIGTERM. `time.ParseDuration` syntax. |
| `--max-sessions=<n>` | `256` | Cap on concurrent sessions. |
| `--idle-timeout=<dur>` | `5m` | Idle reap threshold. |

### Startup emit + warning

Single-line startup emit on stderr, always:

```
yactt mcp serve-http listening on 127.0.0.1:8080 protocol=2025-03-26 registry=/Users/.../projects.json sessions=256 idle=5m grace=10s
```

When `--bind` is non-loopback and `--auth-token` is empty:

```
WARNING: bound to 0.0.0.0 without --auth-token; the daemon is unauthenticated. Set --auth-token or reverse-proxy through an authenticated gateway.
```

Not a refusal — operators sometimes tunnel via SSH or sit behind a reverse proxy that authenticates.

### Audit extensions

`internal/audit.HTTPMeta` struct:

```go
type HTTPMeta struct {
    SessionID  string
    ClientAddr string
}
```

`auditLogger.LogToolCall` gains a `ctx context.Context` first parameter. Stdio path passes `context.Background()`; HTTP path attaches a `HTTPMeta` via `audit.WithHTTPMeta(ctx, meta)`. The audit shim reads the meta from the context when emitting. New optional fields on the `tool_call` JSON line:

```json
{
  "event": "tool_call",
  "tool": "list_projects",
  "input_paths": [],
  "output_bytes": 1234,
  "duration_ms": 12,
  "is_error": false,
  "session_id": "c088c25084fdf1a29303b28d7a77c0e1",
  "client_addr": "127.0.0.1:54321"
}
```

`session_id` and `client_addr` are omitted (via `omitempty`) for stdio sessions.

---

## 7. Testing strategy

| Layer | Test type | Coverage goal |
|---|---|---|
| `internal/mcp/transport/http` routing | `httptest.NewServer` integration | Every route × every method returns the right status + body shape. |
| Session lifecycle | Unit (table-driven) | Allocate, reuse, idle reap, explicit `DELETE`, SSE disconnect, shutdown drain. |
| SSE writer | Unit | Open / data / comment / shutdown-event framing, ctx cancel. |
| Auth middleware | Unit | `--auth-token` enforced on POST/GET/DELETE; loopback + no token = open; non-loopback + no token = 403. |
| Header validation | Unit | `Mcp-Session-Id`, `Mcp-Protocol-Version`, `Accept` parsing. |
| Audit emit | Unit + golden file | `session_id`, `client_addr` populated for HTTP sessions, absent for stdio. |
| Streamable HTTP wire shape | `httptest` + canonical request/response recordings | Header validation, SSE event format, `Last-Event-ID` resumption. |
| Graceful shutdown | Integration with `signal.NotifyContext` | SIGTERM drains in-flight, emits SSE event, closes sessions. |
| Stdio regression | Existing `internal/mcp/server_test.go` | Untouched and green — proves stdio wire shape unchanged. |
| Smoke | `cmd/yactt` | `yactt mcp serve-http --help` exits 0; warning emitted when bind is non-loopback without auth. |

End-to-end conformance test replays the canonical client session (`initialize → notifications/initialized → tools/list → tools/call list_projects → DELETE`) and verifies each step's wire shape.

---

## 8. Backwards compatibility & migration

- **`yactt mcp serve`** (stdio) — unchanged. Same flags, same behavior, same protocol version (`2024-11-05`).
- **Registry file** — unchanged.
- **Audit log** — additive only. `session_id` and `client_addr` are optional and absent in stdio mode. Existing scripts that parse the JSON line still work.
- **Plugin configs** — no change to `.mcp.json`, the Claude plugin, the Pi extension, or the Junie extension. They continue to use stdio.
- **Dependency surface** — `log/slog` is stdlib; no new module requires. The CLI parser migration (hand-rolled → Cobra) bumped the direct-dep count to 2 (tree-sitter + cobra), with two indirect deps (mousetrap, pflag). The README's trust strip and receipts were updated to match; the snake_case "1 dep" → "2 deps" is the only user-facing change. `govulncheck ./...` is clean.

**Operator migration path:**

- On stdio today: do nothing. Auto-updates flow through the plugin/extension channels.
- Adopting HTTP: install the new release, run `yactt mcp serve-http --port=8080`, point the client at `http://127.0.0.1:8080/mcp`.
- Documented in `README.md` (new "For HTTP-capable clients" subsection) and `docs/security.md` (auth posture).

---

## 9. Out of scope (YAGNI)

Deferred, with a note on what would unlock them:

- **WebSocket transport.** Streamable HTTP over POST/GET/DELETE covers the same surface; WS would add a parallel protocol without a clear win.
- **Per-repo auth tokens.** Single global `--auth-token` only. Unlocked when multi-tenant deployments land.
- **Hot reload of the registry.** Trivial follow-up: SIGHUP handler that re-reads the file + reconciles the cache.
- **In-memory parsed-graph cache.** The disk cache plus per-call `project.Resolve` covers warm-path latency; revisit when benchmark shows a need.
- **LRU / size cap on the repo cache.** N/A — there is no in-memory cache to cap.
- **Server-initiated notifications.** No use today; SSE write path is wired so adding later is local.
- **TLS termination.** Operator's responsibility — reverse proxy (Caddy, nginx, Cloudflare Tunnel) does the cert.
- **Cluster mode / multi-host.** Single-host daemon. The registry file is the only shared state.

---

## 10. Open questions / follow-ups

- **`index_repository` warm-load via HTTP.** Today the registry tool walks the repo synchronously. With HTTP, an agent could fire-and-forget and poll `index_status`. Future tool param, not this spec.
- **Streaming responses for long tool calls.** Most tools are fast enough that JSON-only is fine. `query_graph` with `depth=5` on a large graph could push limits; revisit when the benchmark shows it.