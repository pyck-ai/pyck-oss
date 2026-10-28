#!/bin/bash

# Tidy every go.work module, then sync the workspace. Backs `task tidy` and
# .github/renovate-post-upgrade.sh.
#
# GOWORK=off so each module tidies in isolation, exactly as backend/Dockerfile
# builds it (no workspace file in the build context).

set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly PROJECT_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || echo "$SCRIPT_DIR/..")"

cd "$PROJECT_ROOT"

workspace_modules() {
  go work edit -json | grep -oE '"DiskPath": "[^"]+"' | sed -E 's/.*"DiskPath": "([^"]+)".*/\1/'
}

if [[ -z "$(workspace_modules)" ]]; then
  echo "tidy: no modules found in go.work" >&2
  exit 1
fi

go work sync
while IFS= read -r module; do
  [[ -z "$module" ]] && continue
  echo "tidy: tidying ${module}"
  (cd "$PROJECT_ROOT/$module" && GOWORK=off go mod tidy)
done < <(workspace_modules)
go work sync

echo "tidy: done"
