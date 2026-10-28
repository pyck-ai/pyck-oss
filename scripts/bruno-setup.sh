#!/usr/bin/env bash

# Provision everything the Bruno collection needs against a running local
# stack, and write a ready-to-use Bruno environment.
#
# A token is only served while a live tenant row backs it (see
# docs/tenant-access-control.md), so there is no bootstrap account to log in
# with: the root organization is not a tenant, and a user homed in it
# authenticates nothing. The login has to belong to a tenant, and a tenant
# only comes into existence through the registerTenant mutation. This script
# registers one and points the collection at it.
#
# What it produces, in .bruno/environments/local.yml (gitignored):
#   env_zitadel_client_id   the Bruno OIDC app bootstrap created
#   env_pyck_tenant_id      the provisioned tenant
#   env_pyck_token          an admin PAT with every service gate, for
#                           requests that do not go through the OAuth step
#   env_pyck_system_token   the system service token, for the endpoints only
#                           a system caller may reach (restoreTenant,
#                           setTenantExpiry, setTenantUITemplate, the
#                           user-projection CRUD)
#
# Idempotent, and cheap enough that `task bruno:run` just chains it: the
# tenant, its admin and its machine user are looked up by name and reused, so
# a re-run mints one more PAT on the same machine user and changes nothing
# else. The old PATs are left alone: they expire in a year and nothing reads
# them, which is the trade for keeping this script a straight line.
#
# Two failures worth recognising:
#   "admin username \"bruno\" is taken and no user in the tenant's org
#   carries it": Zitadel login names are unique across the instance here
#   (organizations do not qualify them), so some other organization already
#   holds it. A stack bootstrapped before the root-org bruno account was
#   removed will do this; rebuild it with `docker compose down -v && task up`.
#
#   "tenant \"bruno-local\" ... is \"error\"": a previous run failed
#   part-way and left the row behind. Wipe the stack
#   (`docker compose down -v && task up`): retiring the row with
#   deleteTenant does not free its name, so the re-run would still be
#   refused.
#
# Usage:
#   task bruno:setup            # or: scripts/bruno-setup.sh
#
# Then open Bruno, select the "local" environment, and run the "Zitadel
# Login" request, logging in with the credentials printed at the end.

set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly PROJECT_ROOT="$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel 2>/dev/null || echo "$SCRIPT_DIR/..")"
readonly KEYS_DIR="$PROJECT_ROOT/config/keys"
readonly TEMPLATE="$SCRIPT_DIR/bruno-template.yaml"
# .yml, not .yaml: the Bruno CLI resolves `--env local` to
# environments/local.yml and fails outright on any other spelling.
readonly GENERATED="$PROJECT_ROOT/.bruno/environments/local.yml"

# The tenant is a durable local fixture, so its names are fixed rather than
# generated: you type the login, and a re-run reuses what is already there.
readonly TENANT_NAME="bruno-local"
readonly ADMIN_USERNAME="bruno"
readonly ADMIN_PASSWORD="Password1!"
readonly MACHINE_USER="bruno-runner"

die() { echo "error: $*" >&2; exit 1; }

read_env_var() { # file, name
  [ -f "$1" ] || die "$1 not found; run 'task up' first"
  local value
  value="$(grep -m1 "^$2=" "$1" | cut -d= -f2-)"
  [ -n "$value" ] || die "$2 not found in $1"
  printf '%s' "$value"
}

CLIENT_ID="$(read_env_var "$KEYS_DIR/bruno.env" PYCK_BRUNO_CLIENT_ID)"
SYSTEM_TOKEN="$(read_env_var "$KEYS_DIR/bootstrap.env" PYCK_SERVICE_TOKEN)"
[ -f "$TEMPLATE" ] || die "$TEMPLATE not found"

echo "Provisioning tenant '$TENANT_NAME'..." >&2
# shellcheck disable=SC1091
source "$PROJECT_ROOT/tests/integration/scripts/envrc.sh" >/dev/null
provisioned="$(cd "$PROJECT_ROOT/tests/integration" \
  && go run -tags=integration ./cmd/provision-tenant \
    -name "$TENANT_NAME" \
    -admin-username "$ADMIN_USERNAME" \
    -admin-password "$ADMIN_PASSWORD" \
    -machine-user "$MACHINE_USER")" \
  || die "provisioning failed (is the stack up? try 'task up')"
eval "$provisioned"

# Render bruno-template.yaml with the client id filled in and the per-stack
# values appended. The template lives here rather than in
# .bruno/environments/ so Bruno never offers it as a selectable environment:
# it carries no client id and no token, and picking it by mistake fails with
# Errors.App.NotFound, which names neither.
#
# Every value is quoted, and the client id MUST be: it is a Zitadel snowflake
# past 2^53, so an unquoted one is read as a YAML number, lands in a float64
# and comes back with its last digits changed, which the OAuth step reports
# as Errors.App.NotFound, naming neither the id nor the cause. The tracked
# pyck-dev and pyck-test environments quote theirs for the same reason.
{
  sed "s|__CLIENT_ID__|$CLIENT_ID|" "$TEMPLATE"
  cat <<YAML
  - name: env_pyck_tenant_id
    value: "$PYCK_TEST_TENANT_ID"
  - name: env_pyck_token
    value: "$PYCK_TEST_AUTH_TOKEN"
  - name: env_pyck_system_token
    value: "$SYSTEM_TOKEN"
YAML
} > "$GENERATED"

cat >&2 <<MSG

Wrote ${GENERATED#"$PROJECT_ROOT"/}

In Bruno, select the "local" environment. If it was already open, reload it:
Bruno holds the previous values in the editor.

  Tenant              $TENANT_NAME ($PYCK_TEST_TENANT_ID)
  Log in as           $PYCK_TEST_ADMIN_USERNAME / $PYCK_TEST_ADMIN_PASSWORD

The env_pyck_token PAT, which the collection sends on every request, holds
admin plus every service gate; the login holds admin (which implies writer
and reader), so the admin-gated endpoints (assignRoles, removeRoles,
userServiceRoles, deleteTenant) work for both. On a tenant registered before
the platform delegated admin, the org's project grant and the reused machine
user's own authorization are widened in place; the human admin keeps
writer, so recreate the stack (docker compose down -v && task up) if the
login needs admin. restoreTenant, setTenantExpiry and the user-projection CRUD
require a system caller: run those with env_pyck_system_token. See
docs/tenant-access-control.md.
MSG
