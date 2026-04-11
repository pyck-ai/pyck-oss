#!/bin/sh
set -e

AUDIENCE=https://auth.local.pyck.cloud:8080
KEY_FILE_PATH=./config/keys/zitadel-admin-sa.json
ZITADEL_ADMIN_EMAIL="zitadel-admin@zitadel.auth.local.pyck.cloud"


function sedinplace() {
  # This function is used to determine the correct sed command for in-place editing
  # It checks if GNU sed or BSD/macOS sed is being used and sets the appropriate flags
  if sed --version >/dev/null 2>&1; then
    # GNU sed: -i without argument
    sed -E -i "$@"
  else
    # BSD/macOS sed: -i with empty argument
    sed -E -i '' "$@"
  fi
}

function setdotenv() {
  local key="$1"
  local value="$2"

  if grep -q "^${key}=" .env; then
    sedinplace "s|^(#\s*)?${key}=.*|\\1${key}=${value}|g" .env
  else
    echo "${key}=${value}" >> .env
  fi
}


# copy .env
if [ ! -f .env ]; then
  cp .env.example .env
fi


# setup zitadel
if ! pyck setup zitadel > zitadel.json.tmp \
  --admin-email="$ZITADEL_ADMIN_EMAIL" \
  --issuer="$AUDIENCE" \
  --jwt-profile-path="$KEY_FILE_PATH"
then
  echo >&2 "💥 Failed to setup zitadel."
  echo >&2
  echo >&2 "\$ docker compose logs zitadel"
  echo >&2 "-------------------------------------------------"
  docker compose logs --no-log-prefix -n 100 zitadel \
    | grep -E ' level=(warning|error) ' \
    | tail -n 10
  echo >&2 "-------------------------------------------------"
  exit 1
fi

SERVICE_TOKEN="$(cat zitadel.json.tmp | jq -r .service_token)"
ORG_ID="$(cat zitadel.json.tmp | jq -r .org_id)"
PROJECT_ID="$(cat zitadel.json.tmp | jq -r .project_id)"
AUDIENCE="$(cat zitadel.json.tmp | jq -r .audience)"
ZITADEL_ADMIN_PASSWORD="$(cat zitadel.json.tmp | jq -r .admin_password)"

setdotenv PYCK_SERVICE_TOKEN "$SERVICE_TOKEN"
setdotenv PYCK_ZITADEL_ORG_ID "$ORG_ID"
setdotenv PYCK_ZITADEL_PROJECT_ID "$PROJECT_ID"
setdotenv PYCK_ZITADEL_AUDIENCE "$AUDIENCE"


# write key file for services
cat zitadel.json.tmp | jq -r .key_file_json > ./config/keys/local-key.json


# setup zitadel tenant
if ! pyck setup tenant > tenant.json.tmp \
  --issuer="$AUDIENCE" \
  --jwt-profile-path="$KEY_FILE_PATH" \
  --name "localDev" \
  --project-id=$PROJECT_ID
then
  echo >&2 "💥 Failed to setup zitadel tenant."
  echo >&2
  echo >&2 "\$ docker compose logs zitadel"
  echo >&2 "-------------------------------------------------"
  docker compose logs --no-log-prefix -n 100 zitadel \
    | grep -E ' level=(warning|error) ' \
    | tail -n 10
  echo >&2 "-------------------------------------------------"
  exit 1
fi

API_TOKEN=$(cat tenant.json.tmp | jq -r .api_user_token)
SERVICE_WORKER_TOKEN=$(cat tenant.json.tmp | jq -r .service_worker_token)

setdotenv PYCK_TEST_AUTH_TOKEN "$API_TOKEN"
setdotenv PYCK_SERVICE_WORKER_TOKEN "$SERVICE_WORKER_TOKEN"


# setup OIDC Debug Frontend
if ! pyck setup oidc-debug > oidc-debug.json.tmp \
  --issuer="$AUDIENCE" \
  --jwt-profile-path="$KEY_FILE_PATH" \
  --project-id=$PROJECT_ID
then
  echo >&2 "💥 Failed to setup OIDC debug."
  echo >&2
  echo >&2 "\$ docker compose logs zitadel"
  echo >&2 "-------------------------------------------------"
  docker compose logs --no-log-prefix -n 100 zitadel \
    | grep -E ' level=(warning|error) ' \
    | tail -n 10
  echo >&2 "-------------------------------------------------"
  exit 1
fi

OIDC_CLIENT_ID=$(cat oidc-debug.json.tmp | jq -r .client_id)
OIDC_CLIENT_SECRET=$(cat oidc-debug.json.tmp | jq -r .client_secret)

setdotenv OIDC_CLIENT_ID "$OIDC_CLIENT_ID"
setdotenv OIDC_CLIENT_SECRET "$OIDC_CLIENT_SECRET"

# sedinplace "s/const CLIENT_ID = '.*'/const CLIENT_ID = '$OIDC_CLIENT_ID'/g" ./config/oidc-debug/server.js
# sedinplace "s/const CLIENT_SECRET = '.*'/const CLIENT_SECRET = '$OIDC_CLIENT_SECRET'/g" ./config/oidc-debug/server.js


# setup Zitadel sync trigger (webhook called when org metadata changes)
# host.docker.internal is used because Zitadel runs in Docker and needs to reach the management service on the host
MANAGEMENT_WEBHOOK_URL="http://host.docker.internal:8082/webhook/zitadel/organization/sync"

if ! pyck setup zitadel-sync-trigger \
  --issuer="$AUDIENCE" \
  --jwt-profile-path="$KEY_FILE_PATH" \
  --webhook-url="$MANAGEMENT_WEBHOOK_URL"
then
  echo >&2 "⚠️ Failed to setup Zitadel sync trigger. Continuing..."
fi


# cleanup
rm -f zitadel.json.tmp tenant.json.tmp debug.json.tmp oidc-debug.json.tmp


# print login information
cat >&2 <<EOF
-------------------------------------------------
You can now log in to Zitadel:

  URL:      $AUDIENCE
  Username: $ZITADEL_ADMIN_EMAIL
  Password: $ZITADEL_ADMIN_PASSWORD

The OIDC Debug UI is available at:

  URL: http://localhost:3000

For pyck API requests, use the following token:

  Bearer $API_TOKEN

-------------------------------------------------
EOF
