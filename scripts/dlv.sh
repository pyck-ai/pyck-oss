#!/bin/bash
set -uo pipefail

# Configuration with defaults
DLV_BINARY="${DLV_BINARY:-$(command -v dlv || echo 'dlv')}"
DEBUG_DIR="${DEBUG_DIR:-/tmp/debug}"
DLV_API_VERSION="${DLV_API_VERSION:-2}"
BUILD_TAGS="${BUILD_TAGS:-debug}"
GCFLAGS="${GCFLAGS:-all=-N -l}"

# Color codes for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Logging functions
log_info() {
    echo -e "${GREEN}[dlv] INF${NC}: $*"
}

log_warn() {
    echo -e "${YELLOW}[dlv] WRN${NC}: $*"
}

log_error() {
    echo -e "${RED}[dlv] ERR${NC}: $*"
}

# Validation functions
validate_environment() {
    local errors=0

    if [ -z "${SERVICE_NAME:-}" ]; then
        log_error "SERVICE_NAME environment variable is not set"
        ((errors++))
    fi

    if [ -z "${DELVE_PORT:-}" ]; then
        log_error "DELVE_PORT environment variable is not set"
        ((errors++))
    fi

    if ! command -v "$DLV_BINARY" >/dev/null 2>&1; then
        log_error "Delve binary not found at: $DLV_BINARY"
        ((errors++))
    fi

    if [ $errors -gt 0 ]; then
        exit 1
    fi
}

# Setup debug directory
setup_debug_dir() {
    if [ ! -d "$DEBUG_DIR" ]; then
        log_info "Creating debug directory: $DEBUG_DIR"
        mkdir -p "$DEBUG_DIR"
    fi
}

# Check if port is available
check_port() {
    local port="$1"
    if command -v netstat >/dev/null 2>&1; then
        if netstat -ln 2>/dev/null | grep -q ":$port "; then
            log_warn "Port $port appears to be in use"
            return 1
        fi
    elif command -v ss >/dev/null 2>&1; then
        if ss -ln 2>/dev/null | grep -q ":$port "; then
            log_warn "Port $port appears to be in use"
            return 1
        fi
    fi
    return 0
}

# Cleanup function
cleanup() {
    local pids=("$@")
    log_info "Cleaning up processes: ${pids[*]}"
    for pid in "${pids[@]}"; do
        if kill -0 "$pid" 2>/dev/null; then
            log_info "Terminating process $pid"
            kill "$pid" 2>/dev/null || true
        fi
    done

    # Wait for processes to terminate
    for pid in "${pids[@]}"; do
        if kill -0 "$pid" 2>/dev/null; then
            log_info "Waiting for process $pid to terminate"
            wait "$pid" 2>/dev/null || true
        fi
    done
}

# Debug mode function
debug_mode() {
    local service_path

    # Check if a custom path is provided as first argument
    if [ -n "$1" ] && [[ "$1" == ./* ]]; then
        service_path="$1"
        shift # Remove the path from arguments
        log_info "Using custom debug path: $service_path"
    else
        service_path="./cmd/pyck-${SERVICE_NAME}"
        log_info "Using default debug path: $service_path"
    fi

    log_info "Starting service '$SERVICE_NAME' in debug mode..."
    log_info "- Debugee: $service_path"
    log_info "- Debug port: $DELVE_PORT"
    log_info "- Log file: $DEBUG_DIR/dlv.log"
    log_info "Press Ctrl+C to stop"

    # Start delve debugger
    "$DLV_BINARY" debug \
        --accept-multiclient \
        --headless \
        --continue \
        --disable-aslr \
        --build-flags="-tags=$BUILD_TAGS -gcflags='$GCFLAGS'" \
        --api-version="$DLV_API_VERSION" \
        --log \
        --log-dest="$DEBUG_DIR/dlv.log" \
        --output="$DEBUG_DIR/main" \
        --listen=":${DELVE_PORT}" \
        "$service_path" \
        "$@" &

    local dlv_pid=$!

    trap "cleanup $dlv_pid" EXIT
    wait $dlv_pid
}

# Monitor delve log file for attach confirmation
monitor_delve_log() {
    local log_file="$1"
    local monitor_pid_file="$2"

    # Create log file if it doesn't exist
    touch "$log_file"

    # Monitor log file in background
    tail -F "$log_file" 2>/dev/null | while IFS= read -r line; do
        if [[ "$line" == *"attaching to pid"* ]]; then
            log_info "Debugger successfully attached: $line"
        fi
    done &

    local monitor_pid=$!
    echo "$monitor_pid" > "$monitor_pid_file"
}

# Attach mode function - starts program in background, runs debugger in foreground
attach_mode() {
    if [ $# -eq 0 ]; then
        log_error "Attach mode requires a program to run as argument"
        log_error "Usage: SERVICE_NAME=foo DELVE_PORT=2345 $0 attach /usr/local/bin/foo"
        exit 1
    fi

    local program="$1"
    shift # Remove program from args, pass rest to program

    log_info "Attaching service '$SERVICE_NAME' in debug mode..."
    log_info "- Entrypoint: $program"
    log_info "- Args: $*"
    log_info "- Debug port: $DELVE_PORT"
    log_info "- Log file: $DEBUG_DIR/dlv.log"
    log_info "Press Ctrl+C to stop"

    local log_file="$DEBUG_DIR/dlv.log"
    local monitor_pid_file="$DEBUG_DIR/monitor.pid"

    # Start log monitoring
    monitor_delve_log "$log_file" "$monitor_pid_file"
    local monitor_pid=$(<"$monitor_pid_file")

    while true; do
        "$DLV_BINARY" attach \
            --accept-multiclient \
            --headless \
            --continue \
            --api-version="$DLV_API_VERSION" \
            --log \
            --log-dest="$log_file" \
            --listen=":${DELVE_PORT}" \
            --waitfor="${SERVICE_NAME}" \
            --waitfor-duration=10
    done &

    local dlv_pid=$!

    # Signal handler that only cleans up monitor process
    cleanup_on_exit() {
        log_info "Cleaning up monitor process only"
        if kill -0 $monitor_pid 2>/dev/null; then
            kill $monitor_pid 2>/dev/null || true
        fi

        # Clean up monitor PID file
        rm -f "$monitor_pid_file"

        # Leave delve running - don't kill it
        if kill -0 $dlv_pid 2>/dev/null; then
            log_info "Delve debugger (PID: $dlv_pid) remains running for continued debugging"
        fi
    }

    # Set up signal handling - only cleanup monitor on exit, leave delve running
    trap "cleanup_on_exit" EXIT

    log_info "Starting program: $program $*"

    # Use exec to replace the shell with the program so it becomes PID 1
    # This ensures it receives Docker signals (SIGTERM) directly
    exec "$program" "$@"
}

# Usage function
show_usage() {
    cat << EOF
Usage: $0 [debug|attach|help] [options...]

Modes:
  debug [path]     Start service in debug mode with delve (default)
                   Optional path: specify custom service path (e.g., ./cmd/foo/...)
                   Default: ./cmd/pyck-\${SERVICE_NAME}/...
  attach <program> Start delve in background waiting for SERVICE_NAME, then run <program> in foreground
  help             Show this help message

Environment Variables:
  SERVICE_NAME     Name of the service to debug (required)
  DELVE_PORT       Port for delve debugger (required)
  DLV_BINARY       Path to delve binary (default: auto-detect)
  DEBUG_DIR        Directory for debug files (default: /tmp/debug)
  DLV_API_VERSION  Delve API version (default: 2)
  BUILD_TAGS       Go build tags (default: debug)
  GCFLAGS          Go compiler flags (default: all=-N -l)

Examples:
  SERVICE_NAME=workflow DELVE_PORT=2345 $0 debug
  SERVICE_NAME=workflow DELVE_PORT=2345 $0 debug ./cmd/foo/...
  SERVICE_NAME=temporal DELVE_PORT=2346 $0 attach /usr/local/bin/temporal

In attach mode, delve starts in background waiting for '\${SERVICE_NAME}', then the specified program runs in foreground.
Connect your debugger to localhost:\$DELVE_PORT
EOF
}

# Main execution
main() {
    local mode="${1:-debug}"

    case "$mode" in
        help|--help|-h)
            show_usage
            exit 0
            ;;
        debug)
            shift
            validate_environment
            setup_debug_dir
            check_port "$DELVE_PORT" || log_warn "Port check failed, continuing anyway"
            debug_mode "$@"
            ;;
        attach)
            shift
            validate_environment
            setup_debug_dir
            check_port "$DELVE_PORT" || log_warn "Port check failed, continuing anyway"
            attach_mode "$@"
            ;;
        *)
            log_error "Invalid mode '$mode'. Valid modes are 'debug', 'attach', or 'help'"
            show_usage
            exit 1
            ;;
    esac
}

# Run main function with all arguments
main "$@"


