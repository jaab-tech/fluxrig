#!/bin/bash

# Copyright 2025 JAAB Tech SAS, Uruguay
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.


# ==============================================================================
# Shared E2E Utilities
# ==============================================================================

# Colors
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[0;33m'
CYAN='\033[0;36m'
NC='\033[0m'

# Logging Helpers
log_info() {
    echo -e "${CYAN}[INFO]${NC} $1"
}

log_success() {
    echo -e "${GREEN}[PASS] ✅${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[FAIL] ❌${NC} $1" >&2
}

fail() {
    log_error "$1"
    exit 1
}

# Banner (No emojis)
banner() {
    echo -e "${CYAN}==============================================================================${NC}"
    echo -e "${CYAN}$1${NC}"
    echo -e "${CYAN}==============================================================================${NC}"
}

# Section Header
section() {
    echo -e "${CYAN}--- $1 ---${NC}"
}

# Generic Cleanup Hook
cleanup() {
    EXIT_CODE=$?
    if [ $EXIT_CODE -ne 0 ]; then
        echo -e "${RED}[FAIL] ❌ Test Suite Failed (Exit Code: $EXIT_CODE)${NC}"
    fi
    log_info "Shutting down child processes..."
    # Kill only processes started by this shell (job control)
    jobs -p | xargs kill -9 2>/dev/null || true
}
trap cleanup EXIT

# Wait for a port to be listening
# Usage: wait_for_port $PORT [$MAX_RETRIES]
wait_for_port() {
    local port="$1"
    local max_retries="${2:-30}"
    
    log_info "Waiting for port $port..."
    for ((i=1;i<=max_retries;i++)); do
        if lsof -Pi :$port -sTCP:LISTEN -t >/dev/null ; then
            log_success "Port $port is UP."
            return 0
        fi
        sleep 1
    done
    return 1
}

# Setup temporal workspace with symlink
# Usage: setup_workspace $TEST_NAME $BASE_DIR
# Sets: WORK_DIR
setup_workspace() {
    local test_name="$1"
    local base_dir="$2"
    
    # Use formatted date for better readability/sorting
    # Format: work_testname_YYYYMMDD_HHMMSS
    TEST_ID="${test_name}_$(date +%Y%m%d_%H%M%S)"
    
    # Use temporal directory for workspace
    local tmp_root="/tmp/fluxrig"
    if [ ! -d "$tmp_root" ]; then
        mkdir -p "$tmp_root"
    fi

    WORK_DIR="${tmp_root}/work_${TEST_ID}"
    
    log_info "Creating Workspace: ${WORK_DIR}"
    mkdir -p "${WORK_DIR}/mixer/logs" "${WORK_DIR}/mixer/data"
    mkdir -p "${WORK_DIR}/rack/logs" "${WORK_DIR}/rack/data"
    
    # Symlink 'work' to current workspace for easy access
    rm -f "${base_dir}/work"
    ln -s "${WORK_DIR}" "${base_dir}/work"
    log_info "Symlink updated: ${base_dir}/work -> ${WORK_DIR}"
}

# ==============================================================================
# Common Test Functions
# ==============================================================================

# Wait for Mixer Health API
# Usage: wait_for_mixer $API_URL [$MAX_RETRIES]
wait_for_mixer() {
    local api_url="$1"
    local max_retries="${2:-30}"
    
    log_info "Waiting for Mixer Health..."
    for ((i=1;i<=max_retries;i++)); do
        HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$api_url/health" 2>/dev/null)
        if [ "$HTTP_CODE" == "200" ]; then
            log_success "Mixer is UP."
            return 0
        fi
        sleep 1
    done
    fail "Mixer start timeout."
}

# Wait for Rack registration in API
# Usage: wait_for_rack $API_URL $RACK_NAME [$MAX_RETRIES]
wait_for_rack() {
    local api_url="$1"
    local rack_name="$2"
    local max_retries="${3:-30}"
    
    log_info "Waiting for Rack '$rack_name' registration..."
    for ((i=1;i<=max_retries;i++)); do
        RESPONSE=$(curl -s --max-time 2 "$api_url/racks" 2>/dev/null)
        if echo "$RESPONSE" | grep -q "$rack_name"; then
            log_success "Rack '$rack_name' registered."
            return 0
        fi
        echo -n "."
        sleep 1
    done
    echo ""
    fail "Timeout waiting for rack '$rack_name' registration."
}

# Check log contains pattern
# Usage: check_log $LOG_FILE $PATTERN $DESCRIPTION
check_log() {
    local log_file="$1"
    local pattern="$2"
    local description="$3"
    
    if grep -q "$pattern" "$log_file"; then
        log_success "$description"
        return 0
    else
        log_error "$description"
        return 1
    fi
}

# Verify file exists
# Usage: verify_file $FILE_PATH $DESCRIPTION
verify_file() {
    local file_path="$1"
    local description="$2"
    
    if [ -f "$file_path" ]; then
        log_success "$description"
        return 0
    else
        fail "$description - File not found: $file_path"
    fi
}

# Start Mixer and wait for health
# Usage: start_mixer $CONFIG_PATH $LOG_PATH $API_URL
# Sets global MIXER_PID
start_mixer() {
    local config="$1"
    local log_path="$2"
    local api_url="$3"
    
    log_info "Starting Mixer..."
    ./bin/fluxrig-mixer -c "$config" > "$log_path" 2>&1 &
    MIXER_PID=$!
    log_info "Mixer PID: $MIXER_PID"
    
    wait_for_mixer "$api_url"
}

# Start Rack
# Usage: start_rack $CONFIG_PATH $LOG_PATH
# Sets global RACK_PID
start_rack() {
    local config="$1"
    local log_path="$2"
    
    log_info "Starting Rack..."
    ./bin/fluxrig rack -c "$config" > "$log_path" 2>&1 &
    RACK_PID=$!
    log_info "Rack PID: $RACK_PID"
}

# Generate Cluster Keys
# Usage: gen_keys $OUTPUT_PATH
gen_keys() {
    local output_path="$1"
    
    log_info "Generating Cluster Keys..."
    ./bin/fluxrig keys gen-cluster -o "$output_path" > /dev/null
    verify_file "$output_path" "Cluster key generated."
}

# Clean test directories
# Usage: clean_dirs DIR1 [DIR2 ...]
clean_dirs() {
    for dir in "$@"; do
        rm -rf "$dir/data" "$dir/logs"
        mkdir -p "$dir/data" "$dir/logs"
    done
}

# Kill old processes
kill_old_processes() {
    pkill -f "bin/fluxrig" || true
    sleep 1
}

# Path Helper
ensure_root() {
    if [ ! -f "go.mod" ]; then
        SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[1]}")" && pwd)"
        if [ -f "${SCRIPT_DIR}/../../../go.mod" ]; then
             ROOT_DIR="${SCRIPT_DIR}/../../../"
        elif [ -f "${SCRIPT_DIR}/../../go.mod" ]; then
             ROOT_DIR="${SCRIPT_DIR}/../../"
        elif [ -f "${SCRIPT_DIR}/../go.mod" ]; then
             ROOT_DIR="${SCRIPT_DIR}/../"
        fi
    else
        ROOT_DIR="$(pwd)"
    fi
}
