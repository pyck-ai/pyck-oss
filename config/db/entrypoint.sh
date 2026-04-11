#!/bin/sh

# Source this file to export PostgreSQL environment variables from
# PYCK_DATABASE_MASTER_URL
# Usage: source config/db/source-env.sh

# Parse PYCK_DATABASE_MASTER_URL and export PostgreSQL environment variables
# Format: postgres://user:password@host:port/database?params

# Check if PYCK_DATABASE_MASTER_URL is set
if [ -z "$PYCK_DATABASE_MASTER_URL" ]; then
    echo "Error: PYCK_DATABASE_MASTER_URL is not set" >&2
    return 1 2>/dev/null || exit 1
fi

# Parse the database URL
DB_URL="$PYCK_DATABASE_MASTER_URL"

# Remove the protocol (postgres:// or postgresql://)
DB_URL_NO_PROTO="${DB_URL#postgres://}"
DB_URL_NO_PROTO="${DB_URL_NO_PROTO#postgresql://}"

# Extract user:password@host:port/database
# Split by @ to get credentials and host parts
CREDS_PART="${DB_URL_NO_PROTO%%@*}"
HOST_DB_PART="${DB_URL_NO_PROTO#*@}"

# Extract username and password from credentials part
if [[ "$CREDS_PART" == *":"* ]]; then
    DB_USER="${CREDS_PART%%:*}"
    DB_PASSWORD="${CREDS_PART#*:}"
else
    DB_USER="$CREDS_PART"
    DB_PASSWORD=""
fi

# Extract host, port, and database from host part
# First remove any query parameters
HOST_DB_CLEAN="${HOST_DB_PART%%\?*}"

# Extract database name (after the last /)
DB_NAME="${HOST_DB_CLEAN##*/}"

# Extract host and port (before the last /)
HOST_PORT="${HOST_DB_CLEAN%/*}"

# Split host and port
if [[ "$HOST_PORT" == *":"* ]]; then
    DB_HOST="${HOST_PORT%%:*}"
    DB_PORT="${HOST_PORT#*:}"
else
    DB_HOST="$HOST_PORT"
    DB_PORT="5432"  # Default PostgreSQL port
fi

# URL decode function for special characters
urldecode() {
    local url_encoded="${1//+/ }"
    printf '%b' "${url_encoded//%/\\x}"
}

# Decode the values
export POSTGRES_USER=$(urldecode "$DB_USER")
export POSTGRES_PASSWORD=$(urldecode "$DB_PASSWORD")
export POSTGRES_DB=$(urldecode "$DB_NAME")
export POSTGRES_HOST=$(urldecode "$DB_HOST")
export POSTGRES_PORT=$(urldecode "$DB_PORT")

# Validate required fields
if [ -z "$POSTGRES_USER" ] || [ -z "$POSTGRES_DB" ]; then
    echo "Error: Could not parse required fields from database URL" >&2
    return 1 2>/dev/null || exit 1
fi

# Execute the original entrypoint script with all passed arguments
exec /usr/local/bin/docker-entrypoint.sh "$@"