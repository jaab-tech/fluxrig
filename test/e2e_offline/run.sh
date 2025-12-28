#!/bin/bash
set -u

# Config
# Config
TEST_DIR="test/e2e_offline"
MIXER_DIR="$TEST_DIR/mixer"
RACK_DIR="$TEST_DIR/rack"

MIXER_LOG="$MIXER_DIR/logs/mixer.log"
RACK_LOG="$RACK_DIR/logs/rack.log"

MIXER_CONFIG="$MIXER_DIR/mixer.toml"
RACK_CONFIG="$RACK_DIR/rack.toml"
API_URL="http://localhost:8090/api/v1"

MIXER_PID=""
RACK_PID=""

echo "========================================================"
echo "🔌 Starting Offline Mode E2E Test (Isolated)"
echo "========================================================"

cleanup() {
    echo ""
    echo "🧹 Cleanup..."
    if [ -n "$MIXER_PID" ]; then kill $MIXER_PID 2>/dev/null || true; wait $MIXER_PID 2>/dev/null || true; fi
    if [ -n "$RACK_PID" ]; then kill $RACK_PID 2>/dev/null || true; wait $RACK_PID 2>/dev/null || true; fi
    pkill -f "bin/fluxrig" || true
}
trap cleanup EXIT

# 0. Prep
pkill -f "bin/fluxrig" || true
rm -rf $MIXER_DIR/data $MIXER_DIR/logs 
rm -rf $RACK_DIR/data $RACK_DIR/logs 

mkdir -p $MIXER_DIR/data $MIXER_DIR/logs
mkdir -p $RACK_DIR/data $RACK_DIR/logs

# 1. Start Mixer
echo "--- Phase 1: Online Enrollment ---"
echo "🔐 Generating Keys..."
./bin/fluxrig keys gen-cluster -o $MIXER_DIR/data/cluster.key > /dev/null

echo "🚀 Starting Mixer..."
./bin/fluxrig-mixer -c $MIXER_CONFIG > "$MIXER_LOG" 2>&1 &
MIXER_PID=$!
sleep 2

echo "🔌 Starting Rack (Online)..."
./bin/fluxrig rack -c $RACK_CONFIG > "$RACK_LOG" 2>&1 &
RACK_PID=$!

# Wait for Passport and Heartbeats
# Interval is 2s, so we wait 6s to see at least 2 heartbeats
sleep 6

if grep -q "Passport Saved" "$RACK_LOG"; then
    echo "✅ Passport Acquired."
else
    echo "❌ Failed to acquire passport."
    cat "$RACK_LOG"
    exit 1
fi

if grep -q "Sent Heartbeat" "$RACK_LOG"; then
    echo "✅ Online Activity Verified (Sent Heartbeats)."
else
    echo "❌ Failed to send heartbeats (No online activity detected)."
    cat "$RACK_LOG"
    exit 1
fi

# 3. Stop Everything
echo "🛑 Stopping World..."
kill $MIXER_PID
wait $MIXER_PID 2>/dev/null || true
MIXER_PID=""

kill $RACK_PID
wait $RACK_PID 2>/dev/null || true
RACK_PID=""

echo "✅ Environment Stopped. Mixer is DEAD."

# 4. Phase 2: Offline Startup
echo "--- Phase 2: Offline Startup ---"
echo "🔌 Starting Rack (Offline)..."
# We append to same log or new? Let's use same log for simplicity, or append
./bin/fluxrig rack -c $RACK_CONFIG >> "$RACK_LOG" 2>&1 &
RACK_PID=$!

# Wait for Startup
sleep 2

# 5. Verification
LOG="$RACK_LOG"

# Check 1: Loaded Cached Passport
if grep -q "Loaded Cached Passport" "$LOG"; then
    echo "✅ (1/3) Rack loaded cached passport."
else
    echo "❌ (1/3) Rack failed to load passport."
    cat "$LOG"
    exit 1
fi

# Check 2: Offline Mode
if grep -q "Starting in OFFLINE Mode" "$LOG"; then
    echo "✅ (2/3) Rack detected Offline Mode."
else
    echo "❌ (2/3) Rack did not report Offline Mode."
    cat "$LOG"
    exit 1
fi

# Check 3: Process is still running
if ps -p $RACK_PID > /dev/null; then
    echo "✅ (3/3) Rack process is still ALIVE."
else
    echo "❌ (3/3) Rack process DIED."
    cat "$LOG"
    exit 1
fi

echo "✅ Offline Mode Verified."
