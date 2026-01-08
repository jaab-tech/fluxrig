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

set -u

BASE_DIR="$(cd "$(dirname "$0")" && pwd)"

# Colors
GREEN='\033[0;32m'
RED='\033[0;31m'
CYAN='\033[0;36m'
NC='\033[0m'

log_info() { echo -e "${CYAN}[INFO]${NC} $1"; }
log_success() { echo -e "${GREEN}[PASS] ✅${NC} $1"; }
log_error() { echo -e "${RED}[FAIL] ❌${NC} $1"; }

FAILED_TESTS=()
TEST_DIRS=()

echo -e "${CYAN}==============================================================================${NC}"
echo -e "${CYAN}FluxRig Unified Regression Suite${NC}"
echo -e "${CYAN}==============================================================================${NC}"

# 1. Discover Tests
for dir in "${BASE_DIR}"/*/; do
    dir=${dir%*/}
    dirname=$(basename "$dir")
    
    # Skip utils and non-test dirs
    if [ "$dirname" == "utils" ]; then continue; fi
    
    if [ -f "$dir/run.sh" ]; then
        TEST_DIRS+=("$dir")
    fi
done

TOTAL_TESTS=${#TEST_DIRS[@]}
CURRENT_TEST=0

log_info "Found $TOTAL_TESTS test suites."

# 2. Execute Tests
for dir in "${TEST_DIRS[@]}"; do
    ((CURRENT_TEST++))
    dirname=$(basename "$dir")
    
    echo ""
    echo -e "${CYAN}>>> [${CURRENT_TEST}/${TOTAL_TESTS}] Running Test: ${dirname}${NC}"
    
    RUN_SCRIPT="$dir/run.sh"
    
    # Execute the test script
    if bash "$RUN_SCRIPT"; then
        log_success "Test '${dirname}' PASSED"
    else
        log_error "Test '${dirname}' FAILED"
        FAILED_TESTS+=("$dirname")
    fi
done

echo ""
echo -e "${CYAN}==============================================================================${NC}"
if [ ${#FAILED_TESTS[@]} -eq 0 ]; then
    log_success "ALL TESTS PASSED ($CURRENT_TEST suites executed)"
    exit 0
else
    log_error "REGRESSION FAILED. The following suites failed:"
    for t in "${FAILED_TESTS[@]}"; do
        echo -e "  - ${t}"
    done
    exit 1
fi
