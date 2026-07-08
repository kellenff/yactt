#!/usr/bin/env bash
# MCP launcher for Junie. Resolves the project root via
# `git rev-parse --show-toplevel` (cwd fallback for non-git
# workspaces), then `exec`s yactt in single-repo mode. Required
# because Junie's MCP config has no project-dir variable
# substitution — without it the agent only sees the 5-tool
# registry surface.
set -euo pipefail

find_project_root() {
	# git rev-parse --show-toplevel returns the root of the
	# containing repo, or fails (non-zero, empty stdout) outside
	# one — also fails when `.git` exists but isn't a valid repo.
	# pwd -P canonicalizes the cwd fallback so macOS /tmp
	# symlinks don't make the two paths disagree.
	local root
	root=$(git rev-parse --show-toplevel 2>/dev/null) || root=$(pwd -P)
	echo "${root}"
}

# ponytail: dry-run prints the resolved path instead of exec'ing
# yactt. Used by the smoke test in test/launcher.test.sh.
if [[ "${YACTT_LAUNCHER_DRY_RUN:-}" == "1" ]]; then
	find_project_root
	exit 0
fi

exec yactt mcp serve "$(find_project_root)"
