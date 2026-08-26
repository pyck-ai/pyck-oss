#!/bin/bash

# Script to check Temporal version consistency across the repo.
#
# Temporal ships the server three ways under one shared X.Y.Z version line:
#   - the Go module  go.temporal.io/server   (what the custom server builds against)
#   - the Docker image  temporalio/server     (the runtime base image)
#   - the admin-tools image  temporalio/admin-tools (schema setup / CLI tooling)
# These MUST stay on the same version: the running server, its schema tooling and
# the code compiled into it drift apart otherwise. go.temporal.io/server in
# backend/temporal/go.mod is the source of truth; every other reference must match
# its X.Y.Z (the leading server version, ignoring any -tctl-…-cli-… suffix an
# admin-tools tag might carry).
#
# References checked:
#   - ARG TEMPORAL_VERSION=...             server Docker tag (Dockerfiles)
#   - ARG TEMPORAL_ADMINTOOLS_VERSION=...  admin-tools Docker tag (Dockerfiles)
#   - image: temporalio/server:...         compose
#   - image: temporalio/admin-tools:...    compose
# temporalio/ui is intentionally NOT checked: it has its own independent 2.x line.

set -euo pipefail  # Exit on error, undefined vars, pipe failures

# Global variables
SILENT=false
FIX_MODE=false
issues_found=false
readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly PROJECT_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || echo "$SCRIPT_DIR/..")"

# Source of truth for the expected Temporal server version.
readonly SERVER_GOMOD="backend/temporal/go.mod"

# File patterns to scan for version references.
readonly DOCKERFILES=(
  'Dockerfile'
  'Dockerfile.*'
  '**/Dockerfile'
  '**/Dockerfile.*'
)

readonly COMPOSEFILES=(
  '*.yaml'
  '*.yml'
  '**/*.yaml'
  '**/*.yml'
)

# Log error message to stderr
# Args: error_message
function error() {
  echo >&2 "❌ $*"
}

# Log informational message (respects SILENT flag)
# Args: log_message
function log() {
  if [[ "$SILENT" == true ]]; then
    return
  fi
  echo "$*"
}

# Reduce a tag/value to its leading X.Y.Z server version, dropping any suffix
# (e.g. "1.31.1-tctl-1.18.4-cli-1.5.0" -> "1.31.1"). Empty if no semver found.
# Reads from stdin.
function leading_semver() {
  sed -E -n 's/^v?([0-9]+\.[0-9]+\.[0-9]+).*/\1/p' | head -1
}

# Extract the source-of-truth server version from go.temporal.io/server.
function get_source_version() {
  grep -E '^[[:space:]]*go\.temporal\.io/server[[:space:]]+v' "$SERVER_GOMOD" 2>/dev/null \
    | awk '{print $2}' | leading_semver
}

# Compare one found reference against the expected version and record the result.
# Args: expected file label found_value sed_pattern
#   sed_pattern: an -E regex whose first capture group is the literal prefix to
#                keep when --fix rewrites the leading semver.
function check_ref() {
  local expected="$1" file="$2" label="$3" found="$4" sed_pattern="$5"
  local found_semver
  found_semver="$(printf '%s' "$found" | leading_semver)"

  [[ -z "$found_semver" ]] && return 0  # not a pinned version (e.g. :latest); skip

  if [[ "$found_semver" != "$expected" ]]; then
    error "Inconsistent Temporal version in $file ($label): expected $expected, found $found_semver"
    issues_found=true
    if [[ "$FIX_MODE" == true ]]; then
      log "- Updating $label in $file: $found_semver → $expected"
      sed -i -E "s|(${sed_pattern})[0-9]+\.[0-9]+\.[0-9]+|\1${expected}|g" "$file"
    fi
  fi
}

# Verify ARG TEMPORAL_VERSION / TEMPORAL_ADMINTOOLS_VERSION in Dockerfiles.
# Args: expected_version
function verify_dockerfiles() {
  local expected="$1" file_count=0
  log "Checking Temporal versions in Dockerfiles..."

  while read -r dockerfile; do
    [[ -f "$dockerfile" ]] || continue
    file_count=$((file_count + 1))

    local val
    val="$(grep -E '^ARG[[:space:]]+TEMPORAL_VERSION=' "$dockerfile" 2>/dev/null | sed -E 's/.*=//' | head -1 || true)"
    [[ -n "$val" ]] && check_ref "$expected" "$dockerfile" "TEMPORAL_VERSION" "$val" 'ARG[[:space:]]+TEMPORAL_VERSION='

    val="$(grep -E '^ARG[[:space:]]+TEMPORAL_ADMINTOOLS_VERSION=' "$dockerfile" 2>/dev/null | sed -E 's/.*=//' | head -1 || true)"
    [[ -n "$val" ]] && check_ref "$expected" "$dockerfile" "TEMPORAL_ADMINTOOLS_VERSION" "$val" 'ARG[[:space:]]+TEMPORAL_ADMINTOOLS_VERSION='
  done < <(git ls-files "${DOCKERFILES[@]}" 2>/dev/null | sort -u || true)

  log "- Checked $file_count Dockerfiles"
}

# Verify temporalio/server and temporalio/admin-tools image tags in compose files.
# Args: expected_version
function verify_compose() {
  local expected="$1" file_count=0
  log "Checking Temporal image tags in compose files..."

  while read -r composefile; do
    [[ -f "$composefile" ]] || continue
    grep -qE 'image:[[:space:]]*temporalio/(server|admin-tools):' "$composefile" 2>/dev/null || continue
    file_count=$((file_count + 1))

    local image
    for image in server admin-tools; do
      local tag
      tag="$(grep -E "image:[[:space:]]*temporalio/${image}:" "$composefile" 2>/dev/null | sed -E "s|.*temporalio/${image}:||" | head -1 || true)"
      [[ -n "$tag" ]] && check_ref "$expected" "$composefile" "temporalio/${image}" "$tag" "temporalio/${image}:"
    done
  done < <(git ls-files "${COMPOSEFILES[@]}" 2>/dev/null | sort -u || true)

  log "- Checked $file_count compose files"
}

function usage() {
  cat <<'USAGE'
Usage: check-temporal-versions.sh [--fix] [--silent]

Verifies that the temporalio/server Docker tag and temporalio/admin-tools tag
match the go.temporal.io/server version in backend/temporal/go.mod.

  --fix     Rewrite mismatched references to the source-of-truth version.
  --silent  Suppress informational output (errors still print).
USAGE
}

function main() {
  cd "$PROJECT_ROOT"

  for arg in "$@"; do
    case "$arg" in
      --fix) FIX_MODE=true ;;
      --silent) SILENT=true ;;
      -h|--help) usage; exit 0 ;;
      *) error "Unknown argument: $arg"; usage; exit 2 ;;
    esac
  done

  if [[ ! -f "$SERVER_GOMOD" ]]; then
    error "Source-of-truth go.mod not found: $SERVER_GOMOD"
    exit 1
  fi

  local expected
  expected="$(get_source_version)"
  if [[ -z "$expected" ]]; then
    error "Could not determine go.temporal.io/server version from $SERVER_GOMOD"
    exit 1
  fi
  log "Expected Temporal server version (from $SERVER_GOMOD): $expected"

  verify_dockerfiles "$expected"
  verify_compose "$expected"

  if [[ "$issues_found" == true ]]; then
    if [[ "$FIX_MODE" == true ]]; then
      log "✅ Applied fixes; re-run without --fix to confirm consistency."
      exit 0
    fi
    error "Temporal version inconsistencies found. Run '$0 --fix' to align them with $expected."
    exit 1
  fi

  log "✅ All Temporal versions are consistent at $expected"
}

main "$@"
