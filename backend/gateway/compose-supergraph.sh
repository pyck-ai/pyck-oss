#!/usr/bin/env bash
set -euo pipefail

# Default configuration
SUPERGRAPH_OUTPUT="supergraph.graphql"
VERBOSE=false
MAX_DEPTH=""

# Parse arguments
DIRECTORIES=()
OUTPUT_FILE=""

parse_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --output=*)
        OUTPUT_FILE="${1#*=}"
        shift
        ;;
      --max-depth=*)
        MAX_DEPTH="${1#*=}"
        shift
        ;;
      --verbose|-v)
        VERBOSE=true
        shift
        ;;
      --help|-h)
        show_help
        exit 0
        ;;
      -*)
        echo "Unknown option: $1" >&2
        exit 1
        ;;
      *)
        DIRECTORIES+=("$1")
        shift
        ;;
    esac
  done
}

show_help() {
  cat <<EOF
Usage: $0 [OPTIONS] DIRECTORY...

Recursively compose GraphQL schemas from directories into a supergraph.

Options:
  --output=FILE           Output supergraph file (default: supergraph.graphql)
  --max-depth=N          Maximum recursion depth for finding files (optional)
  --verbose, -v          Show detailed progress
  --help, -h             Show this help message

Examples:
  # Recursively process all GraphQL files in service directories
  $0 ../main-data ../management ../inventory
  
  # Process with limited depth
  $0 --max-depth=2 ../services
  
  # Custom output location
  $0 --output=prod.graphql ../*/

The script recursively searches for all *.graphql files in each specified
directory and its subdirectories, concatenates them, and applies service-specific
transformations before generating the final supergraph.
EOF
}

log() {
  if [[ "$VERBOSE" == true ]]; then
    echo "[$(date +'%H:%M:%S')] $*" >&2
  fi
}

# Extract service name from directory path
get_service_name() {
  local dir="$1"
  # Remove trailing slashes
  dir="${dir%/}"
  
  # If directory is named "graph", use parent directory name
  if [[ "$(basename "$dir")" == "graph" ]]; then
    local parent_dir=$(dirname "$dir")
    local service_name=$(basename "$parent_dir")
  else
    # Use directory name directly
    local service_name=$(basename "$dir")
  fi
  
  # Clean up the name (remove hyphens, convert to lowercase)
  echo "$service_name" | tr '[:upper:]' '[:lower:]' | sed 's/-//g'
}

# Determine node suffix for service
get_node_suffix() {
  local service="$1"
  
  case "$service" in
    maindata) echo "" ;;  # No suffix for maindata
    management) echo "DataType" ;;
    inventory) echo "Inventory" ;;
    picking) echo "Picking" ;;
    receiving) echo "Receiving" ;;
    file) echo "File" ;;
    workflow) echo "Workflow" ;;
    factory) echo "Factory" ;;
    *) 
      # For unknown services, capitalize first letter
      echo "$(echo "$service" | sed 's/^\(.\)/\U\1/')"
      ;;
  esac
}

# Get port for service
get_service_port() {
  local service="$1"
  
  case "$service" in
    maindata) echo "8081" ;;
    management) echo "8082" ;;
    inventory) echo "8084" ;;
    picking) echo "8086" ;;
    receiving) echo "8087" ;;
    file) echo "8088" ;;
    workflow) echo "8089" ;;
    factory) echo "8090" ;;
    *) 
      # Generate deterministic port based on service name
      local hash=0
      for (( i=0; i<${#service}; i++ )); do
        hash=$(( hash + $(printf '%d' "'${service:$i:1}") ))
      done
      echo "$((8090 + (hash % 100)))"
      ;;
  esac
}


# Process a directory recursively
process_directory() {
  local dir="$1"
  local service_name=$(get_service_name "$dir")
  local output_file="${service_name}.graphql"
  
  log "Processing $dir → $service_name (recursive)"
  
  # Find all GraphQL files once and cache results
  local graphql_files
  if [[ -n "$MAX_DEPTH" ]]; then
    graphql_files=$(find "$dir" -maxdepth "$MAX_DEPTH" -name "*.graphql" -type f 2>/dev/null)
  else
    graphql_files=$(find "$dir" -name "*.graphql" -type f 2>/dev/null)
  fi
  
  local file_count=$(echo "$graphql_files" | grep -c . 2>/dev/null || echo 0)
  
  if [[ $file_count -eq 0 ]]; then
    echo "Warning: No GraphQL files found in $dir (searched recursively)" >&2
    return 1
  fi
  
  log "  Found $file_count GraphQL files in $dir"
  
  # Priority files (these get added first if they exist)
  local priority_names="ent.graphql schema.graphql types.graphql"
  local temp_file="${output_file}.tmp"
  > "$temp_file"
  
  # Create array to track processed files
  declare -A processed_files
  
  # Add priority files first
  for priority in $priority_names; do
    echo "$graphql_files" | grep "/$priority$" | sort | while read -r file; do
      if [[ -n "$file" ]]; then
        local rel_path=${file#$dir/}
        log "  Adding (priority): $rel_path"
        cat "$file" >> "$temp_file"
        echo >> "$temp_file"
        echo "$file" >> "${temp_file}.processed"
      fi
    done
  done
  
  # Add all other files
  echo "$graphql_files" | sort | while read -r file; do
    if [[ -n "$file" ]]; then
      # Skip if it's a priority file
      local is_priority=false
      for priority in $priority_names; do
        if [[ "$file" == *"/$priority" ]]; then
          is_priority=true
          break
        fi
      done
      
      # Skip if already processed
      if [[ "$is_priority" == false ]] && ! grep -q "^$file$" "${temp_file}.processed" 2>/dev/null; then
        local rel_path=${file#$dir/}
        log "  Adding: $rel_path"
        cat "$file" >> "$temp_file"
        echo >> "$temp_file"
        echo "$file" >> "${temp_file}.processed"
      fi
    fi
  done
  
  # Clean up processed files tracker
  rm -f "${temp_file}.processed"
  
  # Apply node transformations if needed
  local node_suffix=$(get_node_suffix "$service_name")
  
  if [[ -n "$node_suffix" ]]; then
    log "  Applying node transformation: Node → Node${node_suffix}"
    # sed instead of awk: the previous awk regex relied on GNU gawk word
    # boundaries (\< \B) and silently no-ops on mawk, corrupting the output
    sed -e "s/node(/node${node_suffix}(/g" \
        -e "s/nodes(/nodes${node_suffix}(/g" \
        -e "s/Node/Node${node_suffix}/g" \
        "$temp_file" > "$output_file"
    rm "$temp_file"
  else
    log "  No node transformation needed"
    mv "$temp_file" "$output_file"
  fi
  
  # Return service info for supergraph config
  echo "${service_name}|${output_file}|$(get_service_port "$service_name")"
}

# Generate supergraph.yaml
generate_supergraph_config() {
  local config_file="supergraph.yaml"
  local services=("$@")
  
  log "Generating supergraph configuration"
  
  cat > "$config_file" <<EOF
federation_version: "2"
subgraphs:
EOF
  
  for service_info in "${services[@]}"; do
    IFS='|' read -r name path port <<< "$service_info"
    
    cat >> "$config_file" <<EOF
  $name:
    routing_url: http://localhost:$port/query
    schema:
      file: ./$(basename "$path")
EOF
    
    log "  Added subgraph: $name (port $port)"
  done
  
  echo "$config_file"
}

# Process input directory - auto-detect services
process_input() {
  local input_dir="$1"
  local services_found=()
  
  # Check if directory exists
  if [[ ! -d "$input_dir" ]]; then
    echo "Error: Directory not found: $input_dir" >&2
    return 1
  fi
  
  # Case 1: Directory is named "graph" - it's a service directory
  if [[ "$(basename "$input_dir")" == "graph" ]]; then
    log "Detected service directory: $input_dir"
    if service_info=$(process_directory "$input_dir"); then
      echo "$service_info"
      return 0
    fi
    return 1
  fi
  
  # Case 2: Look for subdirectories with "graph" folders at depth 2 (e.g., backend/service/graph)
  # This excludes deeper nested graph directories like backend/service/api/graph
  local found_services=false
  while IFS= read -r -d '' graph_dir; do
    log "Found service: $graph_dir"
    if service_info=$(process_directory "$graph_dir"); then
      services_found+=("$service_info")
      found_services=true
    fi
  done < <(find "$input_dir" -mindepth 2 -maxdepth 2 -type d -name "graph" -print0 2>/dev/null | sort -z)
  
  # Case 3: If no graph subdirectories, check if directory itself has .graphql files
  if [[ "$found_services" == false ]]; then
    if find "$input_dir" -maxdepth 1 -name "*.graphql" -type f | grep -q .; then
      log "Detected GraphQL files in: $input_dir"
      if service_info=$(process_directory "$input_dir"); then
        services_found+=("$service_info")
      fi
    fi
  fi
  
  # Return all found services
  for service in "${services_found[@]}"; do
    echo "$service"
  done
  
  [[ ${#services_found[@]} -gt 0 ]]
}

# Main execution
main() {
  parse_args "$@"
  
  # Validate inputs
  if [[ ${#DIRECTORIES[@]} -eq 0 ]]; then
    echo "Error: No directories specified" >&2
    echo "Try '$0 --help' for usage information" >&2
    exit 1
  fi
  
  echo "=== GraphQL Schema Composition (Recursive) ===" >&2
  echo "Auto-detecting services from ${#DIRECTORIES[@]} input(s)..." >&2
  [[ -n "$MAX_DEPTH" ]] && echo "Max recursion depth: $MAX_DEPTH" >&2
  echo "" >&2
  
  
  # Process all directories with auto-detection
  SERVICES=()
  FAILED=0
  
  for dir in "${DIRECTORIES[@]}"; do
    log "Analyzing: $dir"
    
    # Process input and collect all detected services
    while IFS= read -r service_info; do
      if [[ -n "$service_info" ]]; then
        SERVICES+=("$service_info")
        # Extract service name from service_info for display
        IFS='|' read -r name _ _ <<< "$service_info"
        echo "✓ Detected service: $name" >&2
      fi
    done < <(process_input "$dir")
    
    # Check if process_input failed completely
    if [[ $? -ne 0 ]] && [[ ${#SERVICES[@]} -eq 0 ]]; then
      ((FAILED++))
    fi
  done
  
  if [[ ${#SERVICES[@]} -eq 0 ]]; then
    echo "Error: No services were successfully processed" >&2
    exit 1
  fi
  
  # Generate supergraph.yaml
  echo "" >&2
  echo "Generating supergraph configuration..." >&2
  supergraph_config=$(generate_supergraph_config "${SERVICES[@]}")
  echo "✓ Created: $supergraph_config" >&2
  
  # Run rover to compose the supergraph
  output="${OUTPUT_FILE:-$SUPERGRAPH_OUTPUT}"
  echo "" >&2
  echo "Running rover supergraph compose..." >&2
  
  if command -v rover >/dev/null 2>&1; then
    # Run rover and show its output (warnings/hints) to stderr
    echo "Running rover (warnings/hints will be shown below):" >&2
    if rover supergraph compose \
      --elv2-license accept \
      --config "$supergraph_config" \
      > "$output"; then
      echo "✓ Supergraph generated: $output" >&2
      echo "  Size: $(wc -c < "$output") bytes" >&2
    else
      echo "Error: Rover composition failed" >&2
      echo "Check the generated schemas in $(pwd)/" >&2
      exit 1
    fi
  else
    echo "Error: rover CLI not found" >&2
    echo "Install from: https://www.apollographql.com/docs/rover/getting-started" >&2
    echo "" >&2
    echo "Would run:" >&2
    echo "  rover supergraph compose --elv2-license accept --config $supergraph_config > $output" >&2
    exit 1
  fi
  
  # Summary
  echo "" >&2
  echo "=== Summary ===" >&2
  echo "Services composed: ${#SERVICES[@]}" >&2
  [[ $FAILED -gt 0 ]] && echo "Failed: $FAILED" >&2
  echo "Output: $output" >&2
  echo "Schemas: $(pwd)/" >&2
}

# Cleanup on exit
cleanup() {
  rm -f *.tmp 2>/dev/null || true
}
trap cleanup EXIT

main "$@"