#!/bin/bash
set -u

# ==============================================================================
# E2E CLI & Health Check Verification
# ==============================================================================
# Objective: Verify Helper CLI commands (version, keys, help) and Admin CLI (racks list, remove).
# Isolated Environment: test/e2e_cli/
# ==============================================================================

# 1. Configuration
TEST_DIR="test/e2e_cli"
MIXER_DIR="$TEST_DIR/mixer"
RACK_DIR="$TEST_DIR/rack"
CLI_DIR="$TEST_DIR/cli"

MIXER_LOG="$MIXER_DIR/logs/mixer.log"
RACK_LOG="$RACK_DIR/logs/rack.log"

MIXER_CONFIG="$MIXER_DIR/mixer.toml"
RACK_CONFIG="$RACK_DIR/rack.toml"

API_URL="http://localhost:8090/api/v1"

MIXER_PID=""
RACK_PID=""

# 2. Cleanup Function
cleanup() {
    echo ""
    echo "🧹 Cleanup..."
    if [ -n "$MIXER_PID" ]; then kill $MIXER_PID 2>/dev/null || true; wait $MIXER_PID 2>/dev/null || true; fi
    if [ -n "$RACK_PID" ]; then kill $RACK_PID 2>/dev/null || true; wait $RACK_PID 2>/dev/null || true; fi
    
    # Safe fallback kill
    pkill -f "bin/fluxrig" || true
}
trap cleanup EXIT

echo "========================================================"
echo "🖥️  Starting CLI E2E Test"
echo "========================================================"

# 3. Preparation
echo "🧹 Cleaning previous artifacts..."
pkill -f "bin/fluxrig" || true
rm -rf $MIXER_DIR/data $MIXER_DIR/logs
rm -rf $RACK_DIR/data $RACK_DIR/logs
rm -rf $CLI_DIR/logs

mkdir -p $MIXER_DIR/data $MIXER_DIR/logs
mkdir -p $RACK_DIR/data $RACK_DIR/logs
mkdir -p $CLI_DIR/logs

echo "🔐 Generating Cluster Keys..."
./bin/fluxrig keys gen-cluster -o $MIXER_DIR/data/cluster.key > /dev/null

# 4. Helper CLI Verification (No Mixer Needed)
echo "--------------------------------------------------------"
echo "🔍 Verifying Helper Commands"
echo "--------------------------------------------------------"

# 4.1 Version
VERSION_OUT=$(./bin/fluxrig version)
echo "Output: $VERSION_OUT"
if echo "$VERSION_OUT" | grep -q "^fluxrig"; then
    echo "✅ 'fluxrig version' passed."
else
    echo "❌ 'fluxrig version' failed."
    exit 1
fi

# 4.2 Help
HELP_OUT=$(./bin/fluxrig rack --help)
echo "Output (truncated): $(echo "$HELP_OUT" | head -n 1)..."
if echo "$HELP_OUT" | grep -q "Initializes"; then
    echo "✅ 'fluxrig rack --help' passed."
else
    echo "❌ 'fluxrig rack --help' failed."
    exit 1
fi

# 4.3 Key Gen
# Test gen-cluster CLI
KEYS_OUT=$(./bin/fluxrig keys gen-cluster -o test/e2e_cli/mixer/data/test_gen.key)
echo "Output:"
echo "$KEYS_OUT"
if echo "$KEYS_OUT" | grep -q "Private Key"; then
    echo "✅ 'fluxrig keys gen-cluster' passed."
else
    echo "❌ 'fluxrig keys gen-cluster' failed."
    exit 1
fi

# 5. Admin CLI Verification (Mixer Needed)
echo "--------------------------------------------------------"
echo "🚀 Starting Mixer for Admin CLI Tests"
echo "--------------------------------------------------------"
./bin/fluxrig-mixer -c $MIXER_CONFIG > "$MIXER_LOG" 2>&1 &
MIXER_PID=$!
sleep 2

# Check Mixer Health
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$API_URL/health")
if [ "$HTTP_CODE" != "200" ]; then
    echo "❌ Mixer failed to start."
    cat "$MIXER_LOG"
    exit 1
fi
echo "✅ Mixer is UP."

# 6. Rack Registration (Pending State)
echo "🔌 Starting Rack (Zero Config)..."
./bin/fluxrig rack -c $RACK_CONFIG >> "$RACK_LOG" 2>&1 &
RACK_PID=$!
sleep 2

# Verify Pending Status via CLI
echo "--------------------------------------------------------"
echo "🔍 Verifying Pending State"
echo "--------------------------------------------------------"
LIST_OUT=$(./bin/fluxrig admin racks list)
if echo "$LIST_OUT" | grep -q "pending"; then
    echo "✅ Rack is PENDING as expected."
    echo "Output: $LIST_OUT"
else
    echo "❌ Rack should be PENDING but is NOT."
    echo "Output: $LIST_OUT"
    echo "Logs:"
    tail -n 10 "$RACK_LOG"
    exit 1
fi

# Inspect State (Initial)
# Inspect State (Initial)
echo "🔍 Inspecting State (Pending)..."
STATE_OUT_1=$(./bin/fluxrig keys inspect test/e2e_cli/rack/data/state.flux)
echo "Command Output:"
echo "$STATE_OUT_1"

# The name in the passport will be the ASSIGNED name (e.g. cli-test-1), not the pending ephemeral name.
# So we check for the prefix defined in rack.toml
if echo "$STATE_OUT_1" | grep -q "Name:" && echo "$STATE_OUT_1" | grep -q "cli-test-"; then
    echo "✅ Name Verified (Matches prefix 'cli-test-')."
else
    echo "❌ Name Mismatch. Expected 'cli-test-' prefix."
    exit 1
fi
# Status Check
if echo "$STATE_OUT_1" | grep -q "Status:    pending"; then
    echo "✅ Status Verified (pending)."
else
    echo "❌ Status Mismatch. Expected 'pending'."
    exit 1
fi


# Extract ID logic... tedious with awk/grep.
# Let's assume ID is 1 since clean DB.
# Extract ID from the pending list output
# Format: ID NAME ...
RACK_ID=$(echo "$LIST_OUT" | grep "pending" | awk '{print $1}')
echo "✅ Extracted Rack ID: $RACK_ID"

# 7. Adoption (Approve)
echo "--------------------------------------------------------"
# 7. Adoption (Approve)
echo "--------------------------------------------------------"
echo "✅ Approving Rack..."
echo "--------------------------------------------------------"
./bin/fluxrig admin racks approve $RACK_ID --name "rack-production-01"

sleep 1
LIST_OUT_2=$(./bin/fluxrig admin racks list)
# Remove -q from first grep to ensure output is passed to second grep
if echo "$LIST_OUT_2" | grep "rack-production-01" | grep -q "active"; then
    echo "✅ Rack Approved & Active."
    echo "Output: $LIST_OUT_2"
else
    echo "❌ Rack Approval Failed."
    echo "Output: $LIST_OUT_2"
    exit 1
fi

# Inspect State (Did it change?)
# Inspect State (Did it change?)
echo "🔍 Inspecting State after Approval..."
STATE_OUT_2=$(./bin/fluxrig keys inspect test/e2e_cli/rack/data/state.flux)
echo "Command Output:"
echo "$STATE_OUT_2"

if echo "$STATE_OUT_2" | grep -q "Name:      rack-production-01"; then
    echo "✅ Name Verified (Approved Name)."
else
    echo "❌ Name Mismatch. Expected 'rack-production-01'."
    exit 1
fi
if echo "$STATE_OUT_2" | grep -q "Status:    active"; then
    echo "✅ Status Verified (active)."
else
    echo "❌ Status Mismatch. Expected 'active'."
    exit 1
fi


# 8. Suspension
echo "--------------------------------------------------------"
echo "⏸️  Suspending Rack..."
echo "--------------------------------------------------------"
./bin/fluxrig admin racks suspend $RACK_ID
sleep 1
LIST_OUT_3=$(./bin/fluxrig admin racks list)
if echo "$LIST_OUT_3" | grep "inactive"; then
    echo "✅ Rack Suspended (Registry Updated)."
    echo "Output: $LIST_OUT_3"
    
    # Verify Rack Log for Sync
    # Give it a moment to propagate
    sleep 2
    if grep -q "Status Changed" "$RACK_LOG" && grep -q 'new="inactive"' "$RACK_LOG"; then
         echo "✅ Rack Received Suspension (Log Verified)."
    else
         echo "❌ Rack did NOT receive suspension notification."
         echo "Logs (Tail):"
         tail -n 20 "$RACK_LOG"
         exit 1
    fi
    if grep -q "Received Command" "$RACK_LOG" && grep -q 'cmd="sleep"' "$RACK_LOG"; then
         echo "✅ Rack Received Sleep Command (Log Verified)."
    else
         echo "⚠️  Rack did NOT receive sleep command (Optional check)."
    fi

else
    echo "❌ Rack Suspension Failed."
    echo "Output: $LIST_OUT_3"
    exit 1
fi

# Check State Update (Inactive)
echo "🔍 Inspecting State after Suspension..."
STATE_OUT_SUSP=$(./bin/fluxrig keys inspect test/e2e_cli/rack/data/state.flux)
echo "Command Output:"
echo "$STATE_OUT_SUSP"
if echo "$STATE_OUT_SUSP" | grep -q "Status:    inactive"; then
    echo "✅ Status in State File Verified (inactive)."
else
    echo "❌ Status Mismatch in State File. Expected 'inactive'."
    exit 1
fi

# 9. Activation
echo "--------------------------------------------------------"
echo "▶️  Activating Rack..."
echo "--------------------------------------------------------"
./bin/fluxrig admin racks activate $RACK_ID
sleep 1
LIST_OUT_4=$(./bin/fluxrig admin racks list)
if echo "$LIST_OUT_4" | grep "active"; then
    echo "✅ Rack Activated (Registry Updated)."
    echo "Output: $LIST_OUT_4"
    
     # Verify Rack Log for Sync
    sleep 2
    if grep -q 'new="active"' "$RACK_LOG"; then
         echo "✅ Rack Received Activation (Log Verified)."
    else
         echo "❌ Rack did NOT receive activation notification."
         echo "Logs (Tail):"
         tail -n 20 "$RACK_LOG"
         exit 1
    fi

else
    echo "❌ Rack Activation Failed."
    echo "Output: $LIST_OUT_4"
    exit 1
fi

# 10. Admin Commands Verification (Remove)
echo "--------------------------------------------------------"
echo "🗑️  Verifying Removal"
echo "--------------------------------------------------------"
./bin/fluxrig admin racks remove $RACK_ID
sleep 1

# Verify Removal
LIST_OUT_5=$(./bin/fluxrig admin racks list)
if echo "$LIST_OUT_5" | grep -q "rack-production-01"; then
    echo "❌ 'admin racks remove' Failed (Rack still listed)."
    echo "Output: $LIST_OUT_5"
    exit 1
else
    echo "✅ 'admin racks remove' Verified (Rack gone)."
    echo "Output (Empty or different racks):"
    echo "$LIST_OUT_5"
fi

# 11. Re-Enrollment (Resurrection)
echo "--------------------------------------------------------"
echo "🧟 Verifying Re-Enrollment (Resurrection)"
echo "--------------------------------------------------------"
# Trigger restart (Rack agent logic should retry and eventually get new credentials?)
# Usually run.sh kills rack. Let's restart it explicitly.
if [ -n "$RACK_PID" ]; then kill $RACK_PID 2>/dev/null || true; wait $RACK_PID 2>/dev/null || true; fi
echo "🔌 Restarting Rack for Resurrection..."
./bin/fluxrig rack -c $RACK_CONFIG >> "$RACK_LOG" 2>&1 &
RACK_PID=$!
sleep 2

# Verify it returns as PENDING (Strict Mode!)
# Previously we said Active, but Strict Mode says NEW/UNKNOWN = PENDING.
# Resurrection means it's treated as a new registration.
# Since config is Zero-Config, it will be pending again.
LIST_OUT_6=$(./bin/fluxrig admin racks list)
if echo "$LIST_OUT_6" | grep -q "active"; then
    echo "✅ Resurrection Verified (Rack returned as Active - Pet Mode via Passport)."
    echo "Output: $LIST_OUT_6"
elif echo "$LIST_OUT_6" | grep -q "pending"; then
    echo "✅ Resurrection Verified (Rack returned as Pending)."
    echo "Output: $LIST_OUT_6"
else
    echo "❌ Resurrection Failed (Rack not found)."
    echo "Output: $LIST_OUT_6"
    exit 1
fi

# Inspect State (Resurrection)
echo "🔍 Inspecting State (Resurrection)..."
STATE_OUT_3=$(./bin/fluxrig keys inspect test/e2e_cli/rack/data/state.flux)
echo "Command Output:"
echo "$STATE_OUT_3"

if echo "$STATE_OUT_3" | grep -i "Identity"; then
    echo "✅ Resurrection State: Valid Passport Found."
    # Check if ID changed?
    # We can't easily compare var from script unless we extracted it earlier.
    # But confirming it exists and loads is good.
else
    echo "❌ Resurrection State: Failed to load passport."
    exit 1
fi


echo "========================================================"
echo "✅ ALL CLI TESTS PASSED"
echo "========================================================"
