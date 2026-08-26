#!/bin/bash

# Script to check that the committed ent migrations match the ent schema.
# For every backend service it runs the same migration generator a developer
# runs locally (`task atlas:migrate`, i.e. `go run ent/migrate/main.go <name>`)
# against a throwaway migration name, and fails when the generator emits any
# change — i.e. when the committed migrations have drifted from the schema.
#
# Requires a reachable Postgres (PYCK_DATABASE_MASTER_URL, set by the caller —
# see the Config section below) for the shadow database the generator replays
# migrations into. All other config vars are satisfied with fixed placeholder
# values so core.LoadEnv() succeeds; they are never used by the migration
# path.

set -uo pipefail  # Undefined vars and pipe failures are errors; we handle
                  # per-service failures explicitly rather than aborting early.

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly PROJECT_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || echo "$SCRIPT_DIR/..")"
readonly BACKEND_DIR="$PROJECT_ROOT/backend"

# Services that own an ent schema + handwritten migrations.
readonly SERVICES=(file inventory main-data management picking receiving workflow)

# Throwaway migration name used for the diff probe. Any emitted file is
# cleaned up before the script exits.
readonly PROBE_NAME="ci_drift_check"

function error() {
  echo >&2 "❌ $*"
}

function info() {
  echo "$*"
}

# --- Config -----------------------------------------------------------------
# The generator only reads PYCK_DATABASE_MASTER_URL (the shadow database);
# core.LoadEnv() also requires PYCK_DATABASE_SLAVE_URL to be non-empty but
# never uses it. Both must come from the caller (the environment) — no
# fallback here, so a misconfigured caller fails loudly instead of silently
# testing against the wrong database.
: "${PYCK_DATABASE_MASTER_URL:?PYCK_DATABASE_MASTER_URL must be set}"
: "${PYCK_DATABASE_SLAVE_URL:?PYCK_DATABASE_SLAVE_URL must be set}"
export PYCK_DATABASE_MASTER_URL PYCK_DATABASE_SLAVE_URL

# Everything else below only needs to satisfy core.LoadEnv()'s required/
# notEmpty validation across the services' config structs — it's inert and
# never read on the migration path, so a fixed placeholder is fine.
export PYCK_ENV="${PYCK_ENV:-ci}"
export PYCK_GATEWAY_URL="${PYCK_GATEWAY_URL:-http://gateway:4000}"
export PYCK_TEMPORAL_URL="${PYCK_TEMPORAL_URL:-temporal:7236}"
export PYCK_SERVICE_TOKEN="${PYCK_SERVICE_TOKEN:-ci-dummy-token}"
export PYCK_ZITADEL_OAUTH_URL="${PYCK_ZITADEL_OAUTH_URL:-http://zitadel:8080}"
export PYCK_ZITADEL_GRPC_ADDR="${PYCK_ZITADEL_GRPC_ADDR:-zitadel:8080}"
export PYCK_ZITADEL_AUDIENCE="${PYCK_ZITADEL_AUDIENCE:-http://localhost:8080}"
export PYCK_ZITADEL_ORG_ID="${PYCK_ZITADEL_ORG_ID:-ci-dummy-org}"
export PYCK_ZITADEL_PROJECT_ID="${PYCK_ZITADEL_PROJECT_ID:-ci-dummy-project}"
export PYCK_ZITADEL_APP_KEYFILE="${PYCK_ZITADEL_APP_KEYFILE:-/dev/null}"
export PYCK_ZITADEL_SERVICE_KEYFILE="${PYCK_ZITADEL_SERVICE_KEYFILE:-/dev/null}"
export PYCK_ZITADEL_ACTION_SIGNING_KEY="${PYCK_ZITADEL_ACTION_SIGNING_KEY:-ci-dummy-signing-key}"
export PYCK_NATS_URL="${PYCK_NATS_URL:-nats://nats:4222}"
export PYCK_NATS_STREAM_NAME="${PYCK_NATS_STREAM_NAME:-pyck}"
export PYCK_NATS_WS_URL="${PYCK_NATS_WS_URL:-nats:8889}"
export PYCK_NATS_REPLICAS_NO="${PYCK_NATS_REPLICAS_NO:-1}"
export PYCK_NATS_AUTH_KEY_SEED="${PYCK_NATS_AUTH_KEY_SEED:-ci-dummy-seed}"

# Ensure we're in a git repository (drift is detected via git status).
if ! git -C "$PROJECT_ROOT" rev-parse --git-dir >/dev/null 2>&1; then
  error "Not in a git repository"
  exit 2
fi

if ! command -v go >/dev/null 2>&1; then
  error "go toolchain not found (required to run the migration generator)"
  exit 2
fi

if ! command -v psql >/dev/null 2>&1; then
  error "psql not found (required to prepare the target schema in the shadow DB)"
  error "Install a postgres client (e.g. 'apk add postgresql-client')"
  exit 2
fi

# The generator replays migrations into a shadow database cloned from the
# migration DB and inspects the target schema, which must already exist (it
# does in a local dev DB; a fresh CI DB needs it created). Create it
# idempotently for every service up front — harmless when it already exists.
function ensure_schema() {
  local schema="$1"
  if ! psql "$PYCK_DATABASE_MASTER_URL" -v ON_ERROR_STOP=1 -q \
       -c "CREATE SCHEMA IF NOT EXISTS \"$schema\"" >/dev/null 2>&1; then
    error "failed to ensure schema \"$schema\" exists in the migration DB"
    error "is PYCK_DATABASE_MASTER_URL reachable? ($PYCK_DATABASE_MASTER_URL)"
    exit 2
  fi
}

# Track which services drifted so the final report is comprehensive.
declare -a drifted=()

# Restore the migrations directory of a service to its committed state:
# revert modified tracked files (e.g. atlas.sum) and remove generated files.
function restore_migrations() {
  local migrations_dir="$1"
  git -C "$PROJECT_ROOT" checkout -q -- "$migrations_dir" 2>/dev/null || true
  git -C "$PROJECT_ROOT" clean -fdq -- "$migrations_dir" 2>/dev/null || true
}

for service in "${SERVICES[@]}"; do
  service_dir="$BACKEND_DIR/$service"
  migrations_dir="$service_dir/ent/migrate/migrations"

  info ""
  info "=== Checking $service for schema/migration drift ==="

  if [[ ! -f "$service_dir/ent/migrate/main.go" ]]; then
    error "$service: ent/migrate/main.go not found — skipping"
    drifted+=("$service")
    continue
  fi

  # The target schema matches the service directory name.
  ensure_schema "$service"

  # Make sure we start from a clean migrations directory so any change we see
  # afterward is attributable to this run.
  restore_migrations "$migrations_dir"

  # Run the same generator a developer runs locally.
  gen_output="$(cd "$service_dir" && go run -mod=readonly ent/migrate/main.go "$PROBE_NAME" 2>&1)"
  gen_status=$?

  if [[ $gen_status -ne 0 ]]; then
    error "$service: migration generator failed (exit $gen_status)"
    echo "$gen_output" | sed 's/^/    /' >&2
    drifted+=("$service")
    restore_migrations "$migrations_dir"
    continue
  fi

  # Any change in the migrations directory (new file or modified atlas.sum)
  # means the committed migrations no longer match the schema.
  changes="$(git -C "$PROJECT_ROOT" status --porcelain -- "$migrations_dir")"

  if [[ -n "$changes" ]]; then
    error "$service: schema and migrations have DRIFTED"
    info "    Generated migration SQL:"
    # Print the SQL of every newly generated up-migration file.
    while IFS= read -r line; do
      file="${line:3}"
      if [[ "$file" == *"$PROBE_NAME"*.sql ]]; then
        echo "    --- $file ---"
        sed 's/^/      /' "$PROJECT_ROOT/$file"
      fi
    done <<< "$changes"
    drifted+=("$service")
  else
    info "✅ $service is in sync"
  fi

  # Always clean up so no throwaway files are left behind.
  restore_migrations "$migrations_dir"
done

info ""
if [[ ${#drifted[@]} -gt 0 ]]; then
  error "Schema/migration drift detected in: ${drifted[*]}"
  info "💡 Run 'task atlas:migrate -- <name>' in the affected service(s) and commit the result"
  exit 1
fi

info "✅ All services: schema and migrations are in sync"
