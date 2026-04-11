#!/bin/bash
set -euo pipefail

if [ -z "${SERVICE_NAME:-}" ]; then
    echo "Error: SERVICE_NAME environment variable is not set." >&2
    exit 1
fi

exec watchexec \
    --on-busy-update=restart \
    --stop-timeout 100ms \
    --debounce 500ms \
    --filter '**/*.go' \
    --ignore '/app/*/.var/**/*' \
    --watch "/app/common/" \
    --watch "/app/${SERVICE_NAME}/" \
    --print-events \
    --timings \
    --color=always \
    --clear=clear \
    "$@"
