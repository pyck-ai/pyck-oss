#!/usr/bin/env bash

# Run the Bruno API collection headlessly against the local stack.
#
# Requires `task bruno:setup` first. There is no bootstrap account to borrow:
# a token is served only while a live tenant row backs it (see
# docs/tenant-access-control.md), so the collection needs a tenant that the
# setup registers, and the environment it writes carries the tenant, its PAT
# and the OIDC client id. This script only runs the collection against that
# environment; it provisions nothing itself, so a headless run and the GUI
# always drive the same tenant.
#
# Usage:
#   scripts/bruno-run.sh [TEST_PATH]
#
#   TEST_PATH   Optional path under the .bruno collection to run
#               (default: "tests", the whole suite). Examples:
#                 scripts/bruno-run.sh tests/inventory
#                 scripts/bruno-run.sh tests/main-data/cross-main-data-patch-lifecycle
#
# Environment overrides, for running against something other than the tenant
# the setup provisioned:
#   BRUNO_TOKEN      Auth token, overrides the environment's env_pyck_token
#   PYCK_TENANT_ID   Tenant id, overrides its env_pyck_tenant_id
#   BRUNO_ENV        Bruno environment name (default: local)

set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly PROJECT_ROOT="$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel 2>/dev/null || echo "$SCRIPT_DIR/..")"
readonly BRUNO_DIR="$PROJECT_ROOT/.bruno"

TEST_PATH="${1:-tests}"
BRUNO_ENV="${BRUNO_ENV:-local}"
readonly ENV_FILE="$BRUNO_DIR/environments/$BRUNO_ENV.yml"

die() { echo "error: $*" >&2; exit 1; }

command -v bru >/dev/null 2>&1 || die "bru CLI not found (install @usebruno/cli)"
[ -d "$BRUNO_DIR" ] || die "Bruno collection not found at $BRUNO_DIR"

if [ ! -f "$ENV_FILE" ]; then
  cat >&2 <<MSG
error: ${ENV_FILE#"$PROJECT_ROOT"/} does not exist.

The collection ships no local environment: it holds a tenant PAT and the
system token, so it is generated rather than committed. Create it with:

  task up            # if the stack is not running
  task bruno:setup

That registers the tenant the collection runs against and writes the
environment. Then re-run this script.
MSG
  exit 1
fi

# Overrides are layered on top of the environment rather than replacing it,
# so the client id and the base URLs still come from the file.
overrides=()
[ -n "${BRUNO_TOKEN:-}" ] && overrides+=(--env-var "env_pyck_token=$BRUNO_TOKEN")
[ -n "${PYCK_TENANT_ID:-}" ] && overrides+=(--env-var "env_pyck_tenant_id=$PYCK_TENANT_ID")

echo "Running Bruno tests"
echo "  path: $TEST_PATH"
echo "  env:  $BRUNO_ENV (${ENV_FILE#"$PROJECT_ROOT"/})"
echo

# bru must run from the collection root. -r: bru >=3 no longer recurses into
# nested folders by default and silently reports 0 requests without it.
# --sandbox developer: the default "safe" QuickJS WASM sandbox leaks memory
# per script execution and dies with "memory access out of bounds" after
# ~450 requests, failing everything after it.
cd "$BRUNO_DIR"
exec bru run "$TEST_PATH" -r \
  --sandbox developer \
  --env "$BRUNO_ENV" \
  ${overrides[@]+"${overrides[@]}"}
