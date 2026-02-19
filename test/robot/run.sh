#!/bin/bash
# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

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
TSTAMP=$(date +%Y%m%d_%H%M%S)
TEST_ROOT="/tmp/fluxrig/robot_run_${TSTAMP}"
WORK_DIR="${TEST_ROOT}/work"
RESULTS_DIR="${TEST_ROOT}/results"

mkdir -p "$WORK_DIR" "$RESULTS_DIR"

# Determine Suite Directory for Symlinks
TARGET="$1"
if [ -z "$TARGET" ]; then
    TARGET="suites"
fi

if [ -f "$TARGET" ]; then
    SUITE_DIR=$(dirname "$TARGET")
elif [ -d "$TARGET" ]; then
    SUITE_DIR="$TARGET"
else
    SUITE_DIR="suites"
fi

# Ensure absolute path for linking logic (relative to BASE_DIR execution)
LINK_BASE="${BASE_DIR}/${SUITE_DIR}"
mkdir -p "$LINK_BASE"

# Update Symlinks
rm -rf "${LINK_BASE}/results" "${LINK_BASE}/work"
ln -s "$RESULTS_DIR" "${LINK_BASE}/results"
ln -s "$WORK_DIR" "${LINK_BASE}/work"

echo "Running Robot Framework..."
echo "Run Root:    $TEST_ROOT"
echo "Results Dir: $RESULTS_DIR"
echo "Work Dir:    $WORK_DIR"
echo "Symlink:     ${LINK_BASE}/results -> $RESULTS_DIR"
echo "Symlink:     ${LINK_BASE}/work -> $WORK_DIR"

TARGETS="$@"
if [ -z "$TARGETS" ]; then
    TARGETS="${BASE_DIR}/suites"
fi

# Verify VENV
echo "[ROBOT] Using Python Environment: $VENV_DIR"
"$VENV_DIR/bin/python3" --version
"$VENV_DIR/bin/python3" -c "import sys; print(f'Python Executable: {sys.executable}')"

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
