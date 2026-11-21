#!/usr/bin/env bash
# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0
#
# HA chaos testbed: three JetStream nodes in Docker, faults between them, the
# failover tool measuring write unavailability and lost writes per scenario.
#
#   test/ha/testbed/run.sh                    every scenario
#   SCENARIOS="baseline partition" ROUNDS=5 test/ha/testbed/run.sh
#
# Every knob below is an environment variable with the default shown.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../../.." && pwd)"
export HA_NET="${HA_NET:-172.30.99}"

SCENARIOS="${SCENARIOS:-baseline wan lossy harsh spikes partition wan-partition}"
ROUNDS="${ROUNDS:-10}"
SETTLE="${SETTLE:-3s}"
# Timeout of one write. It must exceed a replicated write's round trip on
# the slowest profile: at ~220 ms round trip plus jitter and retransmits, a
# LAN-sized 250 ms times out almost every write and a healthy cluster reads
# as down. It only bounds how long a failed write blocks; acknowledgement
# times are measured exactly.
OP_TIMEOUT="${OP_TIMEOUT:-2s}"
WRITE_INTERVAL="${WRITE_INTERVAL:-5ms}"
RECOVER_TIMEOUT="${RECOVER_TIMEOUT:-90s}"
FORM_TIMEOUT="${FORM_TIMEOUT:-90s}"
# One-way delay per link: 110 ms each way is about a 220 ms round trip, the
# order of a link from southern Latin America (Uruguay) to a US region.
WAN_NETEM="${WAN_NETEM:-delay 110ms 20ms 25% distribution normal}"
# Gilbert-Elliott loss: "gemodel p r" enters the bad state with probability
# p and leaves it with r, dropping every packet while there. Overall loss is
# p/(p+r), in bursts of 1/r packets on average.
# A poor link: long tail, about 1% loss in bursts of about 10 packets.
LOSSY_NETEM="${LOSSY_NETEM:-delay 130ms 30ms distribution pareto loss gemodel 0.1% 10%}"
# A failing link: the same tail, about 9% loss in bursts of about 10 packets.
HARSH_NETEM="${HARSH_NETEM:-delay 130ms 30ms distribution pareto loss gemodel 1% 10%}"
# Spikes: SPIKE_NETEM for SPIKE_LENGTH seconds every SPIKE_PERIOD seconds.
SPIKE_BASE="${SPIKE_BASE:-delay 110ms 20ms}"
SPIKE_NETEM="${SPIKE_NETEM:-delay 600ms 60ms}"
SPIKE_PERIOD="${SPIKE_PERIOD:-10}"
SPIKE_LENGTH="${SPIKE_LENGTH:-2}"
PARTITION_DURATION="${PARTITION_DURATION:-20s}"

RESULTS="${RESULTS:-$HERE/results/$(date +%Y%m%d_%H%M%S)}"
NODES="fluxrig-ha-n1,fluxrig-ha-n2,fluxrig-ha-n3"
URLS="nats://${HA_NET}.11:4222,nats://${HA_NET}.12:4222,nats://${HA_NET}.13:4222"
CHAOS="$HERE/chaos.sh"
BIN="$HERE/.build/ha-node"

compose() { docker compose -f "$HERE/docker-compose.yml" "$@"; }

cleanup() {
	"$CHAOS" spikes stop || true
	compose down -v --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "building the node binary and image..."
mkdir -p "$HERE/.build" "$RESULTS"
(cd "$ROOT" && CGO_ENABLED=0 go build -o "$BIN" ./test/ha/failover)
docker build -q -t fluxrig-ha-node:local "$HERE" >/dev/null

# measure <scenario> <fault-cmd> <heal-cmd> [fault-duration]
measure() {
	local name=$1 fault=$2 heal=$3 duration=${4:-0}
	echo "== $name"
	"$BIN" -cluster "$URLS" -nodes "$NODES" -label "$name" -rounds "$ROUNDS" \
		-fault-cmd "$fault" -heal-cmd "$heal" -fault-duration "$duration" \
		-settle "$SETTLE" -op-timeout "$OP_TIMEOUT" -write-interval "$WRITE_INTERVAL" \
		-recover-timeout "$RECOVER_TIMEOUT" -form-timeout "$FORM_TIMEOUT" | tee "$RESULTS/$name.txt" ||
		echo "scenario $name stopped early (see $RESULTS/$name.txt)" | tee -a "$RESULTS/$name.txt"
}

fresh() {
	compose down -v --remove-orphans >/dev/null 2>&1 || true
	compose up -d >/dev/null
}

for sc in $SCENARIOS; do
	fresh
	case "$sc" in
	baseline)
		measure baseline "docker kill {node}" "docker start {node}" ;;
	wan)
		"$CHAOS" netem all "$WAN_NETEM"
		measure wan "docker kill {node}" "docker start {node} && '$CHAOS' netem {node} '$WAN_NETEM'" ;;
	lossy)
		"$CHAOS" netem all "$LOSSY_NETEM"
		measure lossy "docker kill {node}" "docker start {node} && '$CHAOS' netem {node} '$LOSSY_NETEM'" ;;
	harsh)
		"$CHAOS" netem all "$HARSH_NETEM"
		measure harsh "docker kill {node}" "docker start {node} && '$CHAOS' netem {node} '$HARSH_NETEM'" ;;
	spikes)
		"$CHAOS" spikes start "$SPIKE_BASE" "$SPIKE_NETEM" "$SPIKE_PERIOD" "$SPIKE_LENGTH"
		measure spikes "docker kill {node}" "docker start {node} && '$CHAOS' netem {node} '$SPIKE_BASE'"
		"$CHAOS" spikes stop ;;
	partition)
		measure partition "'$CHAOS' isolate {node}" "'$CHAOS' heal {node}" "$PARTITION_DURATION" ;;
	wan-partition)
		"$CHAOS" netem all "$WAN_NETEM"
		measure wan-partition "'$CHAOS' isolate {node}" "'$CHAOS' heal {node}" "$PARTITION_DURATION" ;;
	*)
		echo "unknown scenario: $sc" >&2; exit 2 ;;
	esac
done

{
	echo "HA chaos testbed, $(date -u +%Y-%m-%dT%H:%MZ), $(hostname)"
	echo "rounds=$ROUNDS op-timeout=$OP_TIMEOUT write-interval=$WRITE_INTERVAL"
	echo "wan=\"$WAN_NETEM\" lossy=\"$LOSSY_NETEM\" harsh=\"$HARSH_NETEM\" spikes=\"$SPIKE_BASE\" -> \"$SPIKE_NETEM\" ${SPIKE_LENGTH}s every ${SPIKE_PERIOD}s partition=$PARTITION_DURATION"
	echo
	for f in "$RESULTS"/*.txt; do
		[ "$(basename "$f")" = summary.txt ] && continue
		echo "== $(basename "$f" .txt)"
		grep "^rounds without recovery" "$f" || true
		grep -A1 "^write unavailability" "$f" | tail -1 || true
		grep "^acknowledged writes" "$f" || true
	done
} | tee "$RESULTS/summary.txt"
