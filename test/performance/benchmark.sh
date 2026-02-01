#!/bin/bash
set -e

# Configuration
ECHO_PORT=8590
# Assume running from script dir or project root?
# Best practice: Detect script location
DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
PROJECT_ROOT="$DIR/../../"
ECHO_BIN="$PROJECT_ROOT/bin/tcp-echo"
LOAD_BIN="$PROJECT_ROOT/bin/iso8583-load"

# Start Echo Server
echo "---------------------------------------------------"
echo "Starting TCP Echo Server on port $ECHO_PORT..."
$ECHO_BIN -port $ECHO_PORT > echo_bench.log 2>&1 &
ECHO_PID=$!
echo "Echo Server PID: $ECHO_PID"
sleep 2

cleanup() {
    echo "Stopping Echo Server..."
    kill $ECHO_PID || true
    rm -f report_*.json
}
trap cleanup EXIT

run_test() {
    NAME=$1
    CONCURRENCY=$2
    RATE=$3
    DURATION=$4
    
    echo ""
    echo ">>> SCENARIO: $NAME"
    echo ">>> Concurrency: $CONCURRENCY | Rate: $RATE TPS | Duration: $DURATION"
    echo "---------------------------------------------------"
    
    $LOAD_BIN \
        -target localhost:$ECHO_PORT \
        -concurrency $CONCURRENCY \
        -rate $RATE \
        -duration $DURATION \
        -report "report_${NAME}.json"
    
    echo "---------------------------------------------------"
}

# 1. Warmup / Baseline
run_test "baseline" 50 2000 "10s"

# 2. High Concurrency (Stress OS Scheduler/FDs)
# Note: macOS default is often 256 or 1024. If this fails, it's a limit finding!
run_test "concurrency_stress" 1000 1000 "20s"

# 3. High Throughput Endurance (2 Minutes)
run_test "endurance_throughput" 200 5000 "2m"

echo ""
echo ">>> BENCHMARK COMPLETE"
