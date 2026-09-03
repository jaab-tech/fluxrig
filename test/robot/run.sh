#!/bin/bash
# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

set -e
# Support common binary paths (Mac/Homebrew, Linux/usr/local)
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
unset MallocStackLogging
export MallocNanoZone=0

BASE_DIR="$(cd "$(dirname "$0")" && pwd)"
VENV_DIR="${BASE_DIR}/.venv"
RESULTS_DIR="${BASE_DIR}/results"

# 1. Setup Venv
if [ ! -d "$VENV_DIR" ]; then
    echo "[ROBOT] Creating venv..."
    python3 -m venv "$VENV_DIR"
    "$VENV_DIR/bin/pip" install --upgrade pip
fi

# 2. Sync Dependencies (Always ensure we have what's in requirements.txt)
echo "[ROBOT] Syncing dependencies..."
"$VENV_DIR/bin/pip" install -q -r "${BASE_DIR}/requirements.txt"

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

# A suite runs against the compiled binaries in bin/, and run.sh does not build
# them. Running it directly (rather than through `make test-robot-*`, which builds
# first) can test a missing binary, or worse a STALE one: edit a .go file, run the
# suite, and it silently exercises the old code, so a real failure reads as a pass.
# Refuse instead. The staleness rule mirrors the Makefile's bin/<x> prerequisites
# exactly (cmd/<x> + pkg, minus the generated OpenAPI docs); keep them in step, or
# a rebuild and this check will disagree and deadlock.
# Set FLUXRIG_ROBOT_SKIP_BIN_CHECK=true to run against bin/ as-is.
PROJECT_ROOT="$(cd "${BASE_DIR}/../.." && pwd)"
if [ "${FLUXRIG_ROBOT_SKIP_BIN_CHECK}" != "true" ]; then
    STALE=""
    for b in fluxrig fluxrig-mixer iso8583-tool; do
        bin="${PROJECT_ROOT}/bin/${b}"
        if [ ! -x "$bin" ]; then
            STALE="${STALE}
  bin/${b} is missing"
        elif [ -n "$(find "${PROJECT_ROOT}/cmd/${b}" "${PROJECT_ROOT}/pkg" -name '*.go' -not -path '*/mixer/api/docs/*' -newer "$bin" -print -quit 2>/dev/null)" ]; then
            STALE="${STALE}
  bin/${b} is older than a .go source"
        fi
    done
    if [ -n "$STALE" ]; then
        printf '[ROBOT] Refusing to run against stale or missing binaries:%s\n' "$STALE" >&2
        printf '[ROBOT] Build them first:  make build-bin\n' >&2
        printf '[ROBOT] Or bypass on purpose:  FLUXRIG_ROBOT_SKIP_BIN_CHECK=true %s %s\n' "$0" "$*" >&2
        exit 1
    fi
fi

# Verify VENV
echo "[ROBOT] Using Python Environment: $VENV_DIR"
"$VENV_DIR/bin/python3" --version
"$VENV_DIR/bin/python3" -c "import sys; print(f'Python Executable: {sys.executable}')"

EXTRA_ARGS=""
if [ "${FLUXRIG_ROBOT_EXIT_ON_FAILURE}" = "true" ]; then
    echo "[ROBOT] Fast-Fail enabled (--exitonfailure)"
    EXTRA_ARGS="--exitonfailure"
fi

"$VENV_DIR/bin/robot" \
    --outputdir "$RESULTS_DIR" \
    --pythonpath "${BASE_DIR}/lib" \
    --pythonpath "${BASE_DIR}/lib/keywords" \
    $EXTRA_ARGS \
    "${@:-$TARGETS}"

# Print summary
echo "------------------------------------------------------------------------------"
echo "Report: $RESULTS_DIR/report.html"
echo "Log:    $RESULTS_DIR/log.html"
echo "------------------------------------------------------------------------------"
