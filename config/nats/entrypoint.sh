#!/bin/sh

# NATS Entrypoint Script
# Parses PYCK_NATS_URL environment variable to extract username and password
# and sets NATS_USER and NATS_PASSWORD for the NATS server configuration

# URL decode function for special characters
urldecode() {
    local url_encoded="${1//+/ }"
    printf '%b' "${url_encoded//%/\\x}"
}

# Parse PYCK_NATS_URL and extract credentials
# Format: nats://user:password@host:port
if [ -n "$PYCK_NATS_URL" ]; then
    echo "Parsing NATS credentials from PYCK_NATS_URL..."
    
    # Remove the protocol (nats://)
    NATS_URL_NO_PROTO="${PYCK_NATS_URL#nats://}"
    
    # Extract user:password@host:port
    # Split by @ to get credentials and host parts
    CREDS_PART="${NATS_URL_NO_PROTO%%@*}"
    HOST_PORT_PART="${NATS_URL_NO_PROTO#*@}"
    
    # Extract username and password from credentials part
    if [[ "$CREDS_PART" == *":"* ]]; then
        NATS_USER_FROM_URL="${CREDS_PART%%:*}"
        NATS_PASSWORD_FROM_URL="${CREDS_PART#*:}"
        
        # URL decode the values
        export NATS_USER=$(urldecode "$NATS_USER_FROM_URL")
        export NATS_PASSWORD=$(urldecode "$NATS_PASSWORD_FROM_URL")
        
        echo "✓ NATS credentials extracted from PYCK_NATS_URL"
        echo "  NATS_USER: $NATS_USER"
        echo "  NATS_PASSWORD: [hidden]"
    else
        echo "⚠ No credentials found in PYCK_NATS_URL, using defaults or environment variables"
    fi
else
    echo "⚠ PYCK_NATS_URL not set, using NATS_USER and NATS_PASSWORD environment variables"
fi

# Set defaults if not already set
export NATS_USER=${NATS_USER:-root}
export NATS_PASSWORD=${NATS_PASSWORD:-root}

echo "Final NATS configuration:"
echo "  NATS_USER: $NATS_USER"
echo "  NATS_PASSWORD: [hidden]"
echo ""

# Execute the original NATS server entrypoint with all passed arguments
exec docker-entrypoint.sh "$@"