#!/bin/bash

# Script to check that the committed supergraph is in sync
# Regenerates the supergraph from the per-service subgraph schemas and
# fails when the committed artifacts differ from the generator output.

set -euo pipefail  # Exit on error, undefined vars, pipe failures

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly PROJECT_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || echo "$SCRIPT_DIR/..")"
readonly GATEWAY_DIR="$PROJECT_ROOT/backend/gateway"

# Only these generator outputs are tracked; the per-service *.graphql
# intermediates are gitignored
readonly TRACKED_OUTPUTS=(
  "$GATEWAY_DIR/supergraph.graphql"
  "$GATEWAY_DIR/supergraph.yaml"
)

function error() {
  echo >&2 "❌ $*"
}

# Ensure we're in a git repository
if ! git rev-parse --git-dir >/dev/null 2>&1; then
  error "Not in a git repository"
  exit 2
fi

if ! command -v rover >/dev/null 2>&1; then
  error "rover CLI not found (required to compose the supergraph)"
  exit 2
fi

echo "Regenerating supergraph..."
(cd "$GATEWAY_DIR" && task generate)

if ! git -C "$PROJECT_ROOT" diff --exit-code -- "${TRACKED_OUTPUTS[@]}"; then
  error "Supergraph is out of sync with the subgraph schemas!"
  echo "💡 Run 'task generate' in backend/gateway and commit the result"
  exit 1
fi

echo "✅ Supergraph is in sync with the subgraph schemas!"
