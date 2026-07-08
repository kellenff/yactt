#!/usr/bin/env bash
# MCP launcher for Junie. Walks up from cwd looking for `.git`,
# falls back to cwd, then `exec`s yactt in single-repo mode. The
# walker is required because Junie's MCP config has no project-dir
# variable substitution — without it the agent only sees the 5-tool
# registry surface.
set -euo pipefail

find_project_root() {
	local dir="${PWD}"
	while [[ "${dir}" != "/" ]]; do
		if [[ -e "${dir}/.git" ]]; then
			echo "${dir}"
			return 0
		fi
		dir="$(dirname "${dir}")"
	done
	# ponytail: cwd fallback — covers exported tarballs and
	# non-git workspaces where `.git` may legitimately be missing.
	echo "${PWD}"
}

# ponytail: dry-run prints the resolved path instead of exec'ing
# yactt. Used by the smoke test in test/launcher.test.sh.
if [[ "${YACTT_LAUNCHER_DRY_RUN:-}" == "1" ]]; then
	find_project_root
	exit 0
fi

exec yactt mcp serve "$(find_project_root)"
