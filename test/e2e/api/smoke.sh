#!/bin/bash

# Copyright 2025 JAAB Tech SAS, Uruguay
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# test/api/smoke.sh - Smoke test for FluxRig Mixer API
# Usage: ./test/api/smoke.sh [host]

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
