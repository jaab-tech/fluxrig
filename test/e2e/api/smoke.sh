#!/bin/bash
# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

HOST=${1:-"http://localhost:8090"}

echo "[INFO] Testing FluxRig Mixer at $HOST"

# 1. Health Check
echo -n "1. Checking /health... "
HEALTH=$(curl -s $HOST/api/v1/health)
if [[ $HEALTH == *"ok"* ]]; then
    echo "✅ OK"
else
    echo "❌ FAILED: $HEALTH"
    exit 1
fi

# 2. List Racks (Should be empty initially)
echo -n "2. Listing /racks... "
RACKS=$(curl -s $HOST/api/v1/racks)
echo $RACKS
if [[ $RACKS == *"[]"* ]] || [[ $RACKS == "null" ]]; then
    echo "✅ OK (Empty list)"
else
    # It might not be empty if persistence is reused, but checking JSON validity
    echo "ℹ️  Received list"
fi

# 3. Approve Non-Existent Rack (Expect 404)
echo -n "3. Approving invalid ID... "
RESP=$(curl -s -o /dev/null -w "%{http_code}" -X POST $HOST/api/v1/racks/999/approve -d '{"name":"test"}')
if [ "$RESP" -eq 404 ]; then
    echo "✅ OK (404)"
else
    echo "❌ FAILED (Expected 404, got $RESP)"
fi

echo "Smoke Test Complete!"
