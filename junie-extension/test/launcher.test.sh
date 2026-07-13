#!/usr/bin/env bash
# Smoke test for the deprecation notice in yactt-launcher.sh.
# Doesn't exec yactt — uses YACTT_LAUNCHER_DRY_RUN=1.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LAUNCHER="${HERE}/../scripts/yactt-launcher.sh"

fail=0
assert_contains() {
	local got="$1" want="$2" name="$3"
	if [[ "${got}" != *"${want}"* ]]; then
		echo "FAIL: ${name}: output missing '${want}'; got '${got}'" >&2
		fail=1
	else
		echo "PASS: ${name}"
	fi
}

# The dry-run output is a single-line deprecation notice; the
# launcher no longer resolves a project root. This is the post-
# migration contract — agents call index_repository with a
# file:// project URI themselves.
output="$(YACTT_LAUNCHER_DRY_RUN=1 bash "${LAUNCHER}")"
assert_contains "${output}" "deprecated" "dry-run prints deprecation notice"
assert_contains "${output}" "file://" "dry-run points agents at file:// project URI"

exit "${fail}"
