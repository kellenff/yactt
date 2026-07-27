# Persistent macOS LaunchAgent for the HTTP MCP server

**Date:** 2026-07-16
**Status:** Approved
**Scope:** Claude Code plugin bootstrap under `plugins/yactt/`

## Goal

Make the yactt Claude Code plugin use one persistent, parser-warm HTTP MCP daemon instead of spawning a cold stdio server for every client session. The plugin will support macOS only and connect to a loopback-only daemon at:

```text
http://127.0.0.1:57812/mcp
```

The installer will create and maintain a per-user launchd configuration so the daemon starts at login, restarts after an unexpected exit, and remains available across agent restarts.

## Non-goals

- Linux service management or continued Linux plugin support.
- Network-accessible binding, authentication, or TLS.
- Changing the HTTP MCP protocol or server implementation.
- Installing a system-wide LaunchDaemon or requiring administrator privileges.
- Restarting a healthy daemon on every Claude Code SessionStart.

## Chosen approach

`plugins/yactt/scripts/install.sh` will generate and reconcile a user LaunchAgent. A checked-in plist template and an ephemeral `launchctl submit` job were rejected:

- A template still needs user-specific path substitution and adds another packaged artifact.
- `launchctl submit` is less inspectable, less durable, and harder to update or uninstall.

Generating the plist in the existing installer keeps download verification, binary upgrades, service configuration, and service lifecycle in one idempotent bootstrap.

## Files

| Path | Change |
|---|---|
| `plugins/yactt/scripts/install.sh` | Install and reconcile the LaunchAgent after resolving the daemon binary. |
| `plugins/yactt/.mcp.json` | Replace the stdio command with the persistent HTTP endpoint. |
| `plugins/yactt/README.md` | Document macOS-only support, endpoint, service inspection, logs, updates, and uninstall. |
| `plugins/yactt/test/install.test.sh` | Add isolated shell coverage for LaunchAgent behavior. |

## LaunchAgent contract

The installer will maintain:

```text
~/Library/LaunchAgents/com.kellenff.yactt.mcp.plist
```

The plist will use label `com.kellenff.yactt.mcp` and contain these effective settings:

- `ProgramArguments`:
  1. the absolute resolved yactt binary path;
  2. `mcp`;
  3. `serve-http`;
  4. `--bind=127.0.0.1`;
  5. `--port=57812`.
- `RunAtLoad = true`.
- `KeepAlive = true`.
- `ProcessType = Background`.
- A restart throttle to avoid a tight crash loop.
- Standard output and error paths under `~/Library/Logs/yactt/`.

The daemon binds only to loopback. No auth token is needed because non-loopback access is not enabled.

The installer must XML-escape path values, write the plist to a temporary file in the destination directory, validate it with macOS `plutil`, and atomically replace the destination only when its content changed.

## Installer flow

1. Confirm the host is macOS. Any other OS gets a clear unsupported-platform error and a non-zero exit; the plugin no longer claims Linux support.
2. Resolve the executable:
   - If `${CLAUDE_PROJECT_DIR}/bin/yactt` is executable, preserve the developer escape hatch by selecting that binary and skipping release lookup/download.
   - Otherwise use the verified release binary at `$XDG_HOME/bin/yactt`, or `$HOME/.local/bin/yactt` when `XDG_HOME` is unset.
3. If using the release binary, retain the existing latest-version cache, SHA-256 verification, TOFU check, and upgrade behavior.
4. Always reconcile the LaunchAgent after a usable executable is selected, including when the release version was already current.
5. Create `~/Library/LaunchAgents` and `~/Library/Logs/yactt` as needed.
6. Generate and compare the desired plist.
7. Inspect the job with `launchctl print gui/$UID/com.kellenff.yactt.mcp`.
8. Apply the minimum lifecycle action:
   - Missing job: `launchctl bootstrap` it.
   - Changed plist: boot out the loaded job if necessary, then bootstrap the new plist.
   - Upgraded binary at the same path: restart the loaded job with `launchctl kickstart -k` so the new executable is running.
   - Unchanged configuration and binary: do nothing, preserving warm parser state.
9. Surface plist validation and launchctl failures as installer failures rather than leaving the HTTP MCP configuration pointed at a known-unavailable service.

Switching between a project-local development binary and the installed release changes `ProgramArguments`, so normal changed-plist reconciliation reloads the job.

## MCP client configuration

`plugins/yactt/.mcp.json` will become:

```json
{
  "mcpServers": {
    "yactt": {
      "type": "http",
      "url": "http://127.0.0.1:57812/mcp",
      "timeout": 30000
    }
  }
}
```

No project path belongs in the transport configuration. Tools continue selecting repositories through their per-call `file://` `project` argument and the shared registry.

## Updates and failure behavior

- A release upgrade replaces the verified binary, updates the TOFU record, and restarts the loaded LaunchAgent.
- A SessionStart with no changes leaves the daemon running without interruption.
- `KeepAlive` asks launchd to restart an unexpectedly terminated server. The throttle prevents a rapid restart loop if startup repeatedly fails.
- Binding failures, malformed plist output, and launchctl failures are visible through the installer and `~/Library/Logs/yactt/`.
- Because the endpoint is fixed, another process occupying port `57812` prevents yactt from starting; this is reported in the service error log.

## Testing

Add a shell integration test that runs against a temporary home/XDG layout and fake external commands rather than modifying the developer's real launchd domain.

The test will verify:

1. A macOS run generates a valid plist with the absolute executable, loopback bind, port `57812`, `RunAtLoad`, and `KeepAlive`.
2. A missing job is bootstrapped.
3. An unchanged plist and current binary do not restart or re-bootstrap a loaded job.
4. A changed plist is booted out and re-bootstrapped.
5. A replaced release binary causes `kickstart -k` when the plist is unchanged.
6. The project-local `bin/yactt` escape hatch becomes the LaunchAgent executable without contacting GitHub.
7. A non-macOS host receives the documented unsupported-platform failure.
8. `plugins/yactt/.mcp.json` points to the expected HTTP URL.

Verification will run the focused shell test, `bash -n` on the installer and test, `plutil -lint` on a generated plist when running on macOS, and the repository's existing test suite appropriate to the changed plugin surface.

## Operations and uninstall

The README will document these operator commands:

```bash
launchctl print "gui/$(id -u)/com.kellenff.yactt.mcp"
curl -fsS http://127.0.0.1:57812/healthz
```

Uninstall will boot out the user job before deleting the plist and binary. Log and TOFU/cache removal remain explicit optional cleanup steps so uninstall does not silently discard diagnostics or trust state.

## Risk assessment

Design-time blast-radius analysis used the heuristic backend because graph services were unavailable. The projected change is four plugin-local files with low change scope, low failure impact, and low action risk. The primary operational risk is making a static HTTP client configuration depend on successful launchd setup; fail-fast installation and focused lifecycle tests mitigate that risk.
