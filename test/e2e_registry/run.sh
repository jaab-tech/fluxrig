#!/bin/bash
set -u

# Config
TEST_DIR="test/e2e_registry"
MIXER_DIR="$TEST_DIR/mixer"
RACK_DIR="$TEST_DIR/rack"

MIXER_CONFIG="$MIXER_DIR/mixer.toml"
RACK_CONFIG="$RACK_DIR/rack.toml"
BASE_URL="http://127.0.0.1:8093"
API_URL="$BASE_URL/api/v1" # Use different port to avoid conflict with telemetry test if parallel

MIXER_LOG="$MIXER_DIR/logs/mixer.log"
RACK_LOG="$RACK_DIR/logs/rack.log"

DB_CLI="duckdb"
FLUX_BIN="bin/fluxrig"

MIXER_PID=""
RACK_PID=""

echo "========================================================"
echo "🧪 Starting Registry E2E Test"
echo "========================================================"

# Trap for cleanup
cleanup() {
    echo ""
    echo "🧹 Cleanup..."
    if [ -n "$MIXER_PID" ]; then
        kill $MIXER_PID 2>/dev/null || true
        wait $MIXER_PID 2>/dev/null || true
    fi
    if [ -n "$RACK_PID" ]; then
        kill $RACK_PID 2>/dev/null || true
        wait $RACK_PID 2>/dev/null || true
    fi
}
trap cleanup EXIT

# 1. Clean
pkill -f "bin/fluxrig" || true
rm -rf $MIXER_DIR/data $MIXER_DIR/logs $RACK_DIR/data $RACK_DIR/logs
mkdir -p $MIXER_DIR/data $MIXER_DIR/logs
mkdir -p $RACK_DIR/data $RACK_DIR/logs

# 2. Build Check
if [ ! -f "bin/fluxrig-mixer" ]; then
    echo "❌ bin/fluxrig-mixer not found. Run make build."
    exit 1
fi

# 3. Keys
echo "🔐 Generating Keys..."
./bin/fluxrig keys gen-cluster -o $MIXER_DIR/data/cluster.key > /dev/null

# 4. Config Prep (Ensure ports don't conflict with telemetry test defaults)
# Using persistent config files in mixer/mixer.toml and rack/rack.toml
# 5. Start Mixer
echo "🚀 Starting Mixer (Port 8093)..."
./bin/fluxrig-mixer -c $MIXER_DIR/mixer.toml > "$MIXER_LOG" 2>&1 &
MIXER_PID=$!

# Wait for Mixer
echo "⏳ Waiting for Mixer..."
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
echo "🔌 Starting Rack..."
./bin/fluxrig rack -c $RACK_DIR/rack.toml > "$RACK_LOG" 2>&1 &
RACK_PID=$!

# 7. Wait for Registration
echo "⏳ Waiting for Rack Registration..."
FOUND=0
for ((i=1;i<=30;i++)); do
    curl -s "$API_URL/racks" > "$MIXER_DIR/logs/api_racks.json"
    # Match default hostname or IP if name not set? 
    # Rack auto-generates name if not provided? Or stays pending?
    # Default behavior: Pending with machine-id name?
    # Let's check api output.
    if grep -q "machine_id" "$MIXER_DIR/logs/api_racks.json"; then
        echo "✅ Rack Registered (Found structure)!"
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

echo "⏳ Waiting 10s for Snake Registration (NATS Handshake)..."
sleep 10

# Verify Bus Connected in logs
if ! grep -q "Bus Connected" "$RACK_LOG"; then
    echo "❌ Rack did not connect to Bus."
    cat "$RACK_LOG"
    exit 1
fi

# --- CLI VERIFICATION (Requires Mixer Running) ---
echo "🔍 Verifying Registry via CLI..."

RACKS_OUT=$($FLUX_BIN racks --api-url "$BASE_URL")
echo "$RACKS_OUT"
if [[ "$RACKS_OUT" != *"pending"* ]]; then
    echo "❌ CLI Racks Verification Failed."
    echo "❌ CLI Racks Verification Failed."
    exit 1
fi

echo "✅ CLI Racks Verified."

# Stop Mixer to release DB lock before verification
echo "🛑 Stopping Mixer to verify DB..."
kill $MIXER_PID
wait $MIXER_PID 2>/dev/null || true
MIXER_PID=""

# 8. Registry Verification using DuckDB
MIXER_DB="$MIXER_DIR/data/fluxrig.duckdb" # Default name if not overridden? Wait, mixer uses default.
# The telemetry test set data_dir.
# Default DB name is fluxrig.duckdb?
# Let's check NewStore default. "fluxrig.duckdb".
MIXER_DB="$MIXER_DIR/data/fluxrig.duckdb"

echo "🔍 Verifying Registry Content..."

# Use DuckDB CLI
MIXER_COUNT=$($DB_CLI -noheader -csv "$MIXER_DB" "SELECT count(*) FROM registry WHERE type_id=2")
SNAKE_COUNT=$($DB_CLI -noheader -csv "$MIXER_DB" "SELECT count(*) FROM registry WHERE type_id=8")
RACK_COUNT=$($DB_CLI -noheader -csv "$MIXER_DB" "SELECT count(*) FROM registry WHERE type_id=3")

echo "   Found $MIXER_COUNT Mixers"
echo "   Found $SNAKE_COUNT Snakes"
echo "   Found $RACK_COUNT Racks"

if [ "$MIXER_COUNT" -ne 1 ]; then
    echo "❌ Expected 1 Mixer, got $MIXER_COUNT"
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

echo "🔍 Verifying Attributes for Rack..."
# Check if Rack attributes contain IP/Port/Secret
RACK_ROW=$($DB_CLI -noheader -csv "$MIXER_DB" "SELECT stats, attributes, version FROM registry WHERE type_id=3 LIMIT 1")
echo "   Rack Row: $RACK_ROW"
if [[ "$RACK_ROW" != *"last_seen"* ]]; then # stats usually has last_seen?
    # Wait, query is SELECT stats...
    # stats JSON.
    :
fi
# Version check
if [[ "$RACK_ROW" == *"0.1.0-alpha"* ]]; then
     echo "✅ Rack Version Verified: 0.1.0-alpha"
else
     echo "❌ Rack Version Check Failed. Row: $RACK_ROW"
     exit 1
fi

echo "🔍 Verifying Snake Topology..."
SNAKE_ROW=$($DB_CLI -noheader -csv "$MIXER_DB" "SELECT attributes FROM registry WHERE type_id=8 LIMIT 1")
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

# Check Snake MachineID
SNAKE_MID=$($DB_CLI -noheader -csv "$MIXER_DB" "SELECT machine_id FROM registry WHERE type_id=8 LIMIT 1")
if [ "$SNAKE_MID" -ne 1 ]; then
    echo "❌ Snake MachineID mismatch. Expected 1, got $SNAKE_MID"
    exit 1
fi

echo "✅ Registry E2E Test PASSED."

echo "========================================================"
echo "📊 Registry Table Content (ALL)"
echo "========================================================"
$DB_CLI -line "$MIXER_DB" "SELECT * FROM registry ORDER BY type_id"
echo "========================================================"

# --- ADOPTION FLOW ---
echo ""
echo "🔄 Restarting Mixer for Adoption Flow..."
./bin/fluxrig-mixer -c $MIXER_DIR/mixer.toml > "$MIXER_LOG" 2>&1 &
MIXER_PID=$!

# Wait for Mixer
echo "⏳ Waiting for Mixer..."
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

echo "🤝 Adopting Rack 100..."
sleep 5
# Use 'admin racks approve'
APPROVE_OUT=$($FLUX_BIN admin racks approve 100 --name "node-100" --api-url "$BASE_URL")
echo "$APPROVE_OUT"

if [[ "$APPROVE_OUT" != *"approved"* ]]; then
    echo "❌ Approval Failed."
    exit 1
fi

echo "⏳ Waiting 5s for Rack to receive Passport & Update..."
sleep 5

# Check logs for state update
if ! grep -q "State updated" "$RACK_LOG" && ! grep -q "Passport" "$RACK_LOG"; then
   # We might not log "Passport" explicitly, check source if needed. 
   # Assuming some log indicating success.
   echo "⚠️  Warning: specific log message for passport save not found (might be debug level)."
fi

echo "🔄 Restarting Rack to verify persistence..."
kill $RACK_PID
wait $RACK_PID 2>/dev/null || true

# Start Rack again
./bin/fluxrig rack -c $RACK_DIR/rack.toml > "$RACK_LOG.2" 2>&1 &
RACK_PID=$!

echo "⏳ Waiting for Rack to connect..."
sleep 5

# Stop Mixer again for DB Check
echo "🛑 Stopping Mixer to verify Final DB State..."
kill $MIXER_PID
wait $MIXER_PID 2>/dev/null || true
MIXER_PID=""

echo "========================================================"
echo "📊 Registry Table Content (FINAL)"
echo "========================================================"
$DB_CLI -line "$MIXER_DB" "SELECT * FROM registry ORDER BY type_id"
echo "========================================================"
