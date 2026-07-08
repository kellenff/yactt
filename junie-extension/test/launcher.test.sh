#!/usr/bin/env bash
# Smoke test for the project-root walker in yactt-launcher.sh.
# Doesn't exec yactt — uses YACTT_LAUNCHER_DRY_RUN=1.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LAUNCHER="${HERE}/../scripts/yactt-launcher.sh"

fail=0
assert_eq() {
	local got="$1" want="$2" name="$3"
	if [[ "${got}" != "${want}" ]]; then
		echo "FAIL: ${name}: got '${got}', want '${want}'" >&2
		fail=1
	else
		echo "PASS: ${name}"
	fi
}

# Helper: run the launcher in a controlled cwd, dry-run mode.
run_in() {
	(cd "$1" && YACTT_LAUNCHER_DRY_RUN=1 bash "${LAUNCHER}")
}

# Setup: a real git repo (git rev-parse rejects an empty `.git/`
# dir as not-a-repo) + a non-git workspace for the fallback case.
# Canonicalize TMP — on macOS /tmp is a symlink and the launcher
# resolves symlinks before printing.
TMP="$(mktemp -d)"
TMP="$(cd "${TMP}" && pwd -P)"
trap 'rm -rf "${TMP}"' EXIT
mkdir -p "${TMP}/proj/sub/deep"
(cd "${TMP}/proj" && git init -q)

# 1. cwd IS the project root.
assert_eq "$(run_in "${TMP}/proj")" "${TMP}/proj" "root cwd resolves to itself"

# 2. cwd is a deep subdir → walker finds the project root above it.
assert_eq "$(run_in "${TMP}/proj/sub/deep")" "${TMP}/proj" "deep cwd walks up to root"

# 3. Non-git workspace → cwd fallback.
NONGIT="${TMP}/nongit"
mkdir -p "${NONGIT}"
assert_eq "$(run_in "${NONGIT}")" "${NONGIT}" "non-git cwd falls back to itself"

exit "${fail}"
