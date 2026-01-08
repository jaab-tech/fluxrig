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

set -e

BASE_DIR="$(cd "$(dirname "$0")" && pwd)"
VENV_DIR="${BASE_DIR}/.venv"
RESULTS_DIR="${BASE_DIR}/results"

# 1. Setup Venv
if [ ! -d "$VENV_DIR" ]; then
    echo "[ROBOT] Creating venv..."
    python3 -m venv "$VENV_DIR"
    "$VENV_DIR/bin/pip" install --upgrade pip
    "$VENV_DIR/bin/pip" install -r "${BASE_DIR}/requirements.txt"
fi

# Run Robot Framework
# We use a temp directory for global results to avoid polluting the source tree
# Individual suites will symlink their specific results via FluxRigLibrary
RESULTS_DIR="/tmp/fluxrig/robot_results_$(date +%Y%m%d_%H%M%S)"
mkdir -p "$RESULTS_DIR"

echo "Running Robot Framework..."
echo "Results will be in: $RESULTS_DIR"

TARGETS="$@"
if [ -z "$TARGETS" ]; then
    TARGETS="${BASE_DIR}/suites"
fi

"$VENV_DIR/bin/robot" \
    --outputdir "$RESULTS_DIR" \
    --pythonpath "${BASE_DIR}/lib" \
    --loglevel TRACE \
    $TARGETS

# Print summary
echo "------------------------------------------------------------------------------"
echo "Report: $RESULTS_DIR/report.html"
echo "Log:    $RESULTS_DIR/log.html"
echo "------------------------------------------------------------------------------"
