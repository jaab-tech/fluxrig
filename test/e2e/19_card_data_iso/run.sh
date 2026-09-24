#!/bin/bash
# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

set -u

# ==============================================================================
# Card Data in a Real ISO 8583 Flow E2E Test
#
# An ISO 8583 authorization, packed by the terminal tool, crosses from one Rack to
# another over the Mixer's bus, decoded on the way. With the default settings and the
# default log level, the card number must be in no file the Mixer or the Racks wrote:
# not in the message store, not in a log, not in the DuckDB database and not in a
# Parquet file, where the spans and the logs of the run end up.
#
# The DuckDB and Parquet files are compressed or in a database format, so a search of
# their bytes proves nothing. They are read through DuckDB and searched as text.
#
# Needs the duckdb command line tool. CARD_DATA_LOG_LEVEL runs it at another log level.
# ==============================================================================

BASE_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "${BASE_DIR}/../../.." && pwd)"

source "${BASE_DIR}/../utils/e2e_utils.sh"

command -v duckdb >/dev/null 2>&1 || fail "The duckdb command line tool is needed to read the database and the Parquet files."

setup_workspace "card_data_iso" "$BASE_DIR"

banner "Card Data in a Real ISO 8583 Flow E2E Test"

API_URL="http://localhost:9165/api/v1"
PAN=4111111111111111
MESSAGES=20
LOG_LEVEL="${CARD_DATA_LOG_LEVEL:-info}"
STREAM_DIR="$WORK_DIR/mixer/data/snake/jetstream"
SPEC="${ROOT_DIR}/test/robot/suites/payment_switch/specs/auth.yaml"

ENROLL_TIMEOUT=30
STREAM_TIMEOUT=30
TELEMETRY_TIMEOUT=60
STOP_TIMEOUT=30

MIXER_PID=""
A_PID=""
B_PID=""

stop_pid() {
    local pid="$1"
    [ -z "$pid" ] && return 0
    kill "$pid" 2>/dev/null || return 0
    local deadline=$((SECONDS + STOP_TIMEOUT))
    while kill -0 "$pid" 2>/dev/null; do
        (( SECONDS < deadline )) || { kill -9 "$pid" 2>/dev/null; break; }
        sleep 0.2
    done
    wait "$pid" 2>/dev/null || true
}

cleanup() {
    log_info "Shutting down..."
    stop_pid "$A_PID"
    stop_pid "$B_PID"
    stop_pid "$MIXER_PID"
}
trap cleanup EXIT

# 0. Prep
lsof -ti :9165 -ti :4285 -ti :10101 -ti :10102 2>/dev/null | xargs kill -9 2>/dev/null || true

# 1. Mixer, two Racks, one scenario. Only the log level is set, and only to make the
# run at another level possible: the default is "info".
"$(bin_dir)/fluxrig" keys gen-cluster -o "$WORK_DIR/mixer/data/cluster.key" > /dev/null
mkdir -p "$WORK_DIR/rack-a/logs" "$WORK_DIR/rack-b/logs" "$WORK_DIR/mixer/logs"
{ printf '[logging]\nlevel = "%s"\nfilename = "logs/mixer.log"\n\n' "$LOG_LEVEL"; cat "${BASE_DIR}/mixer/mixer.toml"; } > "$WORK_DIR/mixer/mixer.toml"
for r in a b; do
    { printf '[logging]\nlevel = "%s"\nfilename = "logs/rack.log"\n\n' "$LOG_LEVEL"; cat "${BASE_DIR}/rack-$r/rack.toml"; } > "$WORK_DIR/rack-$r/rack.toml"
    mkdir -p "$WORK_DIR/rack-$r/specs"
    cp "$SPEC" "$WORK_DIR/rack-$r/specs/auth.yaml"
done

cd "$WORK_DIR/mixer"
"$(bin_dir)/fluxrig-mixer" -c mixer.toml >> mixer.stdout 2>&1 &
MIXER_PID=$!
cd "${BASE_DIR}"
wait_for_port 9165 30 || fail "Mixer API did not come up."

for r in a b; do
    cd "$WORK_DIR/rack-$r"
    "$(bin_dir)/fluxrig" rack -c rack.toml >> rack.stdout 2>&1 &
    if [ "$r" == "a" ]; then A_PID=$!; else B_PID=$!; fi
    cd "${BASE_DIR}"
done

for r in a b; do
    deadline=$((SECONDS + ENROLL_TIMEOUT))
    until curl -s --max-time 2 "${API_URL}/racks?status=active" | grep -q "rack-$r"; do
        (( SECONDS < deadline )) || fail "rack-$r did not become active."
        sleep 0.5
    done
done
log_success "Both Racks are active."

HTTP_CODE=$(curl -s -o "$WORK_DIR/import.out" -w "%{http_code}" -X POST "${API_URL}/scenario/import?activate=true" \
    -H "Content-Type: application/x-yaml" --data-binary @"${BASE_DIR}/scenario.yaml")
[ "$HTTP_CODE" == "200" ] || { cat "$WORK_DIR/import.out"; fail "Scenario import failed (HTTP $HTTP_CODE)."; }
wait_for_port 10101 30 || fail "The terminal gateway is not listening."
wait_for_port 10102 30 || fail "The sink is not listening."

# 2. Real ISO 8583 authorizations, packed by the terminal tool, with the card number in DE 2.
# Nothing answers them, so the tool gives up on its own: its exit status says nothing here.
log_info "Sending $MESSAGES ISO 8583 authorizations carrying a card number (log level: $LOG_LEVEL)..."
timeout 60 "$(bin_dir)/iso8583-tool" -mode auth -auth-target 127.0.0.1:10101 -encoding ascii -auth-timeout 500ms -auth-reconnect \
    -auth-spec "$WORK_DIR/rack-a/specs/auth.yaml" -auth-pan "$PAN" \
    -auth-count "$MESSAGES" -auth-conns 1 -auth-rate 20 > "$WORK_DIR/tool.stdout" 2>&1 || true

# 3. The evidence has to exist, or the checks below prove nothing: the messages crossed
# the Mixer's bus, the Rack decoded them, and telemetry reached the Mixer's Parquet files.
stream_bytes() {
    find "$STREAM_DIR" -path '*flux-msg*' -name '*.blk' -printf '%s\n' 2>/dev/null | awk '{s+=$1} END{print s+0}'
}
parquet_files() {
    find "$WORK_DIR/mixer" -name '*.parquet' -size +0 2>/dev/null
}
deadline=$((SECONDS + STREAM_TIMEOUT))
until [ "$(stream_bytes)" -gt $((MESSAGES * 100)) ]; do
    (( SECONDS < deadline )) || { cat "$WORK_DIR/tool.stdout"; fail "The Mixer's message stream never received the traffic."; }
    sleep 0.5
done
log_success "The traffic crossed the Mixer's message stream ($(stream_bytes) bytes)."

decoded() {
    grep -ac "Codec message processed" "$WORK_DIR/rack-a/rack.stdout" 2>/dev/null
}
deadline=$((SECONDS + STREAM_TIMEOUT))
until [ "$(decoded)" -gt 0 ]; do
    (( SECONDS < deadline )) || fail "Rack A never decoded an authorization: the ISO path did not run."
    sleep 0.5
done
log_success "Rack A decoded the authorizations."

deadline=$((SECONDS + TELEMETRY_TIMEOUT))
until [ -n "$(parquet_files)" ]; do
    (( SECONDS < deadline )) || fail "No Parquet file reached the Mixer: the telemetry files were not written."
    sleep 1
done
log_success "Telemetry reached the Mixer's Parquet files ($(parquet_files | wc -l) files)."

# 4. Everything stops, so that the database releases its lock and its log is written.
stop_pid "$A_PID"; A_PID=""
stop_pid "$B_PID"; B_PID=""
stop_pid "$MIXER_PID"; MIXER_PID=""

# 5. The card number, as it can be written: digits, and the hex of the digits. Searched in
# every file, then in what DuckDB holds, as text.
PAN_HEX=$(printf '%s' "$PAN" | od -An -tx1 | tr -d ' \n')
PAN_HEX_UPPER=$(printf '%s' "$PAN_HEX" | tr 'a-f' 'A-F')
# A card number in an ISO 8583 message in ASCII is a contiguous run of digits.
PATTERNS=(-e "$PAN" -e "$PAN_HEX" -e "$PAN_HEX_UPPER")

parquet_holds() {
    duckdb -csv -noheader -c "SELECT * FROM read_parquet('$1')" 2>/dev/null | grep -qaF "${PATTERNS[@]}"
}
table_holds() {
    duckdb -readonly "$1" -csv -noheader -c "SELECT * FROM \"$2\"" 2>/dev/null | grep -qaF "${PATTERNS[@]}"
}

# 5a. The detector, first: a search that cannot see a card number finds none anywhere. A
# file written on purpose with the number in it must be seen through the same reading.
CONTROL="$WORK_DIR/control"
mkdir -p "$CONTROL"
duckdb -c "COPY (SELECT '$PAN' AS number) TO '$CONTROL/control.parquet' (FORMAT PARQUET)" > /dev/null 2>&1
parquet_holds "$CONTROL/control.parquet" || fail "The detector does not see a card number in a Parquet file."
duckdb "$CONTROL/control.duckdb" -c "CREATE TABLE t AS SELECT '$PAN' AS number" > /dev/null 2>&1
table_holds "$CONTROL/control.duckdb" t || fail "The detector does not see a card number in a DuckDB table."
log_success "The detector sees a card number in a Parquet file and in a DuckDB table."

# 5b. The bytes of every file the Mixer and the Racks wrote.
LEAKS=$(grep -rlaF "${PATTERNS[@]}" "$WORK_DIR/mixer" "$WORK_DIR/rack-a" "$WORK_DIR/rack-b" 2>/dev/null | sed "s#$WORK_DIR/##")
if [ -z "$LEAKS" ]; then
    log_success "The card number is in the bytes of no file the Mixer or the Racks wrote."
else
    log_error "The card number is readable in the bytes of:"
    echo "$LEAKS"
    LEAKED=1
fi

# 5c. What DuckDB holds.
DB="$WORK_DIR/mixer/data/flux.duckdb"
[ -f "$DB" ] || fail "The Mixer left no DuckDB database."
ROWS=0
TABLES=$(duckdb -readonly "$DB" -noheader -list -c "SELECT table_name FROM information_schema.tables WHERE table_schema='main'" 2>/dev/null)
[ -n "$TABLES" ] || fail "The DuckDB database has no tables to read."
for t in $TABLES; do
    n=$(duckdb -readonly "$DB" -noheader -list -c "SELECT count(*) FROM \"$t\"" 2>/dev/null)
    ROWS=$((ROWS + ${n:-0}))
    if table_holds "$DB" "$t"; then
        log_error "The card number is in the DuckDB table '$t'."
        LEAKED=1
    fi
done
[ "$ROWS" -gt 0 ] || fail "The DuckDB database is empty: there was nothing to search."
log_success "Searched $ROWS DuckDB rows in $(echo "$TABLES" | wc -w) tables."

# 5d. What the Parquet files hold, which is where the logs and the spans of the run are.
PARQUET_ROWS=0
DECODE_EVENTS=0
SPAN_EVENTS=0
while IFS= read -r f; do
    n=$(duckdb -noheader -list -c "SELECT count(*) FROM read_parquet('$f')" 2>/dev/null)
    PARQUET_ROWS=$((PARQUET_ROWS + ${n:-0}))
    if parquet_holds "$f"; then
        log_error "The card number is in the Parquet file ${f#$WORK_DIR/}."
        LEAKED=1
    fi
    case "$(basename "$f")" in
        logs_*)  d=$(duckdb -noheader -list -c "SELECT count(*) FROM read_parquet('$f') WHERE body = 'Codec message processed'" 2>/dev/null)
                 DECODE_EVENTS=$((DECODE_EVENTS + ${d:-0})) ;;
        spans_*) s=$(duckdb -noheader -list -c "SELECT count(*) FROM read_parquet('$f') WHERE name LIKE 'handle_msg%'" 2>/dev/null)
                 SPAN_EVENTS=$((SPAN_EVENTS + ${s:-0})) ;;
    esac
done < <(parquet_files)
[ "$PARQUET_ROWS" -gt 0 ] || fail "The Parquet files are empty: there was nothing to search."
log_success "Searched $PARQUET_ROWS Parquet rows."

# What was searched has to include the events of these messages, or a clean result says
# nothing about them: the decode log of the codec, and the spans of the messages on the bus.
[ "$DECODE_EVENTS" -gt 0 ] || fail "No decode event of the codec is in the Parquet logs: the messages left no trace to search."
[ "$SPAN_EVENTS" -gt 0 ] || fail "No span of a message on the bus is in the Parquet files."
log_success "The search covered $DECODE_EVENTS decode events and $SPAN_EVENTS message spans."

if [ "${LEAKED:-0}" -ne 0 ]; then
    fail "A card number rests in clear."
fi

banner "Card Data in a Real ISO 8583 Flow Verified"
