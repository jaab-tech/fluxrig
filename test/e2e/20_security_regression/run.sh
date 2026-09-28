#!/bin/bash
# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

set -u

# ==============================================================================
# Security Regression: fluxrig-explained/docs/17-deep-review.md,
# "Mixer, store and enrollment"
#
# One fast run covering everything that section's fixes changed about the
# request path, so this doesn't need the full 20-suite regression sweep to
# check a change here. Add to this test as PR2-PR6 land; run it alone with:
#   bash test/e2e/20_security_regression/run.sh
# ==============================================================================

BASE_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "${BASE_DIR}/../../.." && pwd)"

source "${BASE_DIR}/../utils/e2e_utils.sh"

setup_workspace "security_regression" "$BASE_DIR"

banner "Security Regression (17-deep-review, Mixer/store/enrollment)"

MIXER_CONFIG="$BASE_DIR/mixer/mixer.toml"
RACK_CONFIG="$BASE_DIR/rack/rack.toml"
API_URL="http://localhost:8099/api/v1"
AUTH_TOKEN="test-secret-token-do-not-use-in-prod"

MIXER_LOG="$WORK_DIR/mixer/mixer.stdout"
RACK_LOG="$WORK_DIR/rack/rack.stdout"

MIXER_PID=""
RACK_PID=""
RACK2_PID=""

cleanup() {
    echo ""
    log_info "Shutting down..."
    for pid in "$RACK2_PID" "$RACK_PID" "$MIXER_PID"; do
        if [ -n "$pid" ]; then kill -9 "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true; fi
    done
    pkill -P $$ 2>/dev/null || true
}
trap cleanup EXIT

log_info "Killing old processes and freeing ports..."
pkill -f "bin/fluxrig" || true
sleep 1
lsof -ti :8099 | xargs kill -9 2>/dev/null || true
lsof -ti :4299 | xargs kill -9 2>/dev/null || true

cd "${ROOT_DIR}"
mkdir -p "${WORK_DIR}/mixer/logs" "${WORK_DIR}/rack/logs"
# FLUXRIG_E2E_SKIP_BUILD is set by run_consolidated.sh, which builds once
# before launching every segment in parallel (this suite included) —
# concurrent `make build` calls would race on writing the same bin/fluxrig*
# output files. Standalone runs (this var unset) still build normally.
if [ -z "${FLUXRIG_E2E_SKIP_BUILD:-}" ]; then
    log_info "Building..."
    make build > "${WORK_DIR}/mixer/logs/build.log" 2>&1 || fail "Build failed. Check ${WORK_DIR}/mixer/logs/build.log"
fi

log_info "Generating cluster key..."
"${ROOT_DIR}/bin/fluxrig" keys gen-cluster -o "${WORK_DIR}/mixer/data/cluster.key" > /dev/null
[ -f "${WORK_DIR}/mixer/data/cluster.key" ] || fail "Failed to generate cluster.key"

# --- Start the Mixer with real auth required (unlike the rest of the harness,
# which runs with FLUXRIG_API_AUTH_DISABLED_DANGEROUSLY=true so this is the one
# place that exercises the real token path end to end). ---
log_info "Starting Mixer (auth required)..."
cp "$MIXER_CONFIG" "${WORK_DIR}/mixer/mixer.toml"
cd "${WORK_DIR}/mixer"
env -u FLUXRIG_API_AUTH_DISABLED_DANGEROUSLY "${ROOT_DIR}/bin/fluxrig-mixer" -c "mixer.toml" > "mixer.stdout" 2>&1 &
MIXER_PID=$!

log_info "Waiting for Mixer health (unauthenticated, must stay open)..."
FOUND=0
for ((i=1;i<=30;i++)); do
    CODE=$(curl -s -o /dev/null -w "%{http_code}" "$API_URL/health")
    if [ "$CODE" == "200" ]; then FOUND=1; break; fi
    sleep 1
done
if [ $FOUND -eq 0 ]; then cat "$MIXER_LOG"; fail "Mixer start timeout, or /health required auth."; fi
log_success "Mixer up, /health open with no token."

# --- 1. Unauthenticated request to a real route is rejected ---
CODE=$(curl -s -o /dev/null -w "%{http_code}" "$API_URL/racks")
if [ "$CODE" == "401" ]; then
    log_success "GET /racks with no token: 401."
else
    fail "GET /racks with no token: expected 401, got $CODE"
fi

# --- 2. Wrong token is rejected ---
CODE=$(curl -s -o /dev/null -w "%{http_code}" -H "Authorization: Bearer wrong-token" "$API_URL/racks")
if [ "$CODE" == "401" ]; then
    log_success "GET /racks with the wrong token: 401."
else
    fail "GET /racks with the wrong token: expected 401, got $CODE"
fi

# --- 3. Correct token is accepted ---
RACKS_BODY=$(curl -s -H "Authorization: Bearer $AUTH_TOKEN" "$API_URL/racks")
CODE=$(curl -s -o /dev/null -w "%{http_code}" -H "Authorization: Bearer $AUTH_TOKEN" "$API_URL/racks")
if [ "$CODE" == "200" ]; then
    log_success "GET /racks with the correct token: 200."
else
    fail "GET /racks with the correct token: expected 200, got $CODE. Body: $RACKS_BODY"
fi

# --- 3b. The admin CLI itself, not just curl, actually authenticates. The
# rest of this harness runs with auth disabled (see above), so this is the
# only place that would have caught the CLI never sending the token at all:
# every "fluxrig admin racks" call used http.Get/http.Post with no
# Authorization header until this was fixed. ---
if ! "${ROOT_DIR}/bin/fluxrig" admin racks list --api-url "http://localhost:8099" --api-token "$AUTH_TOKEN" > /dev/null; then
    fail "fluxrig admin racks list failed with the correct --api-token."
fi
log_success "admin racks list with --api-token: succeeded."

if "${ROOT_DIR}/bin/fluxrig" admin racks list --api-url "http://localhost:8099" > /dev/null 2>&1; then
    fail "fluxrig admin racks list succeeded with no token at all against an auth-required Mixer."
fi
log_success "admin racks list with no token: correctly rejected."

# --- Start a Rack: Zero-Config enrollment over NATS, unrelated to the HTTP
# API's own auth (the Rack never calls the HTTP API to enroll). This proves
# the bootstrap-secret gate lets a legitimate, correctly configured Rack in. ---
log_info "Starting Rack (Zero-Config, correct bootstrap secret)..."
cp "$RACK_CONFIG" "${WORK_DIR}/rack/rack.toml"
cd "${WORK_DIR}/rack"
"${ROOT_DIR}/bin/fluxrig" rack -c "rack.toml" > "rack.stdout" 2>&1 &
RACK_PID=$!

wait_for_log "$RACK_LOG" "Passport Verified" 20 || { cat "$RACK_LOG"; fail "Rack did not enroll (bootstrap secret path broken)."; }
log_success "Rack enrolled via the bootstrap secret."

# --- 4. The registered Rack's secret never appears in the authenticated
# response body, regardless of auth (json:"-" on registry.Rack.Secret). ---
RACKS_BODY=$(curl -s -H "Authorization: Bearer $AUTH_TOKEN" "$API_URL/racks")
if echo "$RACKS_BODY" | grep -qi '"secret"'; then
    fail "GET /racks response names a secret field: $RACKS_BODY"
fi
if ! echo "$RACKS_BODY" | grep -q "rack-secreg-01"; then
    fail "GET /racks did not list the enrolled Rack: $RACKS_BODY"
fi
log_success "Rack is listed, and its response carries no secret field."

# --- 5. A second Rack claiming the SAME name, with the SAME public bootstrap
# secret (the one every Zero-Config Rack knows), must not adopt the first
# Rack's identity: the first Rack's real secret is a freshly generated one,
# not the bootstrap value, so knowing the bootstrap secret must not be enough
# to hijack an existing name. ---
log_info "Starting a second Rack claiming the same name (hijack attempt)..."
mkdir -p "${WORK_DIR}/rack2/data" "${WORK_DIR}/rack2/logs"
sed "s#dir = \"./data\"#dir = \"${WORK_DIR}/rack2/data\"#; s#filename = \"logs/rack.log\"#filename = \"${WORK_DIR}/rack2/logs/rack.log\"#" \
    "$RACK_CONFIG" > "${WORK_DIR}/rack2/rack.toml"
cd "${WORK_DIR}/rack2"
"${ROOT_DIR}/bin/fluxrig" rack -c "rack.toml" > "rack.stdout" 2>&1 &
RACK2_PID=$!

if wait_for_log "${WORK_DIR}/rack2/rack.stdout" "Passport Verified" 8; then
    fail "The second Rack hijacked the name using only the public bootstrap secret."
fi
log_success "Second Rack correctly rejected: the bootstrap secret alone cannot hijack an enrolled name."

banner "SUCCESS: Security regression passed."
