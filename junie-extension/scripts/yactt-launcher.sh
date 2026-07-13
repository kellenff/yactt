#!/usr/bin/env bash
# MCP launcher for Junie. Execs yactt in registry mode; the agent
# is expected to call `index_repository` with a file:// project
# URI before invoking any code-intel tool. The Junie MCP config
# has no project-dir variable substitution, so the agent picks
# the project explicitly via tool args.
set -euo pipefail

# ponytail: dry-run prints a deprecation notice instead of exec'ing
# yactt. Used by the smoke test in test/launcher.test.sh to assert
# the old `find_project_root` behaviour is gone.
if [[ "${YACTT_LAUNCHER_DRY_RUN:-}" == "1" ]]; then
	echo "deprecated: yactt-launcher no longer resolves a project root; pass a file:// project URI via index_repository"
	exit 0
fi

exec yactt mcp serve
