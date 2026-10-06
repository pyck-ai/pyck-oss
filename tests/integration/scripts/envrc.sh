#!/usr/bin/env bash
#
# Loads the integration-test env. Source this into your shell / IDE (bash or
# zsh) to run or debug suites by hand, or let scripts/start.sh source it —
# either way the env is identical. Sets PYCK_ROOT, sources the pyck
# bootstrap.env, validates the credentials the suites need, and forces the
# host-facing localhost URLs.
#
# Usage (from the tests/integration module root):
#
#   source ./scripts/envrc.sh
#   go test -tags=integration -v -count=1 ./tests/auth-claims/...
#
# It returns (not exits) on error when sourced, so a missing stack won't kill
# your shell.

# Resolve the repo root from this file's location (tests/integration/scripts/),
# unless the caller pinned PYCK_ROOT.
_envrc_dir=$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)
export PYCK_ROOT="${PYCK_ROOT:-$(cd "$_envrc_dir/../../.." && pwd)}"

_bootstrap="$PYCK_ROOT/config/keys/bootstrap.env"
if [[ ! -f "$_bootstrap" ]]; then
  echo "$_bootstrap missing — run: cd $PYCK_ROOT && task up" >&2
  return 1 2>/dev/null || exit 1
fi

# shellcheck disable=SC1090
set -a; source "$_bootstrap"; set +a

# printenv (not ${!var}) so the check also works when sourced from zsh,
# which has no bash-style indirect expansion.
for _v in PYCK_SERVICE_TOKEN PYCK_ZITADEL_PROJECT_ID; do
  if [[ -z "$(printenv "$_v" || true)" ]]; then
    echo "$_v not in $_bootstrap" >&2
    return 1 2>/dev/null || exit 1
  fi
done
unset _v

# Force the host-facing URLs even if the shell already had in-cluster
# equivalents exported (e.g. from sourcing pyck/.env, which carries
# PYCK_GATEWAY_URL=http://gateway:4000 for the compose services). The suites
# run on the host, so localhost is always the right answer.
export PYCK_GATEWAY_URL=http://localhost:4000
export PYCK_ZITADEL_ISSUER=http://localhost:8080
export PYCK_ZITADEL_GRPC_ADDR=localhost:8080
export PYCK_ZITADEL_ADMIN_KEYFILE="$PYCK_ROOT/config/keys/zitadel-admin-sa.json"

# Forward only the *_INTERVAL vars so sweep-timing tests (e.g. tenant expiry)
# can size their waits to the management service's ACTUAL cadence. The source
# of truth is the running management container's environment: that is what the
# stack really uses, including the fast values from
# config/compose/integration.yaml (`task up:integration`), which .env does not
# carry. If docker or the container is unavailable, fall back to the .env file.
# Exported via read+export — NOT source/eval, which would expand $(...) and
# other metacharacters in the value — and the value charset is pinned to
# duration syntax so a malformed line is skipped, not executed.
_interval_re='^PYCK_[A-Z_]+_INTERVAL=[0-9smh.]+$'
_interval_lines=""
if command -v docker >/dev/null 2>&1; then
  _interval_lines=$(docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' pyck-management 2>/dev/null \
    | grep -E "$_interval_re" || true)
fi
if [[ -z "$_interval_lines" && -f "$PYCK_ROOT/.env" ]]; then
  _interval_lines=$(grep -E "$_interval_re" "$PYCK_ROOT/.env" || true)
fi
if [[ -n "$_interval_lines" ]]; then
  while IFS= read -r _line; do
    export "${_line%%=*}=${_line#*=}"
  done <<< "$_interval_lines"
fi
unset _line _interval_re _interval_lines

if [[ ! -f "$PYCK_ZITADEL_ADMIN_KEYFILE" ]]; then
  echo "Zitadel admin keyfile missing: $PYCK_ZITADEL_ADMIN_KEYFILE" >&2
  return 1 2>/dev/null || exit 1
fi
