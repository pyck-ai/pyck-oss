#!/bin/bash

# Script to check Go version consistency across all project files
# Uses go.work as the source of truth for expected versions
# Checks: go.mod files, Dockerfiles

set -euo pipefail  # Exit on error, undefined vars, pipe failures

# Global variables
SILENT=false
DRY_RUN=false
FIX_MODE=false
issues_found=false
readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly PROJECT_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || echo "$SCRIPT_DIR/..")"

# Arrays to store fixes
declare -a fixes=()

# File patterns to check
readonly DOCKERFILES=(
  'Dockerfile'
  'Dockerfile.*'
  '**/Dockerfile'
  '**/Dockerfile.*'
)

readonly GOMODFILES=(
  'go.mod'
  '**/go.mod'
)

# Extract Go version from a file
# Args: file_path
# Returns: version string (e.g., "1.21")
function get_go_version() {
  local file="$1"
  if [[ ! -f "$file" ]]; then
    return 1
  fi
  grep -E "^go\s+" "$file" 2>/dev/null | awk '{print $2}' | head -1 || true
}

# Extract toolchain version from a file
# Args: file_path
# Returns: toolchain version (e.g., "go1.21.1")
function get_toolchain_version() {
  local file="$1"
  if [[ ! -f "$file" ]]; then
    return 1
  fi
  grep -E "^toolchain\s+" "$file" 2>/dev/null | awk '{print $2}' | head -1 || true
}

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

# Verify Go version consistency across all go.mod files
# Args: expected_version
function verify_go_version() {
  local expected_version="$1"
  local file_count=0
  
  log "Checking Go version in go.mod files..."

  while read -r modfile; do
    [[ -f "$modfile" ]] || continue
    file_count=$((file_count + 1))
    
    local mod_version
    mod_version="$(get_go_version "$modfile")"
    
    if [[ -z "$mod_version" ]]; then
      error "No Go version found in $modfile"
      issues_found=true
      continue
    fi

    if [[ "$mod_version" != "$expected_version" ]]; then
      error "Inconsistent Go version in $modfile: expected $expected_version, found $mod_version"
      issues_found=true
      
      # Collect fix information
      if [[ "$FIX_MODE" == true ]]; then
        fixes+=("go_version|$modfile|$expected_version|$mod_version")
      fi
    fi
  done < <(git ls-files "${GOMODFILES[@]}" 2>/dev/null || true)
  
  log "- Checked $file_count go.mod files"
}

# Verify toolchain version consistency across all go.mod files
# Args: expected_version
function verify_toolchain_version() {
  local expected_version="$1"
  local file_count=0
  
  log "Checking toolchain version in go.mod files..."

  while read -r modfile; do
    [[ -f "$modfile" ]] || continue
    file_count=$((file_count + 1))
    
    local mod_version
    mod_version="$(get_toolchain_version "$modfile")"
    
    if [[ -z "$mod_version" ]]; then
      continue  # Toolchain directive is optional
    fi
    
    if [[ "$mod_version" != "$expected_version" ]]; then
      error "Inconsistent toolchain version in $modfile: expected $expected_version, found $mod_version"
      issues_found=true
      
      # Collect fix information
      if [[ "$FIX_MODE" == true ]]; then
        fixes+=("toolchain|$modfile|$expected_version|$mod_version")
      fi
    fi
  done < <(git ls-files "${GOMODFILES[@]}" 2>/dev/null || true)
  
  log "- Checked $file_count go.mod files"
}

# Verify golang container versions in Dockerfiles
# Args: expected_version
function verify_docker_golang_version() {
  local expected_version="$1"
  local file_count=0
  local version_count=0
  
  log "Checking golang container versions in Dockerfiles..."

  while read -r dockerfile; do
    [[ -f "$dockerfile" ]] || continue
    file_count=$((file_count + 1))
    
    # Improved regex to handle various golang image formats
    # FROM golang:1.21, FROM golang:1.21-alpine, FROM golang:1.21.1, etc.
    while IFS= read -r version; do
      if [[ -z "$version" ]]; then
        continue
      fi
      
      version_count=$((version_count + 1))
      if [[ "$version" != "$expected_version" ]]; then
        error "Inconsistent golang container version in $dockerfile: expected $expected_version, found $version"
        issues_found=true
        
        # Collect fix information if in fix mode
        if [[ "$FIX_MODE" == true ]]; then
          fixes+=("dockerfile_golang|$dockerfile|$expected_version|$version")
        fi
      fi
    done < <(grep -E "^FROM[[:space:]]+.*golang:" "$dockerfile" 2>/dev/null | \
             sed -E 's/.*golang:([0-9]+\.[0-9]+(\.[0-9]+)?).*/\1/' | \
             grep -E '^[0-9]+\.[0-9]+(\.[0-9]+)?$' 2>/dev/null || true)
  done < <(git ls-files "${DOCKERFILES[@]}" 2>/dev/null || true)
  
  log "- Checked $file_count Dockerfiles ($version_count golang references found)"
}

# Verify GO_VERSION/GOLANG_VERSION arguments in Dockerfiles
# Args: expected_version
function verify_docker_goarg_version() {
  local expected_version="$1"
  local file_count=0
  local version_count=0
  
  log "Checking GO_VERSION/GOLANG_VERSION args in Dockerfiles..."

  while read -r dockerfile; do
    [[ -f "$dockerfile" ]] || continue
    file_count=$((file_count + 1))
    
    # Improved regex to handle various formats:
    # ARG GO_VERSION=1.21, ENV GOLANG_VERSION=1.21.1, etc.
    while IFS= read -r version; do
      if [[ -z "$version" ]]; then
        continue
      fi
      
      version_count=$((version_count + 1))
      if [[ "$version" != "$expected_version" ]]; then
        error "Inconsistent go arg version in $dockerfile: expected $expected_version, found $version"
        issues_found=true
        
        # Collect fix information if in fix mode
        if [[ "$FIX_MODE" == true ]]; then
          fixes+=("dockerfile_goarg|$dockerfile|$expected_version|$version")
        fi
      fi
    done < <(grep -E "^(ARG|ENV)[[:space:]]+GO(LANG)?_VERSION=" "$dockerfile" 2>/dev/null | \
             sed -E 's/.*(GO(LANG)?_VERSION=)([0-9]+\.[0-9]+(\.[0-9]+)?).*/\3/' | \
             grep -E '^[0-9]+\.[0-9]+(\.[0-9]+)?$' 2>/dev/null || true)
  done < <(git ls-files "${DOCKERFILES[@]}" 2>/dev/null || true)
  
  log "- Checked $file_count Dockerfiles ($version_count GO_VERSION references found)"
}

# Apply a single fix
# Args: fix_type file_path expected_version current_version
function apply_fix() {
  local fix_type="$1"
  local file="$2"
  local expected="$3"
  local current="$4"
  
  local action_msg="Updating $fix_type in $file: $current → $expected"
  
  if [[ "$DRY_RUN" == true ]]; then
    log "- [DRY RUN] $action_msg"
    return 0
  fi
  
  log "- $action_msg"
  
  case "$fix_type" in
    "go_version")
      sed -i "s/^go $current$/go $expected/" "$file"
      ;;
    "toolchain")
      sed -i "s/^toolchain $current$/toolchain $expected/" "$file"
      ;;
    "dockerfile_golang")
      sed -i -E "s/(FROM[[:space:]]+[^[:space:]]*golang:)$current([^[:space:]]*)/\\1$expected\\2/g" "$file"
      ;;
    "dockerfile_goarg")
      sed -i -E "s/(^(ARG|ENV)[[:space:]]+GO(LANG)?_VERSION=)$current/\\1$expected/g" "$file"
      ;;
    *)
      error "Unknown fix type: $fix_type"
      return 1
      ;;
  esac
  
  if [[ $? -eq 0 ]]; then
    return 0
  else
    error "    ❌ Failed to update"
    return 1
  fi
}

# Apply all collected fixes
function process_fixes() {
  local total_fixes=${#fixes[@]}
  
  if [[ $total_fixes -eq 0 ]]; then
    log "✅ No fixes needed!"
    return 0
  fi
  
  if [[ "$DRY_RUN" == true ]]; then
    log "🔍 Dry run - showing what would be fixed:"
  else
    log "🔧 Applying fixes..."
  fi
  
  local success_count=0
  local failure_count=0
  
  for fix in "${fixes[@]}"; do
    IFS='|' read -r fix_type file expected current <<< "$fix"
    if apply_fix "$fix_type" "$file" "$expected" "$current"; then
      success_count=$((success_count + 1))
    else
      failure_count=$((failure_count + 1))
    fi
  done

  if [[ "$FIX_MODE" == true ]]; then
    if [[ "$DRY_RUN" == true ]]; then
      log "📊 Would apply $success_count fixes"
    else
      log "✅ Applied $success_count fixes successfully"
    fi
  fi
  
  if [[ $failure_count -gt 0 ]]; then
    error "❌ $failure_count fixes failed"
    return 1
  fi
  
  return 0
}

# Display usage information
function usage() {
  cat << EOF
Usage: $0 [OPTIONS]

Check Go version consistency across all project files using go.work as source of truth.

Checks:
  - Go version directive in go.mod files
  - Toolchain directive in go.mod files (if present)
  - Golang container versions in Dockerfiles (FROM golang:x.x)
  - GO_VERSION/GOLANG_VERSION arguments in Dockerfiles

OPTIONS:
  --silent     Suppress non-error output
  --dry-run    Show what would be fixed without making changes
  --fix        Automatically fix version inconsistencies
  --help       Show this help message

EXAMPLES:
  $0                # Run consistency check with normal output
  $0 --silent       # Run check with minimal output (for CI/CD)
  $0 --dry-run      # Show what fixes would be applied
  $0 --fix          # Apply fixes automatically
  $0 --fix --silent # Apply fixes silently

EXIT CODES:
  0 - All versions are consistent
  1 - Version inconsistencies found
  2 - Script error (missing go.work, git repository, etc.)

The script uses go.work as the authoritative source for expected Go versions.
If go.work doesn't exist or lacks version directives, those checks are skipped.
EOF
}

# Parse command line arguments
function parse_args() {
  while [[ $# -gt 0 ]]; do
    case $1 in
      --silent)
        SILENT=true
        shift
        ;;
      --dry-run)
        DRY_RUN=true
        FIX_MODE=true  # Enable fix mode for dry run
        shift
        ;;
      --fix)
        FIX_MODE=true
        shift
        ;;
      --help|-h)
        usage
        exit 0
        ;;
      *)
        error "Unknown option: $1"
        usage >&2
        exit 2
        ;;
    esac
  done
}

# Main function
function main() {
  parse_args "$@"
  
  # Ensure we're in a git repository
  if ! git rev-parse --git-dir >/dev/null 2>&1; then
    error "Not in a git repository"
    exit 2
  fi
  
  local go_work_file="$PROJECT_ROOT/go.work"
  
  # Check if go.work exists
  if [[ ! -f "$go_work_file" ]]; then
    error "go.work file not found at: $go_work_file"
    error "This script requires go.work as the source of truth for Go versions"
    exit 2
  fi

  log "Checking Go version consistency across project files..."
  log "Project root: $PROJECT_ROOT"
  log "Using go.work as source of truth: $go_work_file"

  # Get expected versions from go.work
  local expected_go_version
  local expected_toolchain_version
  
  expected_go_version="$(get_go_version "$go_work_file")"
  expected_toolchain_version="$(get_toolchain_version "$go_work_file")"

  # Check toolchain version if specified
  if [[ -z "$expected_toolchain_version" ]]; then
    log "ℹ️  No toolchain version specified in go.work -- skipping checks..."
  else
    log "Expected toolchain version: $expected_toolchain_version"
    verify_toolchain_version "$expected_toolchain_version"
  fi
  
  # Check Go version (required)
  if [[ -z "$expected_go_version" ]]; then
    log "ℹ️  No go version specified in go.work -- skipping checks..."
  else
    # Dockerfiles pin only the major.minor (e.g. 1.25) so we float onto
    # the latest patch from the base image; go.mod/go.work use the full
    # canonical form because the Go toolchain rewrites it that way.
    local expected_go_minor="${expected_go_version%.*}"

    log "Expected Go version: $expected_go_version (Dockerfiles: $expected_go_minor)"

    verify_go_version "$expected_go_version"
    verify_docker_golang_version "$expected_go_minor"
    verify_docker_goarg_version "$expected_go_minor"
  fi

  # Handle fix/dry-run mode
  if [[ "$FIX_MODE" == true ]]; then
    if [[ "$issues_found" == true ]]; then
      if process_fixes; then
        if [[ "$DRY_RUN" == true ]]; then
          exit 0
        fi
        
        log "Re-checking after fixes..."
        
        # Reset and re-run checks
        issues_found=false
        fixes=()
        
        if [[ -n "$expected_go_version" ]]; then
          local expected_go_minor="${expected_go_version%.*}"
          verify_go_version "$expected_go_version"
          verify_docker_golang_version "$expected_go_minor"
          verify_docker_goarg_version "$expected_go_minor"
        fi
        if [[ -n "$expected_toolchain_version" ]]; then
          verify_toolchain_version "$expected_toolchain_version"
        fi
        
        if [[ "$issues_found" == true ]]; then
          error "Some issues remain after applying fixes"
          exit 1
        fi
      else
        error "Fix operation failed"
        exit 1
      fi
    fi
  fi

  # Final result for check mode
  if [[ "$issues_found" == true ]]; then
    error "Go version consistency check failed!"
    log "💡 Run '$(basename "$0") --fix' to automatically fix the issues"
    exit 1
  fi

  log "✅ Go version consistency check passed!"
}

# Only run main if script is executed directly (not sourced)
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  main "$@"
fi
