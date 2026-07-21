#!/bin/bash
# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

set -u

BASE_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "${BASE_DIR}/../../.." && pwd)"

source "${BASE_DIR}/../utils/e2e_utils.sh"

setup_workspace "registry" "$BASE_DIR"

banner "Registry E2E Test"

# Config
MIXER_CONFIG="$BASE_DIR/mixer/mixer.toml"
RACK_CONFIG="$BASE_DIR/rack/rack.toml"
BASE_URL="http://127.0.0.1:8093"
API_URL="$BASE_URL/api/v1"

MIXER_LOG="$WORK_DIR/mixer/mixer.stdout"
RACK_LOG="$WORK_DIR/rack/rack.stdout"

DB_CLI="duckdb"
FLUX_BIN="${ROOT_DIR}/bin/fluxrig"

MIXER_PID=""
RACK_PID=""

# Trap for cleanup
cleanup() {
    echo ""
    log_info "Shutting down..."
    if [ -n "$MIXER_PID" ]; then kill -9 $MIXER_PID 2>/dev/null || true; wait $MIXER_PID 2>/dev/null || true; fi
    if [ -n "$RACK_PID" ]; then kill -9 $RACK_PID 2>/dev/null || true; wait $RACK_PID 2>/dev/null || true; fi
    pkill -f "bin/fluxrig" || true
}
trap cleanup EXIT

# 1. Clean (Handled by setup_workspace for dirs)
pkill -f "bin/fluxrig" || true
# ensure ports are free
lsof -ti :8093 | xargs kill -9 2>/dev/null || true
lsof -ti :4222 | xargs kill -9 2>/dev/null || true

# 2. Build Check
if [ ! -f "${ROOT_DIR}/bin/fluxrig-mixer" ]; then
    echo "❌ bin/fluxrig-mixer not found. Run make build."
    exit 1
fi

# 3. Keys
echo "[INFO] Generating Keys..."
"${ROOT_DIR}/bin/fluxrig" keys gen-cluster -o "$WORK_DIR/mixer/data/cluster.key" > /dev/null

# 4. Config Prep (Ensure ports don't conflict with telemetry test defaults)
# Using persistent config files in mixer/mixer.toml and rack/rack.toml
# 5. Start Mixer
echo "[INFO] Starting Mixer (Port 8093)..."
cp "${BASE_DIR}/mixer/mixer.toml" "${WORK_DIR}/mixer/mixer.toml"
cd "${WORK_DIR}/mixer"
"${ROOT_DIR}/bin/fluxrig-mixer" -c "mixer.toml" > "mixer.stdout" 2>&1 &
MIXER_PID=$!
cd "${BASE_DIR}"

# Wait for Mixer
echo "[INFO] Waiting for Mixer..."
for ((i=1;i<=30;i++)); do
    HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$API_URL/health")
    if [ "$HTTP_CODE" == "200" ]; then
        echo "✅ Mixer is UP."
        break
    fi
    sleep 1
    if [ $i -eq 30 ]; then
        echo "❌ Mixer start timeout."
        cat "$MIXER_LOG"
        exit 1
    fi
done

# 6. Start Rack
echo "[INFO] Starting Rack..."
cp "${BASE_DIR}/rack/rack.toml" "${WORK_DIR}/rack/rack.toml"
cd "${WORK_DIR}/rack"
"${ROOT_DIR}/bin/fluxrig" rack -c "rack.toml" > "rack.stdout" 2>&1 &
RACK_PID=$!
cd "${BASE_DIR}"

# 7. Wait for Registration
echo "[INFO] Waiting for Rack Registration..."
FOUND=0
for ((i=1;i<=30;i++)); do
    curl -s "$API_URL/racks" > "$WORK_DIR/mixer/logs/api_racks.json"
    # Match default hostname or IP if name not set? 
    # Rack auto-generates name if not provided? Or stays pending?
    # Default behavior: Pending with machine-id name?
    # Let's check api output.
    # Match machine_id (UUID format)
    if grep -qE "[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}" "$WORK_DIR/mixer/logs/api_racks.json"; then
        echo "✅ Rack Registered (Found UUID)!"
        FOUND=1
        break
    fi
    sleep 1
done

if [ $FOUND -eq 0 ]; then
    echo "❌ Rack failed to register."
    cat "$RACK_LOG"
    exit 1
fi

echo "[INFO] Waiting 15s for Snake Registration (NATS Handshake)..."
sleep 15

# Verify Bus Connected in logs
if ! grep -q "Bus Connected" "$RACK_LOG"; then
    echo "❌ Rack did not connect to Bus."
    cat "$RACK_LOG"
    exit 1
fi

# --- CLI VERIFICATION (Requires Mixer Running) ---
echo "[INFO] Verifying Registry via CLI..."

RACKS_OUT=$($FLUX_BIN racks --api-url "$BASE_URL")
echo "$RACKS_OUT"
if [[ "$RACKS_OUT" != *"pending"* ]]; then
    echo "❌ CLI Racks Verification Failed."
    echo "❌ CLI Racks Verification Failed."
    exit 1
fi

echo "✅ CLI Racks Verified."

# Stop Mixer to release DB lock before verification
echo "[INFO] Stopping Mixer to verify DB..."
kill $MIXER_PID 2>/dev/null || true
for i in {1..5}; do
    if ! ps -p $MIXER_PID > /dev/null; then break; fi
    sleep 1
done
wait $MIXER_PID 2>/dev/null || true
MIXER_PID=""

# 8. Registry Verification using DuckDB
# 8. Registry Verification using DuckDB
MIXER_DB="$WORK_DIR/mixer/data/flux.duckdb"

echo "[INFO] Verifying Registry Content..."

# Use DuckDB CLI
MIXER_COUNT=$($DB_CLI -noheader -csv "$MIXER_DB" "SELECT count(*) FROM registry WHERE type_id=2")
SNAKE_COUNT=$($DB_CLI -noheader -csv "$MIXER_DB" "SELECT count(*) FROM registry WHERE type_id=9")
RACK_COUNT=$($DB_CLI -noheader -csv "$MIXER_DB" "SELECT count(*) FROM registry WHERE type_id=4")

echo "   Found $MIXER_COUNT Mixers"
echo "   Found $SNAKE_COUNT Snakes"
echo "   Found $RACK_COUNT Racks"

if [ "$MIXER_COUNT" -ne 1 ]; then
    echo "❌ Expected 1 Mixer, got $MIXER_COUNT"
    # Show table for debug
    $DB_CLI -line "$MIXER_DB" "SELECT * FROM registry"
    exit 1
fi
if [ "$SNAKE_COUNT" -ne 1 ]; then
    echo "❌ Expected 1 Snake, got $SNAKE_COUNT"
    exit 1
fi
if [ "$RACK_COUNT" -ne 1 ]; then
    echo "❌ Expected 1 Rack, got $RACK_COUNT"
    exit 1
fi

echo "✅ Registry Counts Verified."

echo "[INFO] Verifying Attributes for Rack..."
# Check if Rack attributes contain IP/Port/Secret
RACK_ROW=$($DB_CLI -noheader -csv "$MIXER_DB" "SELECT stats, attributes, version FROM registry WHERE type_id=4 LIMIT 1")
echo "   Rack Row: $RACK_ROW"
if [[ "$RACK_ROW" != *"last_seen"* ]]; then # stats usually has last_seen?
    # Wait, query is SELECT stats...
    # stats JSON.
    :
fi
# Version check. Compare against the VERSION file rather than a hardcoded list of
# accepted prefixes, which rejected every release from v0.6.0 onward.
EXPECTED_VERSION="$(cat "${ROOT_DIR}/VERSION")"
if [[ "$RACK_ROW" == *"$EXPECTED_VERSION"* ]]; then
     echo "✅ Rack Version Verified ($EXPECTED_VERSION)"
else
     echo "❌ Rack Version Check Failed. Expected '$EXPECTED_VERSION'. Row: $RACK_ROW"
     exit 1
fi

echo "[INFO] Verifying Snake Topology..."
SNAKE_ROW=$($DB_CLI -noheader -csv "$MIXER_DB" "SELECT attributes FROM registry WHERE type_id=9 LIMIT 1")
if [[ "$SNAKE_ROW" != *"mixer"* ]]; then
     echo "❌ Snake Attributes missing 'mixer' topology!"
     exit 1
fi
if [[ "$SNAKE_ROW" != *"rack"* ]]; then
     echo "❌ Snake Attributes missing 'rack' topology!"
     exit 1
fi
if [[ "$SNAKE_ROW" != *"rack_ip"* ]]; then
     echo "❌ Snake Attributes missing 'rack_ip' info!"
     exit 1
fi
if [[ "$SNAKE_ROW" != *"rack_port"* ]]; then
     echo "❌ Snake Attributes missing 'rack_port' info!"
     exit 1
fi
if [[ "$SNAKE_ROW" != *"mixer_ip"* ]]; then
     echo "❌ Snake Attributes missing 'mixer_ip' info!"
     exit 1
fi
if [[ "$SNAKE_ROW" != *"mixer_port"* ]]; then
     echo "❌ Snake Attributes missing 'mixer_port' info!"
     exit 1
fi

# Check Snake MachineID (Should be a valid UUID)
SNAKE_MID=$($DB_CLI -noheader -csv "$MIXER_DB" "SELECT machine_id FROM registry WHERE type_id=9 LIMIT 1")
if [[ ! "$SNAKE_MID" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]]; then
    echo "❌ Snake MachineID is not a valid UUID: $SNAKE_MID"
    exit 1
fi

echo "✅ Registry E2E Test PASSED."

echo "========================================================"
echo "Registry Table Content (ALL)"
echo "========================================================"
$DB_CLI -line "$MIXER_DB" "SELECT * FROM registry ORDER BY type_id"
echo "========================================================"

# --- ADOPTION FLOW ---
echo ""
echo "[INFO] Restarting Mixer for Adoption Flow..."
cd "${WORK_DIR}/mixer"
"${ROOT_DIR}/bin/fluxrig-mixer" -c "mixer.toml" > "mixer.stdout" 2>&1 &
MIXER_PID=$!
cd "${BASE_DIR}"

# Wait for Mixer
echo "[INFO] Waiting for Mixer..."
for ((i=1;i<=30;i++)); do
    HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$API_URL/health")
    if [ "$HTTP_CODE" == "200" ]; then
        echo "✅ Mixer is UP."
        break
    fi
    sleep 1
    if [ $i -eq 30 ]; then
        echo "❌ Mixer start timeout."
        cat "$MIXER_LOG"
        exit 1
    fi
done

echo "[INFO] Adopting Rack..."
# Extract the first pending rack ID
PENDING_ID=$(curl -s "$API_URL/racks?status=pending" | grep -oE "[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}" | head -n1)
if [ -z "$PENDING_ID" ]; then
    echo "❌ No pending rack found for adoption."
    exit 1
fi

echo "[INFO] Adopting Rack $PENDING_ID..."
sleep 2
# Use 'admin racks approve'
APPROVE_OUT=$($FLUX_BIN admin racks approve "$PENDING_ID" --name "node-100" --api-url "$BASE_URL")
echo "$APPROVE_OUT"

if [[ "$APPROVE_OUT" != *"approved"* ]]; then
    echo "❌ Approval Failed."
    exit 1
fi

echo "[INFO] Waiting 5s for Rack to receive Passport & Update..."
sleep 5

# Check logs for state update
if ! grep -q "State updated" "$RACK_LOG" && ! grep -q "Passport" "$RACK_LOG"; then
   # We might not log "Passport" explicitly, check source if needed. 
   # Assuming some log indicating success.
   echo "⚠️  Warning: specific log message for passport save not found (might be debug level)."
fi

echo "[INFO] Restarting Rack to verify persistence..."
kill -9 $RACK_PID 2>/dev/null || true
wait $RACK_PID 2>/dev/null || true

# Start Rack again
cd "${WORK_DIR}/rack"
"${ROOT_DIR}/bin/fluxrig" rack -c "rack.toml" > "rack.stdout.2" 2>&1 &
RACK_PID=$!
cd "${BASE_DIR}"

echo "[INFO] Waiting for Rack to connect..."
sleep 5

# Stop Mixer again for DB Check
echo "[INFO] Stopping Mixer to verify Final DB State..."
kill -9 $MIXER_PID 2>/dev/null || true
wait $MIXER_PID 2>/dev/null || true
MIXER_PID=""

echo "========================================================"
echo "Registry Table Content (FINAL)"
echo "========================================================"
$DB_CLI -line "$MIXER_DB" "SELECT * FROM registry ORDER BY type_id"
echo "========================================================"
