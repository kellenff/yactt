# Persistent macOS HTTP MCP LaunchAgent Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use snowball:subagent-driven-development (recommended) or snowball:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the macOS-only Claude Code plugin install and maintain a persistent loopback HTTP MCP daemon on port `57812`, and connect the plugin to that daemon instead of spawning stdio servers.

**Architecture:** Extend the existing verified release bootstrap with a small launchd reconciliation layer. The installer renders an absolute-path user LaunchAgent, validates and atomically installs it, then performs only the lifecycle action needed for a missing job, changed plist, or replaced binary; unchanged SessionStart runs leave the warm daemon untouched.

**Tech Stack:** Bash 3.2-compatible shell, macOS launchd/`launchctl`, XML property lists/`plutil`, Claude Code `.mcp.json`, existing SHA-256 and TOFU release bootstrap.

---

## Source design

Implement against `docs/snowball/specs/2026-07-16-launchd-http-mcp-design.md`.

## File structure

- Create `plugins/yactt/test/install.test.sh` — dependency-free shell integration harness with fake `launchctl`, `plutil`, `uname`, and `curl`; exercises the real installer without touching the operator's launchd domain or network.
- Modify `plugins/yactt/scripts/install.sh` — macOS platform gate, plist renderer, launchd reconciliation, absolute-path version detection, local-development binary selection, and upgrade restart behavior.
- Modify `plugins/yactt/.mcp.json` — fixed Streamable HTTP client endpoint at `http://127.0.0.1:57812/mcp`.
- Modify `plugins/yactt/README.md` — macOS-only requirements, LaunchAgent operations, developer behavior, and uninstall instructions.

### Task 1: Generate and bootstrap the macOS user LaunchAgent

**Files:**
- Create: `plugins/yactt/test/install.test.sh`
- Modify: `plugins/yactt/scripts/install.sh:1-42,92-105,176-192`

- [ ] **Step 1: Write the failing bootstrap and platform tests**

Create `plugins/yactt/test/install.test.sh` with this initial harness. The project directory deliberately contains `&` so the test also pins XML escaping.

```bash
#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
INSTALL_SCRIPT="${ROOT}/plugins/yactt/scripts/install.sh"
MCP_CONFIG="${ROOT}/plugins/yactt/.mcp.json"
TEST_TMP=$(mktemp -d)
trap 'rm -rf "${TEST_TMP}"' EXIT

fail() {
	printf 'FAIL: %s\n' "$*" >&2
	return 1
}

assert_contains() {
	local file="$1" expected="$2"
	grep -Fq -- "${expected}" "${file}" || fail "${file} does not contain: ${expected}"
}

assert_not_contains() {
	local file="$1" unexpected="$2"
	if grep -Fq -- "${unexpected}" "${file}"; then
		fail "${file} unexpectedly contains: ${unexpected}"
	fi
}

setup_case() {
	CASE_ROOT=$(mktemp -d "${TEST_TMP}/case.XXXXXX")
	export CASE_ROOT
	export HOME="${CASE_ROOT}/home"
	export XDG_HOME="${CASE_ROOT}/xdg"
	export XDG_CACHE_HOME="${CASE_ROOT}/cache"
	export XDG_DATA_HOME="${CASE_ROOT}/data"
	export CLAUDE_PROJECT_DIR="${CASE_ROOT}/project & dev"
	export YACTT_TEST_LAUNCHCTL_LOG="${CASE_ROOT}/launchctl.log"
	export YACTT_TEST_LAUNCHCTL_STATE="${CASE_ROOT}/launchctl.loaded"
	export YACTT_TEST_PLUTIL_LOG="${CASE_ROOT}/plutil.log"
	export YACTT_TEST_CURL_LOG="${CASE_ROOT}/curl.log"
	export YACTT_TEST_BOOTOUT_FAIL=0
	export YACTT_TEST_OS=Darwin
	export YACTT_TEST_ARCH=arm64
	export PATH="${CASE_ROOT}/fake-bin:/usr/bin:/bin"

	mkdir -p "${HOME}" "${CLAUDE_PROJECT_DIR}/bin" "${CASE_ROOT}/fake-bin"
	: > "${YACTT_TEST_LAUNCHCTL_LOG}"
	: > "${YACTT_TEST_PLUTIL_LOG}"
	: > "${YACTT_TEST_CURL_LOG}"

	cat > "${CLAUDE_PROJECT_DIR}/bin/yactt" <<'SCRIPT'
#!/usr/bin/env bash
printf 'yactt v1.2.3\n'
SCRIPT
	chmod +x "${CLAUDE_PROJECT_DIR}/bin/yactt"

	cat > "${CASE_ROOT}/fake-bin/uname" <<'SCRIPT'
#!/usr/bin/env bash
case "${1:-}" in
	-s) printf '%s\n' "${YACTT_TEST_OS}" ;;
	-m) printf '%s\n' "${YACTT_TEST_ARCH}" ;;
	*) /usr/bin/uname "$@" ;;
esac
SCRIPT

	cat > "${CASE_ROOT}/fake-bin/launchctl" <<'SCRIPT'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "${YACTT_TEST_LAUNCHCTL_LOG}"
case "${1:-}" in
	print) [[ -f "${YACTT_TEST_LAUNCHCTL_STATE}" ]] ;;
	bootstrap) : > "${YACTT_TEST_LAUNCHCTL_STATE}" ;;
	bootout)
		if [[ "${YACTT_TEST_BOOTOUT_FAIL:-0}" == "1" ]]; then
			exit 75
		fi
		rm -f "${YACTT_TEST_LAUNCHCTL_STATE}"
		;;
	kickstart) : ;;
	*) exit 64 ;;
esac
SCRIPT

	cat > "${CASE_ROOT}/fake-bin/plutil" <<'SCRIPT'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "${YACTT_TEST_PLUTIL_LOG}"
[[ "${1:-}" == "-lint" && -s "${2:-}" ]]
SCRIPT

	cat > "${CASE_ROOT}/fake-bin/curl" <<'SCRIPT'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "${YACTT_TEST_CURL_LOG}"
exit 97
SCRIPT

	chmod +x "${CASE_ROOT}/fake-bin/"*
}

run_installer() {
	bash "${INSTALL_SCRIPT}" > "${CASE_ROOT}/stdout" 2> "${CASE_ROOT}/stderr"
}

test_initial_bootstrap_uses_local_binary() {
	setup_case
	run_installer

	local plist="${HOME}/Library/LaunchAgents/com.kellenff.yactt.mcp.plist"
	[[ -f "${plist}" ]] || fail "LaunchAgent plist was not created"
	assert_contains "${plist}" '<string>com.kellenff.yactt.mcp</string>'
	assert_contains "${plist}" 'project &amp; dev/bin/yactt</string>'
	assert_contains "${plist}" '<string>serve-http</string>'
	assert_contains "${plist}" '<string>--bind=127.0.0.1</string>'
	assert_contains "${plist}" '<string>--port=57812</string>'
	assert_contains "${plist}" '<key>RunAtLoad</key>'
	assert_contains "${plist}" '<key>KeepAlive</key>'
	assert_contains "${plist}" '<key>ProcessType</key>'
	assert_contains "${plist}" '<string>Background</string>'
	assert_contains "${plist}" '<key>ThrottleInterval</key>'
	assert_contains "${plist}" '<integer>10</integer>'
	assert_contains "${plist}" "<string>${HOME}/Library/Logs/yactt/mcp.stdout.log</string>"
	assert_contains "${plist}" "<string>${HOME}/Library/Logs/yactt/mcp.stderr.log</string>"
	assert_contains "${YACTT_TEST_LAUNCHCTL_LOG}" "bootstrap gui/$(id -u) ${plist}"
	assert_contains "${YACTT_TEST_PLUTIL_LOG}" "-lint"
	[[ ! -s "${YACTT_TEST_CURL_LOG}" ]] || fail "developer escape hatch contacted GitHub"
}

test_non_macos_fails_clearly() {
	setup_case
	export YACTT_TEST_OS=Linux
	if run_installer; then
		fail "Linux installer run unexpectedly succeeded"
	fi
	assert_contains "${CASE_ROOT}/stderr" 'the yactt Claude Code plugin only supports macOS'
}

failures=0
run_test() {
	local name="$1" status
	set +e
	(set -e; "${name}")
	status=$?
	set -e
	if (( status == 0 )); then
		printf 'ok - %s\n' "${name}"
	else
		printf 'not ok - %s\n' "${name}" >&2
		failures=$((failures + 1))
	fi
}

run_test test_initial_bootstrap_uses_local_binary
run_test test_non_macos_fails_clearly

if (( failures > 0 )); then
	exit 1
fi
```

Make the test executable:

```bash
chmod +x plugins/yactt/test/install.test.sh
```

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```bash
bash plugins/yactt/test/install.test.sh
```

Expected: both tests report `not ok`. The existing developer escape hatch exits before creating a plist, and Linux exits successfully before the new platform gate.

- [ ] **Step 3: Update the installer contract, add LaunchAgent constants, and remove the early developer exit**

Replace the opening comment in `plugins/yactt/scripts/install.sh` with:

```bash
#!/usr/bin/env bash
# Installs the verified yactt release and reconciles its persistent
# macOS user LaunchAgent. Runs on every Claude Code SessionStart;
# unchanged binaries and service configuration leave the warm HTTP
# MCP daemon untouched. A project-local bin/yactt remains the
# developer escape hatch and becomes the LaunchAgent executable.
#
# Install path: $XDG_HOME/bin/yactt if XDG_HOME is set, otherwise
# $HOME/.local/bin/yactt. The LaunchAgent always uses an absolute path.
```

Delete the current top-level developer escape-hatch block at lines 38-41. After `CACHE_TTL=3600`, add:

```bash
# --- persistent HTTP MCP LaunchAgent (macOS user domain) ---
LAUNCH_LABEL="com.kellenff.yactt.mcp"
HTTP_PORT=57812
LAUNCH_AGENT_DIR="${HOME}/Library/LaunchAgents"
LAUNCH_AGENT_PATH="${LAUNCH_AGENT_DIR}/${LAUNCH_LABEL}.plist"
LOG_DIR="${HOME}/Library/Logs/yactt"
STDOUT_LOG="${LOG_DIR}/mcp.stdout.log"
STDERR_LOG="${LOG_DIR}/mcp.stderr.log"
LAUNCH_DOMAIN="gui/$(id -u)"
```

- [ ] **Step 4: Make the architecture resolver macOS-only**

Replace `os_arch` with:

```bash
os_arch() {
	local os arch
	os=$(uname -s)
	if [[ "${os}" != "Darwin" ]]; then
		echo "yactt: the yactt Claude Code plugin only supports macOS (found ${os})" >&2
		return 1
	fi

	case "$(uname -m)" in
		arm64 | aarch64) arch=arm64 ;;
		x86_64 | amd64) arch=amd64 ;;
		*) echo "yactt: unsupported macOS architecture $(uname -m)" >&2; return 1 ;;
	esac
	echo "darwin_${arch}"
}
```

In `latest_version`, replace the cross-platform dependency hint with the macOS-only message:

```bash
	echo "yactt: 'jq' required for bootstrap (brew install jq)" >&2
```

- [ ] **Step 5: Add the XML renderer and minimal initial bootstrap**

Add these functions immediately after `os_arch`. This is intentionally the minimum behavior for the first green test; Task 2 adds reconciliation.

```bash
xml_escape() {
	printf '%s' "$1" | sed \
		-e 's/&/\&amp;/g' \
		-e 's/</\&lt;/g' \
		-e 's/>/\&gt;/g'
}

render_launch_agent() {
	local binary="$1"
	local binary_xml stdout_xml stderr_xml
	binary_xml=$(xml_escape "${binary}")
	stdout_xml=$(xml_escape "${STDOUT_LOG}")
	stderr_xml=$(xml_escape "${STDERR_LOG}")

	cat <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>${LAUNCH_LABEL}</string>
	<key>ProgramArguments</key>
	<array>
		<string>${binary_xml}</string>
		<string>mcp</string>
		<string>serve-http</string>
		<string>--bind=127.0.0.1</string>
		<string>--port=${HTTP_PORT}</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>ProcessType</key>
	<string>Background</string>
	<key>ThrottleInterval</key>
	<integer>10</integer>
	<key>StandardOutPath</key>
	<string>${stdout_xml}</string>
	<key>StandardErrorPath</key>
	<string>${stderr_xml}</string>
</dict>
</plist>
PLIST
}

install_launch_agent() {
	local binary="$1"
	local binary_changed="${2:-0}"
	local tmp
	: "${binary_changed}"

	mkdir -p "${LAUNCH_AGENT_DIR}" "${LOG_DIR}"
	tmp=$(mktemp "${LAUNCH_AGENT_DIR}/.${LAUNCH_LABEL}.XXXXXX")
	render_launch_agent "${binary}" > "${tmp}"
	if ! plutil -lint "${tmp}" >/dev/null; then
		rm -f "${tmp}"
		echo "yactt: generated LaunchAgent plist failed validation" >&2
		return 1
	fi
	chmod 0644 "${tmp}"
	mv "${tmp}" "${LAUNCH_AGENT_PATH}"
	launchctl bootstrap "${LAUNCH_DOMAIN}" "${LAUNCH_AGENT_PATH}"
}
```

- [ ] **Step 6: Route local and release binaries through LaunchAgent installation**

Replace `main` and the unconditional final call with:

```bash
main() {
	local target
	target=$(os_arch) || return 1

	local local_binary=""
	if [[ -n "${CLAUDE_PROJECT_DIR:-}" ]]; then
		local_binary="${CLAUDE_PROJECT_DIR}/bin/yactt"
	fi
	if [[ -n "${local_binary}" && -x "${local_binary}" ]]; then
		install_launch_agent "${local_binary}" 0
		return
	fi

	local latest installed binary_changed=0
	latest=$(latest_version) || {
		echo "yactt: cannot reach GitHub Releases; plugin will not work until network is back" >&2
		return 1
	}
	installed=$(installed_version || true)

	if [[ "${installed}" != "${latest}" ]]; then
		download_and_install "${latest}" "${target}"
		binary_changed=1
	fi
	install_launch_agent "${INSTALL_PATH}" "${binary_changed}"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
	main "$@"
fi
```

The `BASH_SOURCE` guard keeps command execution unchanged while allowing later tests to source the renderer and lifecycle functions without running the network bootstrap.

- [ ] **Step 7: Run the focused test and verify GREEN**

Run:

```bash
bash plugins/yactt/test/install.test.sh
```

Expected: two `ok` lines and exit status 0.

- [ ] **Step 8: Commit the initial LaunchAgent behavior**

```bash
git add plugins/yactt/scripts/install.sh plugins/yactt/test/install.test.sh
git commit -m "feat(plugin): bootstrap HTTP MCP LaunchAgent"
```

### Task 2: Reconcile launchd state without restarting healthy sessions

**Files:**
- Modify: `plugins/yactt/test/install.test.sh`
- Modify: `plugins/yactt/scripts/install.sh` (`install_launch_agent`)

- [ ] **Step 1: Add failing lifecycle tests**

Insert these functions before `failures=0` in `plugins/yactt/test/install.test.sh`:

```bash
test_unchanged_agent_is_left_running() {
	setup_case
	run_installer
	: > "${YACTT_TEST_LAUNCHCTL_LOG}"

	run_installer

	assert_contains "${YACTT_TEST_LAUNCHCTL_LOG}" "print gui/$(id -u)/com.kellenff.yactt.mcp"
	assert_not_contains "${YACTT_TEST_LAUNCHCTL_LOG}" "bootstrap "
	assert_not_contains "${YACTT_TEST_LAUNCHCTL_LOG}" "bootout "
	assert_not_contains "${YACTT_TEST_LAUNCHCTL_LOG}" "kickstart "
}

test_changed_plist_is_reloaded() {
	setup_case
	run_installer
	local plist="${HOME}/Library/LaunchAgents/com.kellenff.yactt.mcp.plist"
	printf 'stale configuration\n' > "${plist}"
	: > "${YACTT_TEST_LAUNCHCTL_LOG}"

	run_installer

	assert_contains "${YACTT_TEST_LAUNCHCTL_LOG}" "bootout gui/$(id -u)/com.kellenff.yactt.mcp"
	assert_contains "${YACTT_TEST_LAUNCHCTL_LOG}" "bootstrap gui/$(id -u) ${plist}"
	assert_contains "${plist}" '<string>--port=57812</string>'
}

test_failed_bootout_preserves_the_previous_plist_for_retry() {
	setup_case
	run_installer
	local plist="${HOME}/Library/LaunchAgents/com.kellenff.yactt.mcp.plist"
	cp "${plist}" "${CASE_ROOT}/original.plist"

	export CLAUDE_PROJECT_DIR="${CASE_ROOT}/replacement & dev"
	mkdir -p "${CLAUDE_PROJECT_DIR}/bin"
	cp "${CASE_ROOT}/project & dev/bin/yactt" "${CLAUDE_PROJECT_DIR}/bin/yactt"
	export YACTT_TEST_BOOTOUT_FAIL=1
	: > "${YACTT_TEST_LAUNCHCTL_LOG}"

	if run_installer; then
		fail "installer succeeded despite a failed launchctl bootout"
	fi
	cmp -s "${plist}" "${CASE_ROOT}/original.plist" || fail "failed bootout replaced the working plist"
	assert_contains "${YACTT_TEST_LAUNCHCTL_LOG}" "bootout gui/$(id -u)/com.kellenff.yactt.mcp"

	export YACTT_TEST_BOOTOUT_FAIL=0
	: > "${YACTT_TEST_LAUNCHCTL_LOG}"
	run_installer
	assert_contains "${YACTT_TEST_LAUNCHCTL_LOG}" "bootout gui/$(id -u)/com.kellenff.yactt.mcp"
	assert_contains "${YACTT_TEST_LAUNCHCTL_LOG}" "bootstrap gui/$(id -u) ${plist}"
	assert_contains "${plist}" 'replacement &amp; dev/bin/yactt</string>'
}

test_replaced_binary_kickstarts_unchanged_agent() {
	setup_case
	unset CLAUDE_PROJECT_DIR
	# shellcheck disable=SC1090 -- the test intentionally sources the repository path.
	source "${INSTALL_SCRIPT}"
	mkdir -p "${INSTALL_DIR}"
	cat > "${INSTALL_PATH}" <<'SCRIPT'
#!/usr/bin/env bash
printf 'yactt v1.2.3\n'
SCRIPT
	chmod +x "${INSTALL_PATH}"
	install_launch_agent "${INSTALL_PATH}" 0
	: > "${YACTT_TEST_LAUNCHCTL_LOG}"

	latest_version() { printf '2.0.0\n'; }
	installed_version() { printf '1.2.3\n'; }
	download_and_install() {
		cat > "${INSTALL_PATH}" <<'SCRIPT'
#!/usr/bin/env bash
printf 'yactt v2.0.0\n'
SCRIPT
		chmod +x "${INSTALL_PATH}"
	}

	main

	assert_contains "${YACTT_TEST_LAUNCHCTL_LOG}" "kickstart -k gui/$(id -u)/com.kellenff.yactt.mcp"
	assert_not_contains "${YACTT_TEST_LAUNCHCTL_LOG}" "bootout "
	assert_not_contains "${YACTT_TEST_LAUNCHCTL_LOG}" "bootstrap "
}
```

Add these invocations after the two existing `run_test` lines:

```bash
run_test test_unchanged_agent_is_left_running
run_test test_changed_plist_is_reloaded
run_test test_failed_bootout_preserves_the_previous_plist_for_retry
run_test test_replaced_binary_kickstarts_unchanged_agent
```

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```bash
bash plugins/yactt/test/install.test.sh
```

Expected: the first two tests remain green; the four new tests fail because the minimal implementation always overwrites and bootstraps the job.

- [ ] **Step 3: Replace the minimal installer with idempotent reconciliation**

Replace `install_launch_agent` in `plugins/yactt/scripts/install.sh` with:

```bash
install_launch_agent() {
	local binary="$1"
	local binary_changed="${2:-0}"
	local tmp plist_changed=0 loaded=0
	local service="${LAUNCH_DOMAIN}/${LAUNCH_LABEL}"

	mkdir -p "${LAUNCH_AGENT_DIR}" "${LOG_DIR}"
	tmp=$(mktemp "${LAUNCH_AGENT_DIR}/.${LAUNCH_LABEL}.XXXXXX")
	render_launch_agent "${binary}" > "${tmp}"
	if ! plutil -lint "${tmp}" >/dev/null; then
		rm -f "${tmp}"
		echo "yactt: generated LaunchAgent plist failed validation" >&2
		return 1
	fi
	chmod 0644 "${tmp}"

	if [[ -f "${LAUNCH_AGENT_PATH}" ]] && cmp -s "${tmp}" "${LAUNCH_AGENT_PATH}"; then
		rm -f "${tmp}"
	else
		plist_changed=1
	fi

	if launchctl print "${service}" >/dev/null 2>&1; then
		loaded=1
	fi

	if (( plist_changed )); then
		if (( loaded )); then
			if ! launchctl bootout "${service}"; then
				rm -f "${tmp}"
				return 1
			fi
		fi
		mv "${tmp}" "${LAUNCH_AGENT_PATH}"
		launchctl bootstrap "${LAUNCH_DOMAIN}" "${LAUNCH_AGENT_PATH}"
	elif (( ! loaded )); then
		launchctl bootstrap "${LAUNCH_DOMAIN}" "${LAUNCH_AGENT_PATH}"
	elif (( binary_changed )); then
		launchctl kickstart -k "${service}"
	fi
}
```

This ordering validates before replacement, keeps the old plist on disk when `bootout` fails so a later SessionStart retries the reconciliation, atomically moves only changed content, and avoids a process restart on unchanged SessionStart runs.

- [ ] **Step 4: Run the focused test and verify GREEN**

Run:

```bash
bash plugins/yactt/test/install.test.sh
```

Expected: six `ok` lines and exit status 0.

- [ ] **Step 5: Commit lifecycle reconciliation**

```bash
git add plugins/yactt/scripts/install.sh plugins/yactt/test/install.test.sh
git commit -m "feat(plugin): reconcile persistent MCP LaunchAgent"
```

### Task 3: Detect the installed release by absolute path

**Files:**
- Modify: `plugins/yactt/test/install.test.sh`
- Modify: `plugins/yactt/scripts/install.sh:51-54, main`

- [ ] **Step 1: Add a failing current-release test**

Insert this function before `failures=0` in `plugins/yactt/test/install.test.sh`:

```bash
test_current_release_uses_absolute_install_path() {
	setup_case
	unset CLAUDE_PROJECT_DIR
	mkdir -p "${XDG_HOME}/bin" "$(dirname "${XDG_CACHE_HOME}/yactt/latest")"
	cat > "${XDG_HOME}/bin/yactt" <<'SCRIPT'
#!/usr/bin/env bash
printf 'yactt v1.2.3\n'
SCRIPT
	chmod +x "${XDG_HOME}/bin/yactt"
	printf '1.2.3\n' > "${XDG_CACHE_HOME}/yactt/latest"

	run_installer

	local plist="${HOME}/Library/LaunchAgents/com.kellenff.yactt.mcp.plist"
	assert_contains "${plist}" "<string>${XDG_HOME}/bin/yactt</string>"
	[[ ! -s "${YACTT_TEST_CURL_LOG}" ]] || fail "current absolute-path install contacted GitHub"
	assert_not_contains "${YACTT_TEST_LAUNCHCTL_LOG}" "kickstart "
}
```

Add its invocation with the other runner lines:

```bash
run_test test_current_release_uses_absolute_install_path
```

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```bash
bash plugins/yactt/test/install.test.sh
```

Expected: the new test reports `not ok`. The installed binary is intentionally absent from `PATH`, so the existing `command -v yactt` check misses it and reaches the fake failing `curl`.

- [ ] **Step 3: Make version detection use the selected executable**

Replace `installed_version` with:

```bash
installed_version() {
	local binary="${1:-${INSTALL_PATH}}"
	[[ -x "${binary}" ]] || return 1
	"${binary}" version 2>/dev/null | awk '{print $2}' | sed 's/^v//'
}
```

In `main`, change:

```bash
installed=$(installed_version || true)
```

to:

```bash
installed=$(installed_version "${INSTALL_PATH}" || true)
```

Replace the PATH-sanity comment in `download_and_install` with this accurate description; keep the existing warning commands beneath it:

```bash
	# PATH sanity check for direct CLI use. The LaunchAgent executes
	# INSTALL_PATH directly and does not depend on the shell's PATH.
```

Keep the warning because users may still invoke the `yactt` CLI directly, even though launchd uses the absolute path.

- [ ] **Step 4: Run the focused test and verify GREEN**

Run:

```bash
bash plugins/yactt/test/install.test.sh
```

Expected: seven `ok` lines, no network calls in the current-release case, and exit status 0.

- [ ] **Step 5: Commit absolute-path release detection**

```bash
git add plugins/yactt/scripts/install.sh plugins/yactt/test/install.test.sh
git commit -m "fix(plugin): detect installed yactt by absolute path"
```

### Task 4: Point Claude Code at the persistent HTTP endpoint

**Files:**
- Modify: `plugins/yactt/test/install.test.sh`
- Modify: `plugins/yactt/.mcp.json`

- [ ] **Step 1: Add the failing MCP configuration contract test**

Insert this function before `failures=0` in `plugins/yactt/test/install.test.sh`:

```bash
test_mcp_config_uses_persistent_http_endpoint() {
	assert_contains "${MCP_CONFIG}" '"type": "http"'
	assert_contains "${MCP_CONFIG}" '"url": "http://127.0.0.1:57812/mcp"'
	assert_not_contains "${MCP_CONFIG}" '"command"'
	assert_not_contains "${MCP_CONFIG}" '"args"'
}
```

Add its runner invocation:

```bash
run_test test_mcp_config_uses_persistent_http_endpoint
```

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```bash
bash plugins/yactt/test/install.test.sh
```

Expected: the HTTP configuration test reports `not ok` because `.mcp.json` still declares the stdio command.

- [ ] **Step 3: Replace the MCP server configuration**

Replace all of `plugins/yactt/.mcp.json` with:

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

- [ ] **Step 4: Run the focused test and validate the JSON**

Run:

```bash
bash plugins/yactt/test/install.test.sh
jq empty plugins/yactt/.mcp.json
```

Expected: eight `ok` lines; `jq empty` exits 0 without output.

- [ ] **Step 5: Commit the HTTP client switch**

```bash
git add plugins/yactt/.mcp.json plugins/yactt/test/install.test.sh
git commit -m "feat(plugin): connect Claude to persistent HTTP MCP"
```

### Task 5: Document the macOS-only persistent service

**Files:**
- Modify: `plugins/yactt/README.md:9-50,74-95,117-143,155-172`

- [ ] **Step 1: Update Quickstart and plugin behavior**

In the Claude Code Quickstart, replace the paragraph after the install commands with:

```markdown
That's it. The macOS `SessionStart` hook downloads the matched binary from the latest GitHub release on first use, verifies its SHA256 against the published `SHA256SUMS`, and installs a persistent user LaunchAgent. Later sessions reconcile the service without restarting a healthy daemon. Requires macOS and `jq` (`brew install jq`).
```

In the Pi section, make the shared platform constraint explicit by replacing its final paragraph with:

```markdown
The Pi extension reuses the same macOS-only installer and the same TOFU + SHA256 chain. It therefore requires a macOS user launchd domain; Linux service management is not provided.
```

Replace the numbered list under `## What this plugin does` with:

```markdown
1. **Installs `yactt`** to `$XDG_HOME/bin/yactt` (or `$HOME/.local/bin/yactt` when `XDG_HOME` is unset).
2. **Maintains a persistent LaunchAgent** at `~/Library/LaunchAgents/com.kellenff.yactt.mcp.plist`, running `yactt mcp serve-http` on loopback port `57812` with `RunAtLoad` and `KeepAlive`.
3. **Connects Claude Code over Streamable HTTP** at `http://127.0.0.1:57812/mcp` instead of spawning a cold stdio server per session.
4. **Activates the `code-explore` skill**, which teaches the agent the canonical exploration flow.
```

- [ ] **Step 2: Add service operation and diagnostics documentation**

Immediately after the numbered behavior list, add:

````markdown
### Persistent service operations

The daemon is user-scoped, binds only to `127.0.0.1`, and needs no administrator privileges. Inspect the launchd job and health endpoint with:

```bash
launchctl print "gui/$(id -u)/com.kellenff.yactt.mcp"
curl -fsS http://127.0.0.1:57812/healthz
```

Service output is written to:

- `~/Library/Logs/yactt/mcp.stdout.log`
- `~/Library/Logs/yactt/mcp.stderr.log`

A SessionStart only reloads the job when its plist changes, and only kickstarts it when a release replaces the binary. An unchanged service stays warm.
````

- [ ] **Step 3: Update runtime and developer descriptions**

Change the runtime TOFU heading sentence to refer to `mcp serve-http` startup rather than `mcp serve` startup.

Replace the final developer-escape paragraph with:

```markdown
The SessionStart hook selects `${CLAUDE_PROJECT_DIR}/bin/yactt` without contacting GitHub and updates the LaunchAgent to execute that absolute path. Switching back to a project without a local binary restores the verified release path on the next SessionStart.
```

- [ ] **Step 4: Replace update and uninstall instructions**

Replace the `## Update & uninstall` body through the cache-cleanup sentence with:

````markdown
The bootstrap upgrades `yactt` automatically when a newer release ships. The next SessionStart verifies and replaces the binary, then kickstarts the loaded LaunchAgent so the new version is active. Sessions with no binary or plist change leave the daemon untouched.

To uninstall:

```bash
launchctl bootout "gui/$(id -u)/com.kellenff.yactt.mcp" 2>/dev/null || true
rm -f "$HOME/Library/LaunchAgents/com.kellenff.yactt.mcp.plist"
rm -f "${XDG_HOME:-$HOME/.local}/bin/yactt"
rm -f "${XDG_DATA_HOME:-$HOME/.local/share}/yactt/known-good"
```

Optional diagnostics and release-cache cleanup:

```bash
rm -rf "$HOME/Library/Logs/yactt"
rm -f "${XDG_CACHE_HOME:-$HOME/.cache}/yactt/latest"
```
````

- [ ] **Step 5: Make Trust and Future sections consistent**

Change the Trust bullet to say `yactt mcp serve-http` makes no outbound network calls except for optional language-server subprocesses. Replace the platform-related Future bullet with:

```markdown
- Linux user-service support (the Claude plugin bootstrap is macOS-only today).
```

- [ ] **Step 6: Review the rendered prose and commit**

Run:

```bash
rg -n 'macOS|57812|LaunchAgent|launchctl|healthz|mcp.stdout.log' plugins/yactt/README.md
git diff --check -- plugins/yactt/README.md
```

Expected: each service term appears in the relevant new sections; `git diff --check` exits 0 without output.

Then commit:

```bash
git add plugins/yactt/README.md
git commit -m "docs(plugin): document persistent macOS MCP service"
```

### Task 6: Run final verification without touching the real launchd domain

**Files:**
- Verify: `plugins/yactt/scripts/install.sh`
- Verify: `plugins/yactt/test/install.test.sh`
- Verify: `plugins/yactt/.mcp.json`
- Verify: `plugins/yactt/README.md`

- [ ] **Step 1: Check shell and JSON syntax**

Run:

```bash
bash -n plugins/yactt/scripts/install.sh
bash -n plugins/yactt/test/install.test.sh
jq empty plugins/yactt/.mcp.json
```

Expected: all three commands exit 0 without output.

- [ ] **Step 2: Validate a real rendered plist with macOS `plutil`**

Run:

```bash
tmpdir=$(mktemp -d)
HOME="${tmpdir}/home" bash -c 'source plugins/yactt/scripts/install.sh; render_launch_agent "/tmp/yactt & dev"' > "${tmpdir}/yactt.plist"
plutil -lint "${tmpdir}/yactt.plist"
rm -rf "${tmpdir}"
```

Expected: `plutil` reports `OK`. This calls only the pure renderer; it does not call the real `launchctl`.

- [ ] **Step 3: Run all plugin integration cases**

Run:

```bash
bash plugins/yactt/test/install.test.sh
```

Expected: eight `ok` lines and exit status 0. The test's fake launchctl domain ensures no real service is loaded, unloaded, or restarted.

- [ ] **Step 4: Run static shell analysis when available**

Run:

```bash
if command -v shellcheck >/dev/null 2>&1; then
	shellcheck plugins/yactt/scripts/install.sh plugins/yactt/test/install.test.sh
else
	echo "shellcheck not installed; syntax and integration checks completed"
fi
```

Expected: shellcheck exits 0, or the explicit skip message is printed.

- [ ] **Step 5: Run the repository regression suite**

Run:

```bash
go test ./...
```

Expected: all Go packages pass.

- [ ] **Step 6: Inspect the final change set**

Run:

```bash
git diff --check
git status --short
git diff --stat HEAD~5..HEAD
```

Expected: no whitespace errors; only the four planned plugin files differ across the implementation commits, apart from pre-existing unrelated worktree files. Do not stage or alter `.brainstorm/chorus-20260706T132250.json`, `docs/snowball/decisions/`, `junie-extension/junie-extension`, `plans/`, or `.snowball/blast-radius/last.json` as part of this feature.
