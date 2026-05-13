#!/usr/bin/env bash
set -euo pipefail

# Runs `mutation { ensureTemporalNamespace }` against the gateway for every
# tenant returned by the `tenants` query.
#
# Usage:
#   scripts/ensure-temporal-namespaces.sh --gateway <url> --token <bearer>
#   scripts/ensure-temporal-namespaces.sh -g <url> -t <bearer>
#   GATEWAY_URL=<url> TOKEN=<bearer> scripts/ensure-temporal-namespaces.sh
#
# Examples:
#   scripts/ensure-temporal-namespaces.sh -g http://localhost:4000 -t "$TOKEN"
#   scripts/ensure-temporal-namespaces.sh -g https://gateway.dev.pyck.ai -t "$TOKEN"
#
# Optional:
#   PAGE_SIZE  number of tenants per page (default 100)

GATEWAY_URL="${GATEWAY_URL:-}"
TOKEN="${TOKEN:-}"
PAGE_SIZE="${PAGE_SIZE:-100}"

usage() {
  sed -n '3,16p' "$0" | sed 's/^# \{0,1\}//'
  exit "${1:-0}"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    -g|--gateway) GATEWAY_URL="$2"; shift 2 ;;
    -t|--token)   TOKEN="$2"; shift 2 ;;
    -h|--help)    usage 0 ;;
    *) echo "unknown argument: $1" >&2; usage 2 ;;
  esac
done

if [[ -z "${GATEWAY_URL}" ]]; then
  echo "gateway URL required (--gateway or GATEWAY_URL env)" >&2
  exit 2
fi
if [[ -z "${TOKEN}" ]]; then
  echo "token required (--token or TOKEN env)" >&2
  exit 2
fi

if ! command -v jq >/dev/null 2>&1; then
  echo "jq is required" >&2
  exit 1
fi

graphql() {
  local tenant_header="$1"
  local body="$2"
  curl -sS --fail-with-body \
    -H "Authorization: Bearer ${TOKEN}" \
    -H "X-Pyck-Tenant-Id: ${tenant_header}" \
    -H "Content-Type: application/json" \
    -X POST "${GATEWAY_URL}" \
    -d "${body}"
}

list_tenants() {
  local query body response cursor="null"
  local -a ids=()

  query='query($first: Int!, $after: Cursor) { tenants(first: $first, after: $after) { pageInfo { hasNextPage endCursor } edges { node { id } } } }'

  while :; do
    body="$(jq -nc --arg q "${query}" --argjson first "${PAGE_SIZE}" --argjson after "${cursor}" \
      '{query: $q, variables: {first: $first, after: $after}}')"

    response="$(graphql "all" "${body}")"

    if echo "${response}" | jq -e '.errors' >/dev/null 2>&1; then
      echo "tenants query failed:" >&2
      echo "${response}" | jq . >&2
      exit 1
    fi

    while IFS= read -r id; do
      ids+=("${id}")
    done < <(echo "${response}" | jq -r '.data.tenants.edges[].node.id')

    if [[ "$(echo "${response}" | jq -r '.data.tenants.pageInfo.hasNextPage')" != "true" ]]; then
      break
    fi
    cursor="$(echo "${response}" | jq -c '.data.tenants.pageInfo.endCursor')"
  done

  printf '%s\n' "${ids[@]}"
}

ensure_for_tenant() {
  local tenant_id="$1"
  local response
  response="$(graphql "${tenant_id}" '{"query":"mutation { ensureTemporalNamespace }"}')" || {
    echo "FAIL  ${tenant_id}  (transport)"
    return 1
  }

  if echo "${response}" | jq -e '.errors' >/dev/null 2>&1; then
    local err
    err="$(echo "${response}" | jq -c '.errors')"
    echo "FAIL  ${tenant_id}  ${err}"
    return 1
  fi

  local ok
  ok="$(echo "${response}" | jq -r '.data.ensureTemporalNamespace')"
  echo "OK    ${tenant_id}  ensureTemporalNamespace=${ok}"
}

echo "Listing tenants from ${GATEWAY_URL}..."
TENANT_IDS=()
while IFS= read -r line; do
  [[ -n "${line}" ]] && TENANT_IDS+=("${line}")
done < <(list_tenants)
echo "Found ${#TENANT_IDS[@]} tenants"

failures=0
for tid in "${TENANT_IDS[@]}"; do
  if ! ensure_for_tenant "${tid}"; then
    failures=$((failures + 1))
  fi
done

echo
echo "Done. ${#TENANT_IDS[@]} tenants processed, ${failures} failures."
exit "${failures}"
