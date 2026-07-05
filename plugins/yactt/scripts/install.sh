#!/usr/bin/env bash
# Bootstraps the yactt binary from GitHub Releases. Runs on every
# Claude Code SessionStart. No-ops when the installed version is
# already current or when the project-local bin/yactt exists
# (developer escape hatch).
#
# Install path: $XDG_HOME/bin/yactt if XDG_HOME is set, otherwise
# $HOME/.local/bin/yactt. Both are XDG-conventional locations that
# most shells have on PATH.
set -euo pipefail

REPO="kellenff/yactt"
API_URL="https://api.github.com/repos/${REPO}/releases/latest"

# --- install path ---
if [[ -n "${XDG_HOME:-}" ]]; then
	INSTALL_DIR="${XDG_HOME}/bin"
else
	INSTALL_DIR="${HOME}/.local/bin"
fi
INSTALL_PATH="${INSTALL_DIR}/yactt"

# --- cache (caps unauthed GitHub API at ~24 calls/day) ---
CACHE_FILE="${XDG_CACHE_HOME:-${HOME}/.cache}/yactt/latest"
CACHE_TTL=3600

# --- developer escape hatch: project-local build wins ---
if [[ -x "${CLAUDE_PROJECT_DIR:-}/bin/yactt" ]]; then
	exit 0
fi

cache_age() {
	local f="${1}"
	local now; now=$(date +%s)
	local mtime
	mtime=$(stat -c %Y "${f}" 2>/dev/null || stat -f %m "${f}" 2>/dev/null || echo 0)
	echo $(( now - mtime ))
}

installed_version() {
	command -v yactt >/dev/null 2>&1 || return 1
	yactt version 2>/dev/null | awk '{print $2}' | sed 's/^v//'
}

# Lightweight semver allowlist. Accepts:
#   - 1.0            (one segment)
#   - 1.0.0          (two or three segments)
#   - 1.0.0-rc.1     (with pre-release)
# Refuses anything else — including a poisoned cache that would
# otherwise turn into a 404 download attempt against the releases API.
is_semver() {
	[[ "$1" =~ ^[0-9]+(\.[0-9]+){1,2}(-[0-9A-Za-z.+-]+)?$ ]]
}

latest_version() {
	local cached=""
	if [[ -f "${CACHE_FILE}" ]] && (( $(cache_age "${CACHE_FILE}") < CACHE_TTL )); then
		cached=$(<"${CACHE_FILE}")
		if [[ -n "${cached}" ]] && is_semver "${cached}"; then
			echo "${cached}"
			return 0
		fi
		# Cache content invalid — fall through to the API.
	fi
	if ! command -v jq >/dev/null 2>&1; then
		echo "yactt: 'jq' required for bootstrap (brew install jq / apt install jq)" >&2
		return 1
	fi
	local v
	v=$(curl -fsSL "${API_URL}" | jq -r '.tag_name') || return 1
	v="${v#v}" # strip leading 'v'
	if ! is_semver "${v}"; then
		echo "yactt: API returned non-semver tag '${v}' — refusing to install" >&2
		return 1
	fi
	mkdir -p "$(dirname "${CACHE_FILE}")"
	echo "${v}" > "${CACHE_FILE}"
	echo "${v}"
}

os_arch() {
	local os arch
	case "$(uname -s)" in
		Darwin) os=darwin ;;
		Linux) os=linux ;;
		*) echo "yactt: unsupported OS $(uname -s) — skipping bootstrap" >&2; return 1 ;;
	esac
	case "$(uname -m)" in
		arm64 | aarch64) arch=arm64 ;;
		x86_64 | amd64) arch=amd64 ;;
		*) echo "yactt: unsupported arch $(uname -m) — skipping bootstrap" >&2; return 1 ;;
	esac
	echo "${os}_${arch}"
}

download_and_install() {
	local version="$1" target="$2"
	local archive="yactt_${target}.tar.gz"
	local base="https://github.com/${REPO}/releases/download/v${version}"
	local tmpdir
	tmpdir=$(mktemp -d)
	# shellcheck disable=SC2064
	trap "rm -rf '${tmpdir}'" EXIT

	echo "yactt: downloading v${version} (${target})..." >&2
	curl -fsSL -o "${tmpdir}/${archive}" "${base}/${archive}"
	curl -fsSL -o "${tmpdir}/SHA256SUMS" "${base}/SHA256SUMS"

	# Verify against SHA256SUMS. Refuse to install on mismatch — the
	# user picked "latest wins" but only when the download is genuine.
	local expected actual
	expected=$(awk -v a="${archive}" '$2 == a {print $1}' "${tmpdir}/SHA256SUMS")
	actual=$(shasum -a 256 "${tmpdir}/${archive}" | awk '{print $1}')
	if [[ -z "${expected}" ]]; then
		echo "yactt: no SHA256 entry for ${archive} — refusing to install" >&2
		return 1
	fi
	if [[ "${expected}" != "${actual}" ]]; then
		echo "yactt: checksum mismatch (expected ${expected}, got ${actual})" >&2
		return 1
	fi

	mkdir -p "${INSTALL_DIR}"
	tar -xzf "${tmpdir}/${archive}" -C "${tmpdir}" "yactt_${target}"
	install -m 0755 "${tmpdir}/yactt_${target}" "${INSTALL_PATH}"
	echo "yactt: installed v${version} → ${INSTALL_PATH}" >&2

	# PATH sanity check — soft warning, not fatal. The MCP server
	# will fail to start if yactt is unreachable, which surfaces the
	# problem loudly enough.
	if ! command -v yactt >/dev/null 2>&1; then
		echo "yactt: WARNING — ${INSTALL_DIR} is not on PATH." >&2
		echo "         Add 'export PATH=\"${INSTALL_DIR}:\$PATH\"' to your shell rc." >&2
	fi
}

main() {
	local target
	target=$(os_arch) || exit 0
	local latest installed
	latest=$(latest_version) || {
		echo "yactt: cannot reach GitHub Releases; plugin will not work until network is back" >&2
		exit 1
	}
	installed=$(installed_version || true)

	if [[ "${installed}" == "${latest}" ]]; then
		exit 0
	fi
	download_and_install "${latest}" "${target}"
}

main "$@"