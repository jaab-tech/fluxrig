#!/bin/bash
set -u

# Config
TEST_DIR="test/e2e_conflict"
MIXER_DIR="$TEST_DIR/mixer"
RACK_A_DIR="$TEST_DIR/rack_a"
RACK_B_DIR="$TEST_DIR/rack_b"
RACK_C_DIR="$TEST_DIR/rack_c"
RACK_D_DIR="$TEST_DIR/rack_d"
RACK_E_DIR="$TEST_DIR/rack_e"

MIXER_LOG="$MIXER_DIR/logs/mixer.log"
MIXER_CONFIG="$MIXER_DIR/mixer.toml"

# APIs
API_URL="http://localhost:8090/api/v1"

# PIDs
MIXER_PID=""
RACK_A_PID=""
RACK_B_PID=""
RACK_C_PID=""
RACK_D_PID=""
RACK_E_PID=""

echo "========================================================"
echo "⚔️ Starting Conflict & Identity E2E Test"
echo "========================================================"

cleanup() {
    echo ""
    echo "🧹 Cleanup..."
    kill $MIXER_PID 2>/dev/null || true
    kill $RACK_A_PID 2>/dev/null || true
    kill $RACK_B_PID 2>/dev/null || true
    kill $RACK_C_PID 2>/dev/null || true
    kill $RACK_D_PID 2>/dev/null || true
    kill $RACK_E_PID 2>/dev/null || true
    pkill -f "bin/fluxrig" || true
}
trap cleanup EXIT

# 0. Prep
pkill -f "bin/fluxrig" || true
# Clean directories
rm -rf $TEST_DIR/*/data $TEST_DIR/*/logs

mkdir -p $MIXER_DIR/data $MIXER_DIR/logs
mkdir -p $RACK_A_DIR/data $RACK_A_DIR/logs
mkdir -p $RACK_B_DIR/data $RACK_B_DIR/logs
mkdir -p $RACK_C_DIR/data $RACK_C_DIR/logs
mkdir -p $RACK_D_DIR/data $RACK_D_DIR/logs
mkdir -p $RACK_E_DIR/data $RACK_E_DIR/logs

# Configs for Scenarios are now static in $RACK_*_DIR/rack.toml




# Config C & D (Zero Config)




# 1. Start Mixer
echo "🚀 Starting Mixer..."
./bin/fluxrig keys gen-cluster -o $MIXER_DIR/data/cluster.key > /dev/null
./bin/fluxrig-mixer -c $MIXER_CONFIG > "$MIXER_LOG" 2>&1 &
MIXER_PID=$!
sleep 2

# ==========================================
# Scenario 1: Active Conflict (Security)
# ==========================================
echo "----------------------------------------"
echo "Validating Scenario 1: Active Conflict"
echo "----------------------------------------"

echo "🔌 Starting Rack A (Legitimate Owner)..."
./bin/fluxrig rack -c $RACK_A_DIR/rack.toml > "$RACK_A_DIR/logs/rack.log" 2>&1 &
RACK_A_PID=$!

sleep 2
grep "Passport Verified" "$RACK_A_DIR/logs/rack.log" || { echo "❌ Rack A failed to register"; exit 1; }
ID_A=$(grep "Passport Verified" "$RACK_A_DIR/logs/rack.log" | grep -o 'id=[0-9]*' | xargs | cut -d= -f2)
NAME_A=$(grep "Passport Verified" "$RACK_A_DIR/logs/rack.log" | grep -o 'name=[^ ]*' | xargs | cut -d= -f2)
echo "✅ Rack A Registered (ID: $ID_A, Name: $NAME_A)"

echo "😈 Starting Rack B (Hijacker - No Secret)..."
./bin/fluxrig rack -c $RACK_B_DIR/rack.toml > "$RACK_B_DIR/logs/rack.log" 2>&1 &
RACK_B_PID=$!

sleep 2
# Verify B Failed
if grep -q "Passport Verified" "$RACK_B_DIR/logs/rack.log"; then
    echo "❌ Fail: Rack B registered successfully (Should be Rejected)"
    exit 1
fi
# Check for rejection log or lack of it. Logic: If it doesn't get passport, it failed.
echo "✅ Success: Rack B was rejected (No Passport issued)"
kill $RACK_B_PID || true

# ==========================================
# Scenario 2: Session Recovery (Recovery)
# ==========================================
echo "----------------------------------------"
echo "Validating Scenario 2: Session Recovery"
echo "----------------------------------------"

echo "💀 Killing Rack A..."
kill $RACK_A_PID
wait $RACK_A_PID 2>/dev/null

echo "♻️ Restarting Rack A (With Passport/Secret)..."
./bin/fluxrig rack -c $RACK_A_DIR/rack.toml >> "$RACK_A_DIR/logs/rack.log" 2>&1 &
RACK_A_PID=$!

sleep 2
CHECK_RECOVERY=$(tail -n 20 "$RACK_A_DIR/logs/rack.log" | grep "Loaded Cached Passport")
if [ -z "$CHECK_RECOVERY" ]; then
    echo "❌ Fail: Rack A did not load cached passport"
    exit 1
fi
echo "✅ Success: Rack A recovered session"

# ==========================================
# Scenario 3: Zero Config (Auto-Scale)
# ==========================================
echo "----------------------------------------"
echo "Validating Scenario 3: Zero-Config (Cattle)"
echo "----------------------------------------"

echo "🐮 Starting Rack C..."
./bin/fluxrig rack -c $RACK_C_DIR/rack.toml > "$RACK_C_DIR/logs/rack.log" 2>&1 &
RACK_C_PID=$!
sleep 2

ID_C=$(grep "Passport Verified" "$RACK_C_DIR/logs/rack.log" | grep -o 'id=[0-9]*' | xargs | cut -d= -f2)
NAME_C=$(grep "Passport Verified" "$RACK_C_DIR/logs/rack.log" | grep -o 'name=[^ ]*' | xargs | cut -d= -f2)
if [ -z "$ID_C" ]; then
    echo "❌ Fail: Rack C failed to register"
    exit 1
fi

if [[ "$NAME_C" != probes-* ]]; then
    echo "❌ Fail: Rack C assigned name '$NAME_C' does not start with 'probes-'"
    exit 1
fi
echo "✅ Rack C Registered (ID: $ID_C, Name: $NAME_C)"

echo "🐮 Starting Rack D..."
./bin/fluxrig rack -c $RACK_D_DIR/rack.toml > "$RACK_D_DIR/logs/rack.log" 2>&1 &
RACK_D_PID=$!
sleep 2

ID_D=$(grep "Passport Verified" "$RACK_D_DIR/logs/rack.log" | grep -o 'id=[0-9]*' | xargs | cut -d= -f2)
NAME_D=$(grep "Passport Verified" "$RACK_D_DIR/logs/rack.log" | grep -o 'name=[^ ]*' | xargs | cut -d= -f2)
if [ -z "$ID_D" ]; then
    echo "❌ Fail: Rack D failed to register"
    exit 1
fi

if [[ "$NAME_D" != probes-* ]]; then
    echo "❌ Fail: Rack D assigned name '$NAME_D' does not start with 'probes-'"
    exit 1
fi
echo "✅ Rack D Registered (ID: $ID_D, Name: $NAME_D)"

if [ "$ID_C" == "$ID_D" ]; then
    echo "❌ Fail: Rack C and D got same ID ($ID_C)"
    exit 1
fi
echo "✅ Success: Unique IDs assigned via Auto-Scale"

# ==========================================
# Scenario 4: Default Zero Config (No Prefix)
# ==========================================
echo "----------------------------------------"
echo "Validating Scenario 4: Default Zero Config (Default Prefix)"
echo "----------------------------------------"

echo "🐮 Starting Rack E (No Prefix)..."
./bin/fluxrig rack -c $RACK_E_DIR/rack.toml > "$RACK_E_DIR/logs/rack.log" 2>&1 &
RACK_E_PID=$!
sleep 2

ID_E=$(grep "Passport Verified" "$RACK_E_DIR/logs/rack.log" | grep -o 'id=[0-9]*' | xargs | cut -d= -f2)
NAME_E=$(grep "Passport Verified" "$RACK_E_DIR/logs/rack.log" | grep -o 'name=[^ ]*' | xargs | cut -d= -f2)
if [ -z "$ID_E" ]; then
    echo "❌ Fail: Rack E failed to register"
    exit 1
fi

if [[ "$NAME_E" != node-* ]]; then
    echo "❌ Fail: Rack E assigned name '$NAME_E' does not start with default 'node-'"
    exit 1
fi
echo "✅ Rack E Registered (ID: $ID_E, Name: $NAME_E)"


echo "========================================================"
echo "✅ ALL SCENARIOS PASSED"
echo "========================================================"
exit 0
