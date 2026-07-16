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

test_replaced_binary_kickstarts_unchanged_agent() {
	setup_case
	unset CLAUDE_PROJECT_DIR
	# shellcheck disable=SC1090
	# The test intentionally sources the repository path.
	source "${INSTALL_SCRIPT}"
	mkdir -p "${INSTALL_DIR}"
	cat > "${INSTALL_PATH}" <<'SCRIPT'
#!/usr/bin/env bash
printf 'yactt v1.2.3\n'
SCRIPT
	chmod +x "${INSTALL_PATH}"
	install_launch_agent "${INSTALL_PATH}" 0
	: > "${YACTT_TEST_LAUNCHCTL_LOG}"

	# These shadows are invoked via main() below, not directly.
	# shellcheck disable=SC2329
	latest_version() { printf '2.0.0\n'; }
	# shellcheck disable=SC2329
	installed_version() { printf '1.2.3\n'; }
	# shellcheck disable=SC2329
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
run_test test_unchanged_agent_is_left_running
run_test test_changed_plist_is_reloaded
run_test test_failed_bootout_preserves_the_previous_plist_for_retry
run_test test_replaced_binary_kickstarts_unchanged_agent
run_test test_current_release_uses_absolute_install_path

if (( failures > 0 )); then
	exit 1
fi
