#!/bin/bash
set -u

# Config
# Config
# Paths relative to project root (Execution from root is assumed)
TEST_DIR="test/e2e_simple"
MIXER_DIR="$TEST_DIR/mixer"
RACK_DIR="$TEST_DIR/rack"

MIXER_CONFIG="$MIXER_DIR/mixer.toml"
RACK_CONFIG="$RACK_DIR/rack.toml"
API_URL="http://localhost:8090/api/v1"

MIXER_LOG="$MIXER_DIR/logs/mixer.log"
RACK_LOG="$RACK_DIR/logs/rack.log"

MIXER_PID=""
RACK_PID=""

echo "========================================================"
echo "🧪 Starting Simple E2E Test"
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
    # Fallback cleanup only if needed
    pkill -P $$ 2>/dev/null || true
}
trap cleanup EXIT

# 1. Clean
echo "Possibly killing old processes..."
pkill -f "bin/fluxrig" || true
sleep 1
pkill -f "bin/fluxrig" || true
sleep 1
# Clean specific data dirs
rm -rf $MIXER_DIR/data $MIXER_DIR/logs $RACK_DIR/data $RACK_DIR/logs
mkdir -p $MIXER_DIR/data $MIXER_DIR/logs
mkdir -p $RACK_DIR/data $RACK_DIR/logs

# 2. Build
echo "🔨 Building..."
make build > $MIXER_DIR/logs/build.log 2>&1
if [ $? -ne 0 ]; then
    echo "❌ Build failed. Check $MIXER_DIR/logs/build.log"
    exit 1
fi

# 3. Keys
echo "🔐 Generating Keys..."
# Key gen needs to output to test mixer dir
./bin/fluxrig keys gen-cluster -o $MIXER_DIR/data/cluster.key > /dev/null
if [ ! -f "$MIXER_DIR/data/cluster.key" ]; then
    echo "❌ Failed to generate cluster.key"
    exit 1
fi
echo "✅ Keys generated."

# 4. Start Mixer
echo "🚀 Starting Mixer..."
./bin/fluxrig-mixer -c $MIXER_CONFIG > "$MIXER_LOG" 2>&1 &
MIXER_PID=$!
echo "   Mixer PID: $MIXER_PID"

# Wait for Mixer
echo "⏳ Waiting for Mixer Health..."
MAX_RETRIES=30
for ((i=1;i<=MAX_RETRIES;i++)); do
    HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$API_URL/health")
    if [ "$HTTP_CODE" == "200" ]; then
        echo "✅ Mixer is UP."
        break
    fi
    sleep 1
    if [ $i -eq $MAX_RETRIES ]; then
        echo "❌ Mixer start timeout."
        echo "❌ Mixer start timeout."
        cat "$MIXER_LOG"
        exit 1
    fi
done

# 5. Start Rack
echo "🔌 Starting Rack..."
echo "🔌 Starting Rack..."
./bin/fluxrig rack -c $RACK_CONFIG > "$RACK_LOG" 2>&1 &
RACK_PID=$!
echo "   Rack PID: $RACK_PID"

# 6. Verify Registration (Loop)
echo "🔍 Verifying Registration (Expecting 'rack-e2e-01')..."
FOUND=0
for ((i=1;i<=30;i++)); do
    # Diagnostic: Check raw DB from outside
    # echo "   [Diag] Checking DB file directly..."
    # go run test/check_db.go "$DATA_DIR/fluxrig_test.duckdb" > "$LOG_DIR/db_check.txt" 2>&1
    # cat "$LOG_DIR/db_check.txt"

    curl -s --max-time 2 "$API_URL/racks" > "$MIXER_DIR/logs/api_racks.json"
    if grep -q "rack-e2e-01" "$MIXER_DIR/logs/api_racks.json"; then
        echo "✅ Rack Registered and Found in API!"
        FOUND=1
        break
     else
          # Print what we see periodically (every 5s)
          if (( i % 5 == 0 )); then
              echo "   (API returned: $(cat $MIXER_DIR/logs/api_racks.json))"
          fi
     fi
    sleep 1
    echo -n "."
done
echo ""

if [ $FOUND -eq 0 ]; then
    echo "❌ Timeout waiting for registration."
    echo "--- Rack Log ---"
    cat "$RACK_LOG"
    echo "--- Mixer Log (Tail) ---"
    tail -n 20 "$MIXER_LOG"
    exit 1
fi

# 6.5 Verify State File (Passport)
echo "📜 Verifying Passport (state.flux)..."
STATE_FILE="$RACK_DIR/data/state.flux"
if [ -f "$STATE_FILE" ]; then
    echo "✅ Passport found at $STATE_FILE"
    echo "🔍 Inspecting Passport..."
    go run test/helpers/inspect_state.go "$STATE_FILE"
else
    echo "❌ Passport MISSING at $STATE_FILE"
    echo "--- Rack Log (Tail) ---"
    tail -n 20 "$RACK_LOG"
    exit 1
fi

# 7. List Racks (CLI Check)
echo "🖥️  Verifying CLI..."
./bin/fluxrig admin --api-url "http://localhost:8090" racks list > "$MIXER_DIR/logs/cli_list.txt" 2>&1
if grep -q "rack-e2e-01" "$MIXER_DIR/logs/cli_list.txt"; then
    echo "✅ CLI lists the rack."
else
    echo "❌ CLI failed to list rack."
    cat "$MIXER_DIR/logs/cli_list.txt"
    exit 1
fi

# 8. Verify DB Tables and Content
echo "🔍 Verifying DB Content (Snapshot)..."
# Copy DB to temp to avoid lock
cp "$MIXER_DIR/data/fluxrig_test.duckdb" "$MIXER_DIR/data/snapshot.duckdb"
[ -f "$MIXER_DIR/data/fluxrig_test.duckdb.wal" ] && cp "$MIXER_DIR/data/fluxrig_test.duckdb.wal" "$MIXER_DIR/data/snapshot.duckdb.wal"

echo "📋 Checking 'registry' table (Racks)..."
DB_OUT=$(duckdb -readonly -csv -noheader -c "SELECT name, machine_id FROM registry WHERE name='rack-e2e-01' AND type_id=3;" "$MIXER_DIR/data/snapshot.duckdb")
echo "   DB Row: $DB_OUT"

if [[ "$DB_OUT" == *"rack-e2e-01"* ]]; then
    echo "✅ DB Verification Passed (Rack found in DB)"
else
    echo "❌ DB Verification Failed (Rack not found in DB snapshot)"
    exit 1
fi

rm -f "$MIXER_DIR/data/snapshot.duckdb" "$MIXER_DIR/data/snapshot.duckdb.wal"

# 9. Wait for Heartbeats
echo "⏳ Letting agent run for heartbeats (5s)..."
sleep 5

echo "========================================================"
echo "✅ SUCCESS: All checks passed."
echo "========================================================"
