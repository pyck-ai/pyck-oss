#!/usr/bin/env bash
#
# Runs the integration suites: load the env (via scripts/envrc.sh), check the
# stack is reachable, then run the tests. Invoked by `task test:integration`
# (repo root) / `task test:integration:run` (this module).
#
# Uses gotestsum when it's on PATH (matching the rest of the repo's test
# tooling); falls back to `go test` otherwise. Packages run serially (-p 1) by
# default because each suite provisions a real tenant + namespace, and running
# all suites at once exhausts the dev Postgres connection pool.
#
# Forwards all CLI args to the test runner:
#
#   ./scripts/start.sh -run TestProvisioning/Test03
#
# Tunables (env):
#   PYCK_IT_PARALLEL       go test -p value (default 1)
#   PYCK_IT_RACE           1=enable -race (default 1), 0=disable
#   PYCK_IT_WAIT_TIMEOUT   seconds to poll for the stack (default 0 = once)
#   PYCK_IT_NO_GOTESTSUM   1=force plain `go test` even if gotestsum exists

set -euo pipefail

# This script lives at <repo>/tests/integration/scripts; cd to the module root
# so the ./scripts/... and ./tests/... paths below resolve.
cd "$(dirname "$0")/.."

# Load + validate + export the env. Same file you can `source` for IDE use.
# shellcheck source=scripts/envrc.sh
source ./scripts/envrc.sh || exit 1

# ── stack readiness ────────────────────────────────────────────────────────
# A non-200 HTTP code still counts as "up" (the gateway answers GET / with 400,
# Zitadel with 302) — we only care that something is listening. Temporal is
# gRPC, so probe it with a raw TCP connect. Set PYCK_IT_WAIT_TIMEOUT to poll
# (e.g. right after `task up` in CI); default 0 means a single check.
# NOTE: on connection failure curl still prints "000" via -w before exiting
# non-zero, so the fallback must REPLACE the captured value, not append to it
# (`|| echo 000` inside the substitution would yield "000000", which passes
# the != 000 check and reports a dead endpoint as up).
http_up() { local c; c=$(curl -s -o /dev/null -m 5 -w '%{http_code}' "$1" 2>/dev/null) || c=000; [[ "$c" != 000 ]]; }
# Prefer nc (portable); fall back to bash's /dev/tcp, which not every bash
# build ships with.
tcp_up() {
  local hp=$1
  local host=${hp%%:*}
  local port=${hp##*:}
  if command -v nc >/dev/null 2>&1; then
    nc -z -w 5 "$host" "$port" >/dev/null 2>&1
  else
    timeout 5 bash -c ">/dev/tcp/$host/$port" 2>/dev/null
  fi
}

stack_ready() {
  local ok=true
  http_up "$PYCK_GATEWAY_URL"    || { echo "  ✗ gateway unreachable at $PYCK_GATEWAY_URL" >&2; ok=false; }
  http_up "$PYCK_ZITADEL_ISSUER" || { echo "  ✗ zitadel unreachable at $PYCK_ZITADEL_ISSUER" >&2; ok=false; }
  tcp_up  "${PYCK_TEMPORAL_ADDRESS:-localhost:7233}" || { echo "  ✗ temporal unreachable" >&2; ok=false; }
  [[ "$ok" == true ]]
}

deadline=$(( $(date +%s) + ${PYCK_IT_WAIT_TIMEOUT:-0} ))
until stack_ready; do
  if [[ "$(date +%s)" -ge "$deadline" ]]; then
    echo "pyck stack not ready — start it with: cd $PYCK_ROOT && task up" >&2
    exit 1
  fi
  sleep 2
done
echo "✓ pyck stack reachable (gateway, zitadel, temporal)" >&2

# ── run ────────────────────────────────────────────────────────────────────
# Explicit -timeout: several suites size waits from the stack's sweep
# intervals, and go test's default 10m per-package timeout turns a slow
# stack into an undiagnosable goroutine-dump panic instead of a failure.
test_flags=(-tags=integration -count=1 -timeout 20m -p "${PYCK_IT_PARALLEL:-1}")
[[ "${PYCK_IT_RACE:-1}" == "1" ]] && test_flags+=(-race)

if command -v gotestsum >/dev/null 2>&1 && [[ "${PYCK_IT_NO_GOTESTSUM:-}" != "1" ]]; then
  # gotestsum emits GitHub-Actions annotations under GITHUB_ACTIONS=true and a
  # readable per-package summary locally.
  exec gotestsum \
    --format-icons=text \
    --format-hide-empty-pkg \
    --packages=./tests/... \
    -- "${test_flags[@]}" "$@"
fi

exec go test "${test_flags[@]}" -v ./tests/... "$@"
