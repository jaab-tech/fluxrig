#!/usr/bin/env bash
# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0
#
# Network faults between the testbed nodes. Every fault applies only to
# traffic from a node to its peers (the cluster routes), never to a client on
# the host: that models Mixers in different sites, each with a local client.
#
#   chaos.sh netem   <node|all> "<netem args>"   shape node -> peers
#   chaos.sh clear   <node|all>                   remove shaping
#   chaos.sh isolate <node>                       drop all traffic to/from peers
#   chaos.sh heal    <node>                       undo isolate
#   chaos.sh spikes start "<base>" "<spike>" <period> <length>
#   chaos.sh spikes stop
#
# netem args are passed to `tc qdisc ... netem` unchanged, for example
#   "delay 30ms 5ms 25% distribution normal"     latency with correlated jitter
#   "delay 30ms 20ms distribution pareto"        heavy-tailed spikes
#   "delay 80ms 20ms loss gemodel 1% 10%"        burst loss (Gilbert-Elliott)
# `spikes` holds <base> and switches every node to <spike> for <length>
# every <period> (seconds), until `spikes stop`.
set -euo pipefail

HA_NET="${HA_NET:-172.30.99}"
NODES=(fluxrig-ha-n1 fluxrig-ha-n2 fluxrig-ha-n3)
declare -A IP=([fluxrig-ha-n1]="${HA_NET}.11" [fluxrig-ha-n2]="${HA_NET}.12" [fluxrig-ha-n3]="${HA_NET}.13")
SPIKE_PID_FILE="${SPIKE_PID_FILE:-/tmp/fluxrig-ha-spikes.pid}"

targets() { if [ "$1" = all ]; then echo "${NODES[@]}"; else echo "$1"; fi; }

peers_of() {
	local self=$1 n
	for n in "${NODES[@]}"; do [ "$n" != "$self" ] && echo "${IP[$n]}"; done
}

# netem sits on band 3 of a prio qdisc; a u32 filter sends only packets for
# a peer to that band. Everything else takes the unshaped bands.
apply_netem() {
	local node=$1 args=$2 peer
	docker exec "$node" tc qdisc del dev eth0 root 2>/dev/null || true
	docker exec "$node" tc qdisc add dev eth0 root handle 1: prio bands 3
	# shellcheck disable=SC2086 # args are netem words, split on purpose
	docker exec "$node" tc qdisc add dev eth0 parent 1:3 handle 30: netem $args
	for peer in $(peers_of "$node"); do
		docker exec "$node" tc filter add dev eth0 parent 1:0 protocol ip prio 1 u32 match ip dst "$peer/32" flowid 1:3
	done
}

change_netem() {
	local node=$1 args=$2
	# shellcheck disable=SC2086
	docker exec "$node" tc qdisc change dev eth0 parent 1:3 handle 30: netem $args
}

cmd=${1:-}; shift || true
case "$cmd" in
netem)
	for n in $(targets "$1"); do apply_netem "$n" "$2"; done ;;
clear)
	for n in $(targets "$1"); do docker exec "$n" tc qdisc del dev eth0 root 2>/dev/null || true; done ;;
isolate)
	for peer in $(peers_of "$1"); do
		docker exec "$1" iptables -A INPUT -s "$peer" -j DROP
		docker exec "$1" iptables -A OUTPUT -d "$peer" -j DROP
	done ;;
heal)
	docker exec "$1" iptables -F INPUT
	docker exec "$1" iptables -F OUTPUT ;;
spikes)
	case "${1:-}" in
	start)
		base=$2 spike=$3 period=$4 length=$5
		for n in "${NODES[@]}"; do apply_netem "$n" "$base"; done
		(
			while true; do
				sleep "$((period - length))"
				for n in "${NODES[@]}"; do change_netem "$n" "$spike" || true; done
				sleep "$length"
				for n in "${NODES[@]}"; do change_netem "$n" "$base" || true; done
			done
		) &
		echo $! >"$SPIKE_PID_FILE" ;;
	stop)
		[ -f "$SPIKE_PID_FILE" ] && kill "$(cat "$SPIKE_PID_FILE")" 2>/dev/null || true
		rm -f "$SPIKE_PID_FILE" ;;
	*) echo "usage: chaos.sh spikes start|stop" >&2; exit 2 ;;
	esac ;;
*)
	sed -n '5,22p' "$0" >&2; exit 2 ;;
esac
