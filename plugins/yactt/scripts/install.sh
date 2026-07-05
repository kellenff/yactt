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

# TOFU (Trust-On-First-Use) of (version, sha256). After the first
# successful install we record the sha256 we observed in SHA256SUMS.
# On every subsequent install: if the version is the same as the
# last install AND the sha256 from SHA256SUMS differs, refuse — that
# catches "compromised GitHub release at the same version" (a replay
# or silent replace). It does NOT catch a malicious new release (the
# sha256 would differ but we'd treat it as a legitimate upgrade).
# For full supply-chain integrity, sign SHA256SUMS out-of-band
# (cosign/minisign with a pinned key); see plugin README.
KNOWN_GOOD_FILE="${XDG_DATA_HOME:-${HOME}/.local/share}/yactt/known-good"

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

	# Fetch SHA256SUMS FIRST so we can run the TOFU check before we
	# download the binary itself. Saves a 100+ MB download if TOFU
	# refuses (a same-version replay with a swapped hash).
	echo "yactt: fetching SHA256SUMS for v${version}..." >&2
	curl -fsSL -o "${tmpdir}/SHA256SUMS" "${base}/SHA256SUMS"

	local expected
	expected=$(awk -v a="${archive}" '$2 == a {print $1}' "${tmpdir}/SHA256SUMS")
	if [[ -z "${expected}" ]]; then
		echo "yactt: no SHA256 entry for ${archive} — refusing to install" >&2
		return 1
	fi

	# TOFU check: same version as before but a different sha256 in
	# the manifest is a strong signal of replay / compromised release.
	if [[ -f "${KNOWN_GOOD_FILE}" ]]; then
		local known_version known_sha
		known_version=$(awk '{print $1}' "${KNOWN_GOOD_FILE}")
		known_sha=$(awk '{print $2}' "${KNOWN_GOOD_FILE}")
		if [[ "${known_version}" == "${version}" && "${known_sha}" != "${expected}" ]]; then
			echo "yactt: refusing v${version} — known-good hash was ${known_sha}, manifest now says ${expected}" >&2
			echo "         likely a replay or compromised release; refusing to install" >&2
			return 1
		fi
	fi

	echo "yactt: downloading v${version} (${target})..." >&2
	curl -fsSL -o "${tmpdir}/${archive}" "${base}/${archive}"

	# Verify against SHA256SUMS. This is CORRUPTION DETECTION only —
	# the manifest comes from the same origin as the binary, so a
	# compromised release endpoint can ship a matching pair. The
	# TOFU check above is what catches that for same-version replays.
	# For full supply-chain integrity, sign the manifest out-of-band
	# (cosign / minisign with a pinned public key); see README.
	local actual
	actual=$(shasum -a 256 "${tmpdir}/${archive}" | awk '{print $1}')
	if [[ "${expected}" != "${actual}" ]]; then
		echo "yactt: checksum mismatch (expected ${expected}, got ${actual})" >&2
		return 1
	fi

	mkdir -p "${INSTALL_DIR}"
	tar -xzf "${tmpdir}/${archive}" -C "${tmpdir}" "yactt_${target}"
	install -m 0755 "${tmpdir}/yactt_${target}" "${INSTALL_PATH}"
	echo "yactt: installed v${version} → ${INSTALL_PATH}" >&2

	# Record the (version, sha256) pair for the next TOFU check.
	mkdir -p "$(dirname "${KNOWN_GOOD_FILE}")"
	printf '%s %s\n' "${version}" "${actual}" > "${KNOWN_GOOD_FILE}"

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