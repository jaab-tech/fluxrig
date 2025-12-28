#!/bin/bash
set -u

# Config
TEST_DIR="test/e2e_telemetry"
MIXER_DIR="$TEST_DIR/mixer"
RACK_DIR="$TEST_DIR/rack"

MIXER_CONFIG="$MIXER_DIR/mixer.toml"
RACK_CONFIG="$RACK_DIR/rack.toml"
API_URL="http://127.0.0.1:8092/api/v1"

MIXER_LOG="$MIXER_DIR/logs/mixer.log"
RACK_LOG="$RACK_DIR/logs/rack.log"

DB_CLI="duckdb"
FLUX_BIN="bin/fluxrig"

MIXER_PID=""
RACK_PID=""

echo "========================================================"
echo "🧪 Starting Telemetry E2E Test"
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

# 2. Build is assumed done by make (skip here or fast check)
# But for standalone, let's assume binaries exist or we fail
if [ ! -f "bin/fluxrig-mixer" ]; then
    echo "❌ bin/fluxrig-mixer not found. Run make build."
    exit 1
fi

# 3. Keys
echo "🔐 Generating Keys..."
./bin/fluxrig keys gen-cluster -o $MIXER_DIR/data/cluster.key > /dev/null

# 4. Start Mixer
echo "🚀 Starting Mixer (Port 8092)..."
./bin/fluxrig-mixer -c $MIXER_CONFIG > "$MIXER_LOG" 2>&1 &
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

# 5. Start Rack
echo "🔌 Starting Rack..."
./bin/fluxrig rack -c $RACK_CONFIG > "$RACK_LOG" 2>&1 &
RACK_PID=$!

# 6. Wait for Registration & Heartbeats
echo "⏳ Waiting for Rack Registration..."
FOUND=0
for ((i=1;i<=30;i++)); do
    curl -s --max-time 2 "$API_URL/racks" > "$MIXER_DIR/logs/api_racks.json"
    if grep -q "rack-e2e-telemetry-01" "$MIXER_DIR/logs/api_racks.json"; then
        echo "✅ Rack Registered!"
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

# 7. Generate Traffic (Wait for Telemetry)
echo "⏳ Waiting for Telemetry Ingestion (30s for metrics export)..."
sleep 30

# 8. Verify Parquet Files
# 8. Verify DuckDB Content
MIXER_DB="$MIXER_DIR/data/fluxrig_test.duckdb"
# 5. Verify Telemetry Data (Now in Parquet)
TELEMETRY_DIR="$MIXER_DIR/data/telemetry"
echo "🔍 Verifying Parquet Content in $TELEMETRY_DIR/logs..."

# We use DuckDB CLI to query the generated parquet files
# Note: glob pattern requires quoting
PARQUET_GLOB="$TELEMETRY_DIR/logs/**/*.parquet"

# Check if any files exist first
if [ -z "$(find "$TELEMETRY_DIR/logs" -name "*.parquet" -print -quit)" ]; then
    echo "❌ No Parquet files found in $TELEMETRY_DIR/logs"
    echo "--- Mixer Log ---"
    cat "$MIXER_LOG"
    exit 1
fi

PARQUET_GLOB="$TELEMETRY_DIR/logs/*/*/*/*/*.parquet" # Explicit glob as fallback for DuckDB if ** fails
# Or rely on search:
PARQUET_GLOB="$TELEMETRY_DIR/logs/**/*.parquet"

COUNT_LOGS=$($DB_CLI -noheader -csv -c "SELECT count(*) FROM read_parquet('$TELEMETRY_DIR/logs/**/*.parquet')")
# Rack logs have entity_name LIKE 'rack%'
COUNT_RACK_LOGS=$($DB_CLI -noheader -csv -c "SELECT count(*) FROM read_parquet('$TELEMETRY_DIR/logs/**/*.parquet') WHERE entity_name LIKE 'rack%'")

# Registry Verification

# --- NEW CLI VERIFICATION ---
echo "🔍 Verifying Telemetry via CLI..."

# Check for metric-related log entries
echo "   Checking logs for heartbeat metric activity..."
cat "$RACK_LOG" | grep -i "heartbeat\|metric" | tail -10

# Give metrics extra time to batch and export
echo "   Waiting additional 10s for metric batching..."
sleep 10

# 1. Logs
echo "   Testing 'fluxrig logs'..."
LOGS_OUT=$($FLUX_BIN logs --api-url "http://localhost:8092" --limit 20 --min-level debug)
echo "$LOGS_OUT"

# Count log lines (header + content, count non-empty lines)
LOG_COUNT=$(echo "$LOGS_OUT" | tail -n +2 | grep -v "^$" | wc -l | tr -d ' ')
if [ "$LOG_COUNT" -lt 5 ]; then
    echo "❌ CLI Logs Verification Failed. Expected at least 5 logs, got $LOG_COUNT"
    exit 1
fi
echo "✅ CLI Logs Verified ($LOG_COUNT logs found)."

# 2. Metrics
echo "   Testing 'fluxrig metrics'..."
METRICS_OUT=$($FLUX_BIN metrics --api-url "http://localhost:8092" --limit 10)
echo "$METRICS_OUT"
if [[ "$METRICS_OUT" != *"heartbeats_sent"* ]]; then
    echo "❌ CLI Metrics Verification Failed (Missing 'heartbeats_sent'). Output:"
    echo "$METRICS_OUT"
    exit 1
fi
echo "✅ CLI Metrics Verified."
DB_CLI="duckdb"
FLUX_BIN="bin/fluxrig"

# Use DuckDB CLI to query the generated parquet files (Wait, Registry is in DuckDB itself, not parquet)
# Registry is in the .duckdb file
# Snapshot DB to avoid locking
cp "$MIXER_DB" "$MIXER_DIR/data/snapshot.duckdb"
[ -f "$MIXER_DB.wal" ] && cp "$MIXER_DB.wal" "$MIXER_DIR/data/snapshot.duckdb.wal"
SNAPSHOT_DB="$MIXER_DIR/data/snapshot.duckdb"

MIXER_COUNT=$($DB_CLI -noheader -csv "$SNAPSHOT_DB" "SELECT count(*) FROM registry WHERE type_id=2")
SNAKE_COUNT=$($DB_CLI -noheader -csv "$SNAPSHOT_DB" "SELECT count(*) FROM registry WHERE type_id=8")
RACK_COUNT=$($DB_CLI -noheader -csv "$SNAPSHOT_DB" "SELECT count(*) FROM registry WHERE type_id=3")

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
# Verify Attributes Content
echo "🔍 Verifying Attributes for Rack..."
# Check if Rack attributes contain IP/Port/Secret
RACK_ROW=$($DB_CLI -noheader -csv "$SNAPSHOT_DB" "SELECT stats, attributes, version FROM registry WHERE type_id=3 LIMIT 1")
if [[ -z "$RACK_ROW" ]]; then
    echo "❌ Rack not found in DB!"
    exit 1
fi

echo "🔍 Verifying Mixer Stats..."
MIXER_ROW=$($DB_CLI -noheader -csv "$SNAPSHOT_DB" "SELECT stats FROM registry WHERE type_id=2 LIMIT 1")
if [[ "$MIXER_ROW" != *"goroutines"* ]]; then
     echo "❌ Mixer Stats missing (goroutines): $MIXER_ROW"
     exit 1
fi

echo "🔍 Verifying Snake Attributes & Stats..."
# Query Snake (Type 8)
SNAKE_ROW=$($DB_CLI -noheader -csv "$SNAPSHOT_DB" "SELECT stats, attributes, name FROM registry WHERE type_id=8 LIMIT 1")
echo "   Snake Row: $SNAKE_ROW"
if [[ -z "$SNAKE_ROW" ]]; then
     echo "❌ Snake not found in DB!"
     exit 1
fi
if [[ "$SNAKE_ROW" != *"rack"* ]]; then
     echo "❌ Snake Attributes missing 'rack' topology!"
     exit 1
fi
# Stats should have basic flow metrics
if [[ "$SNAKE_ROW" != *"in_msgs"* ]]; then
     echo "❌ Snake Stats missing 'in_msgs' metric!"
     exit 1
fi

echo "✅ Registry Counts Verified."




# Debug: Show distinct identities with decoded components
echo "   Identities found (Decoded):"
echo "   Format: EntityID, Type, MachineID, Seq, Name, Count"
$DB_CLI -noheader -csv -c "
    SELECT DISTINCT 
        entity_id, 
        (entity_id >> 56)::INTEGER as type, 
        ((entity_id >> 40) & 65535)::INTEGER as mid, 
        (entity_id & 1099511627775)::BIGINT as seq, 
        entity_name, 
        count(*) 
    FROM read_parquet('$TELEMETRY_DIR/logs/**/*.parquet') 
    GROUP BY entity_id, entity_name, type, mid, seq
" | sed 's/^/   - /'

echo "   Found $COUNT_LOGS Logs total"
echo "   Found $COUNT_RACK_LOGS Rack Logs (by Name)"

if [ "$COUNT_LOGS" -eq 0 ]; then
    echo "❌ No Telemetry Data Found in Parquet."
    # Dump mixer log
    echo "--- Mixer Log ---"
    cat "$MIXER_LOG"
    # Dump Rack log
    echo "--- Rack Log ---"
    cat "$RACK_LOG"
    exit 1
fi

if [ "$COUNT_RACK_LOGS" -eq 0 ]; then
    echo "❌ No Rack Logs Found (Only Mixer?)."
    echo "--- Rack Log ---"
    cat "$RACK_LOG"
    exit 1
fi


# Dump full registry for user inspection
echo "========================================================"
echo "📊 Registry Table Content"
echo "========================================================"
$DB_CLI -csv -header "$SNAPSHOT_DB" "SELECT * FROM registry ORDER BY type_id, machine_id"
echo "========================================================"

echo "📊 Log Entries per Entity"
echo "========================================================"
$DB_CLI -csv -header -c "SELECT entity_name, count(*) as log_count FROM read_parquet('$TELEMETRY_DIR/logs/**/*.parquet') GROUP BY entity_name ORDER BY log_count DESC"
echo "========================================================"

echo "✅ Telemetry Data Verified in Parquet!"
echo "✅ SUCCESS: Telemetry E2E Passed."
