#!/usr/bin/env bash
# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0
#
# HA product testbed: three stock NATS servers in a cluster, three Mixers on it
# in external mode with the write lease on, and two Racks that know every NATS
# server. It measures the product, not the mechanism: control-write
# availability across a lease handover, a Mixer cut off from NATS, the loss of
# a NATS server, and that the traffic between Racks and their durable events
# keep flowing.
#
#   test/ha/mixers/run.sh
#   SCENARIOS="cut-holder" test/ha/mixers/run.sh
#   PROFILE=latam test/ha/mixers/run.sh     # cross-site latency on every scenario
#   SECURE=0 test/ha/mixers/run.sh          # plaintext NATS, no client certificates
#   TELEMETRY=s3 SCENARIOS=telemetry test/ha/mixers/run.sh  # or nats, otlp: the shared telemetry store
#   SCENARIOS="leader-cost" PROFILE=latam test/ha/mixers/run.sh
#   SCENARIOS="kill-nats-idle" test/ha/mixers/run.sh  # a stream leader no Rack is connected to
#   PREV_REF=<ref> SCENARIOS="rolling" test/ha/mixers/run.sh  # upgrade one Mixer at a time
#
# SECURE=1, the default, runs NATS as the product expects: TLS with a client
# certificate for every Mixer and Rack (verify_and_map), and for each Rack the
# permissions that `fluxrig scenario permissions` writes for the scenarios this
# script activates. The routes between the NATS servers stay in plaintext: they
# belong to the operator of NATS.
#
# Each scenario asserts what it claims and the script exits non-zero when any
# assertion fails. leader-cost is a measurement and only records.
#
# Each Mixer publishes its API on the host (m1 :8091, m2 :8092, m3 :8093) and
# each NATS server its client port (n1 :4223, n2 :4224, n3 :4225).
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../../.." && pwd)"
export HA_NET="${HA_NET:-172.31.77}"
export GOMODCACHE="${GOMODCACHE:-$(go env GOMODCACHE)}"

SECURE="${SECURE:-1}"
# TELEMETRY is observability.storage.type of the Mixers: local, s3, nats or
# otlp. The telemetry scenario needs one of the last three.
TELEMETRY="${TELEMETRY:-local}"
# RACK_LOG is the log level of the Racks: debug shows each sync probe.
RACK_LOG="${RACK_LOG:-info}"
# MIXER_LOG is the log level of the Mixers: debug shows each telemetry fetch.
MIXER_LOG="${MIXER_LOG:-info}"
# The telemetry scenario: how long the flow runs before and after the lease
# holder is killed, and how long the store may take to hold every line.
TELEMETRY_BEFORE="${TELEMETRY_BEFORE:-20}"
TELEMETRY_AFTER="${TELEMETRY_AFTER:-40}"
TELEMETRY_WAIT="${TELEMETRY_WAIT:-240}"
DEFAULT_SCENARIOS="up kill-holder stop-holder cut-holder kill-nats cut-rack events-failover wasm-catalog"
[ "$SECURE" = 1 ] && DEFAULT_SCENARIOS="$DEFAULT_SCENARIOS probe"
SCENARIOS="${SCENARIOS:-$DEFAULT_SCENARIOS}"
HOLDER_WAIT="${HOLDER_WAIT:-120}" # seconds to wait for a holder after a fault
RESULTS="${RESULTS:-$HERE/results/$(date +%Y%m%d_%H%M%S)}"
NATS_NODES="fluxrig-ha-n1 fluxrig-ha-n2 fluxrig-ha-n3"
MIXERS="fluxrig-ha-m1 fluxrig-ha-m2 fluxrig-ha-m3"
RACKS="fluxrig-ha-r1 fluxrig-ha-r2"
API_WAIT="${API_WAIT:-120}" # seconds to wait for a Mixer API to answer
# Seconds to wait for every Rack to read online again after a fault: the
# Mixer liveness TTL (90s) plus one Rack heartbeat interval (30s), plus margin.
RACKS_WAIT="${RACKS_WAIT:-150}"
# Seconds a control write, or the traffic between the Racks, may take to work
# again after a fault starts or ends. Either one can start a JetStream
# election (stream leader or metadata), and a write fails until it ends.
WRITE_WAIT="${WRITE_WAIT:-60}"
# LATE_WAIT is how long a missing message of the traffic between the Racks
# may take to arrive before it counts as lost: a consumer redelivers a
# message it sent through a server that died after snake.ack_wait (10s by
# default), and a slow link adds its round trips.
LATE_WAIT="${LATE_WAIT:-30}"
# Seconds a cut is held, counted from when it starts. Longer than the Mixer
# liveness TTL (90s), so a Rack whose heartbeats stop reaching every Mixer
# reads offline before the heal.
CUT_HOLD="${CUT_HOLD:-120}"
# The events scenario: how long terminals keep connecting, when the NATS
# leader of the event stream is killed and when it starts again (seconds from
# the first connection), and how long a terminal waits for its logon.
LOGON_RUN="${LOGON_RUN:-90}"
LOGON_KILL_AT="${LOGON_KILL_AT:-20}"
LOGON_START_AT="${LOGON_START_AT:-50}"
LOGON_TIMEOUT="${LOGON_TIMEOUT:-30s}"
# PROFILE=latam shapes the link between the two sites on every scenario, from
# the start: the southern Latin America profile of the plan, about 220 ms round
# trip with jitter. clean leaves the network as it is.
PROFILE="${PROFILE:-clean}"
case "$PROFILE" in
clean) ;;
latam) CROSS_SITE_NETEM="${CROSS_SITE_NETEM:-delay 110ms 20ms distribution normal}" ;;
*) echo "unknown PROFILE: $PROFILE (clean or latam)" >&2; exit 2 ;;
esac
SITE_A="fluxrig-ha-n1 fluxrig-ha-n2 fluxrig-ha-m1 fluxrig-ha-m2 fluxrig-ha-r1 fluxrig-ha-s3 fluxrig-ha-otlp"
SITE_B="fluxrig-ha-n3 fluxrig-ha-m3 fluxrig-ha-r2"
# PREV_REF is the version the rolling scenario upgrades from. It needs the
# external mode, so there is no default before a release that has it.
PREV_REF="${PREV_REF:-}"
SITE="$HERE/.build/site"
PKI="$SITE/tls"
# SCENARIO names the scenario running, for the ledger of acknowledged writes.
SCENARIO=""

if [ "$SECURE" = 1 ]; then
	SCHEME=tls
	TOOL_CONN=(-ca "$PKI/ca.crt" -cert "$PKI/admin.crt" -key "$PKI/admin.key")
else
	SCHEME=nats
	TOOL_CONN=()
fi
NATS_URL="$SCHEME://127.0.0.1:4223,$SCHEME://127.0.0.1:4224,$SCHEME://127.0.0.1:4225"
NATS_INSIDE="$SCHEME://$HA_NET.31:4222,$SCHEME://$HA_NET.32:4222,$SCHEME://$HA_NET.33:4222"

# api_of prints the host URL of a Mixer container's API.
api_of() {
	case "$1" in
	fluxrig-ha-m1) echo "http://127.0.0.1:8091/api/v1" ;;
	fluxrig-ha-m2) echo "http://127.0.0.1:8092/api/v1" ;;
	fluxrig-ha-m3) echo "http://127.0.0.1:8093/api/v1" ;;
	*) return 1 ;;
	esac
}
compose() { docker compose -f "$HERE/docker-compose.yml" "$@"; }
cleanup() { compose down -v --remove-orphans >/dev/null 2>&1 || true; }
trap cleanup EXIT

# tool runs one of the testbed tools, built once. Not through go run, which
# turns every non-zero exit into 1, and a tool's exit code says what failed.
tool() {
	local name=$1 bin="$HERE/.build/tools/$1"
	shift
	[ -x "$bin" ] || (cd "$ROOT" && go build -o "$bin" "./test/ha/mixers/tools/$name")
	"$bin" "$@"
}

echo "building the binaries and image..."
mkdir -p "$HERE/.build" "$RESULTS"
rm -rf "$HERE/.build/tools"
PROBE_TEMPLATE="$RESULTS/ha_probe.yaml"
cat >"$PROBE_TEMPLATE" <<'YAML'
meta:
  name: "ha_probe_NNN"
  version: "1.0.0"
racks:
  - name: "rack-r1"
    labels: { tier: "edge" }
gears:
  - name: "probe-gw"
    type: "io_tcp"
    deploy: "rack-r1"
    config: { mode: "server", bind: ":9001", delimiter: "\n", delimiter_append: true }
wires:
  - from: "probe-gw.out"
    to: "probe-gw.in"
YAML
# The data-plane flow: rack-r1 emits one numbered message every
# DATAPLANE_INTERVAL on the guaranteed lane, and rack-r2 prints each one. The
# wire crosses the bus and the two sites, so it measures what a fault costs the
# traffic between two Racks (tools/dataplane reads rack-r2's log).
DATAPLANE_INTERVAL="${DATAPLANE_INTERVAL:-200ms}"
DATAPLANE="$RESULTS/ha_dataplane.yaml"
cat >"$DATAPLANE" <<YAML
meta:
  name: "ha_dataplane"
  version: "1.0.0"
racks:
  - name: "rack-r1"
  - name: "rack-r2"
gears:
  - name: "dp-sender"
    type: "bento"
    deploy: "rack-r1"
    config:
      ports: { outputs: ["out"] }
      bento:
        input:
          generate:
            interval: "$DATAPLANE_INTERVAL"
            mapping: |
              root.seq = count("ha_dataplane")
              root.sent = now()
        pipeline:
          processors:
            # One numbered line of telemetry per message, which the
            # telemetry scenario counts in the store.
            - log:
                level: INFO
                message: 'ha-seq \${! this.seq }'
  - name: "dp-receiver"
    type: "bento"
    deploy: "rack-r2"
    config:
      ports: { inputs: ["in"] }
      bento:
        output:
          stdout: {}
wires:
  - from: "dp-sender.out"
    to: "dp-receiver.in"
    lane: "guaranteed"
YAML
# The events flow: rack-r1 accepts ISO 8583 terminals, and sends each one a
# network management message when it connects. The message comes from a
# reader of the server's durable connection events, so a terminal that gets
# it proves the event went through the Rack's outbox, the event stream and the
# reader's consumer.
EVENTS="$RESULTS/ha_events.yaml"
cat >"$EVENTS" <<'YAML'
meta:
  name: "ha_events"
  version: "1.0.0"
racks:
  - name: "rack-r1"
gears:
  - name: "server_io"
    type: "io_iso8583"
    deploy: "rack-r1"
    config:
      mode: "server"
      bind: ":8888"
      frame_length_size: 2
      frame_length_endian: "big"
      encoding: "ascii"
      strict_connection_routing: true
      idle_timeout: "5m"
  - name: "on_connect"
    type: "bento"
    deploy: "rack-r1"
    config:
      ports:
        outputs: ["out"]
        events:
          - gear: "server_io"
            on: ["opened"]
      bento:
        pipeline:
          processors:
            - mapping: |
                meta "iso8583.mti" = "0800"
                root = {"iso8583": {"field": {
                  "7": now().ts_format("0102150405", "UTC"),
                  "11": "%06d".format(counter(min: 1, max: 999999)),
                  "70": "001"
                }}}
  - name: "encode"
    type: "codec_iso8583"
    deploy: "rack-r1"
    config:
      spec_path: "/specs/iso8583-v87-ascii.yaml"
      direction: "encode"
      on_error: "reject"
wires:
  - from: "on_connect.out"
    to: "encode.in"
  - from: "encode.out"
    to: "server_io.in"
YAML
# One cluster key for every Mixer, written fresh for each run. It is a test
# key, so it is made readable by the container user.
rm -f "$HERE/.build/cluster.key"
tool clusterkey -out "$HERE/.build/cluster.key"
chmod 0644 "$HERE/.build/cluster.key"
(cd "$ROOT" && CGO_ENABLED=1 go build -o "$HERE/.build/fluxrig" ./cmd/fluxrig)
(cd "$ROOT" && CGO_ENABLED=1 go build -o "$HERE/.build/fluxrig-mixer" ./cmd/fluxrig-mixer)
(cd "$ROOT" && go build -o "$HERE/.build/nats-server" github.com/nats-io/nats-server/v2)
(cd "$ROOT" && go build -o "$HERE/.build/s3server" ./test/ha/mixers/tools/s3server)
(cd "$ROOT" && go build -o "$HERE/.build/otlpreceiver" ./test/ha/mixers/tools/otlpreceiver)
docker build -q -t fluxrig-ha-mixer:local "$HERE" >/dev/null

# gen_ca writes a self-signed authority. gen_cert signs a certificate with it,
# usable by a server and by a client, with the subject /O=fluxrig/CN=<name> and
# the addresses after the name.
gen_ca() {
	openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes \
		-keyout "$PKI/ca.key" -out "$PKI/ca.crt" -days 2 -subj "/O=fluxrig/CN=fluxrig-ha-test-ca" 2>/dev/null
}
gen_cert() {
	local name=$1 san="DNS:$1"
	shift
	for ip in "$@"; do san="$san,IP:$ip"; done
	openssl req -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes \
		-keyout "$PKI/$name.key" -out "$PKI/$name.csr" -subj "/O=fluxrig/CN=$name" 2>/dev/null
	printf 'subjectAltName=%s\nextendedKeyUsage=serverAuth,clientAuth\n' "$san" >"$PKI/$name.ext"
	openssl x509 -req -in "$PKI/$name.csr" -CA "$PKI/ca.crt" -CAkey "$PKI/ca.key" -CAcreateserial \
		-days 2 -extfile "$PKI/$name.ext" -out "$PKI/$name.crt" 2>/dev/null
	rm -f "$PKI/$name.csr" "$PKI/$name.ext"
}

# render_site writes the config of every node into the directory the containers
# mount: the NATS servers with their users, the Mixers and the Racks.
render_site() {
	rm -rf "$SITE" && mkdir -p "$PKI"
	local tls_mixer="" tls_rack="" users="" i
	if [ "$SECURE" = 1 ]; then
		gen_ca
		for i in 1 2 3; do gen_cert "n$i" "$HA_NET.3$i" 127.0.0.1; done
		for n in mixer-m1 mixer-m2 mixer-m3 rack-r1 rack-r2 admin; do gen_cert "$n"; done
		chmod 0644 "$PKI"/*
		# The Racks get what the scenarios this script activates need, and
		# nothing else. The Mixers and the testbed's own tools are unrestricted.
		users=$("$HERE/.build/fluxrig" scenario permissions "$DATAPLANE" "$EVENTS" \
			--rack rack-r1 --rack rack-r2 --user 'CN={rack},O=fluxrig' | grep -v '^#')
		users="  { user: \"CN=mixer-m1,O=fluxrig\" }
  { user: \"CN=mixer-m2,O=fluxrig\" }
  { user: \"CN=mixer-m3,O=fluxrig\" }
  { user: \"CN=admin,O=fluxrig\" }
$users"
		printf '%s\n' "$users" >"$RESULTS/nats_users.conf"
	fi
	for i in 1 2 3; do
		local tag="site:a"
		[ "$i" = 3 ] && tag="site:b"
		{
			echo "server_name: n$i"
			echo "server_tags: [\"$tag\"]"
			echo "port: 4222"
			# The server's pings to its clients: a client that died, or was cut
			# off, is dropped after them, and with it its share of a queue
			# group. At the NATS defaults (2m, 2) a cut-off Mixer kept getting
			# heartbeats for minutes. An operator's servers need the same.
			echo "ping_interval: \"10s\""
			echo "ping_max: 3"
			echo "jetstream { store_dir: \"/data/js\" }"
			# A server that dies without closing its routes (a crash, a cut
			# link) is noticed only when its pings run out: at the NATS
			# defaults (2m, 2 pings) that took over a minute across the
			# latam link, and the streams it led stalled meanwhile. An
			# operator's cluster needs the same.
			echo "cluster { name: fluxrig-ha, port: 6222, ping_interval: \"10s\", ping_max: 3, routes: [nats://$HA_NET.31:6222, nats://$HA_NET.32:6222, nats://$HA_NET.33:6222] }"
			if [ "$SECURE" = 1 ]; then
				echo "tls { cert_file: \"/etc/fluxrig/tls/n$i.crt\", key_file: \"/etc/fluxrig/tls/n$i.key\", ca_file: \"/etc/fluxrig/tls/ca.crt\", verify_and_map: true }"
				echo "authorization { users: ["
				printf '%s\n' "$users"
				echo "] }"
			fi
		} >"$SITE/n$i.conf"
	done
	local storage=""
	case "$TELEMETRY" in
	local) ;;
	s3) printf 'ha\n' >"$SITE/s3.key"
		storage="[observability.storage]
type = \"s3\"
batch_interval = \"5s\"
[observability.storage.s3]
endpoint = \"$HA_NET.40:9000\"
bucket = \"fluxrig-telemetry\"
region = \"us-east-1\"
plaintext = true
access_key_file = \"/etc/fluxrig/s3.key\"
secret_key_file = \"/etc/fluxrig/s3.key\"" ;;
	nats) storage="[observability.storage]
type = \"nats\"
batch_interval = \"5s\"" ;;
	otlp) storage="[observability.storage]
type = \"otlp\"
batch_interval = \"5s\"
[observability.storage.otlp]
endpoint = \"http://$HA_NET.41:4318\"" ;;
	*) echo "unknown TELEMETRY: $TELEMETRY (local, s3, nats or otlp)" >&2; exit 2 ;;
	esac
	for i in 1 2 3; do
		[ "$SECURE" = 1 ] && tls_mixer="root_ca_file = \"/etc/fluxrig/tls/ca.crt\"
client_cert_file = \"/etc/fluxrig/tls/mixer-m$i.crt\"
client_key_file = \"/etc/fluxrig/tls/mixer-m$i.key\""
		cat >"$SITE/m$i.toml" <<TOML
[base]
name = "mixer-m$i"

[logging]
level = "$MIXER_LOG"

[mixer]
queue_group = "fluxrig-mixers"
follow_interval = "5s"
forward_timeout = "5s"

[api]
port = 8090
auth_disabled_dangerously = true

[snake]
external = true
url = "$NATS_INSIDE"
replicas = 3
form_timeout = "2m"
$tls_mixer

[store]
dir = "/data"
cluster_key_file = "cluster.key"

[registry]
lease_enabled = true
lease_ttl = "30s"
lease_renew = "10s"
replicas = 3

[enrollment]
auto_adopt = true

$storage
TOML
	done
	for i in 1 2; do
		[ "$SECURE" = 1 ] && tls_rack="root_ca_file = \"/etc/fluxrig/tls/ca.crt\"
client_cert_file = \"/etc/fluxrig/tls/rack-r$i.crt\"
client_key_file = \"/etc/fluxrig/tls/rack-r$i.key\""
		cat >"$SITE/r$i.toml" <<TOML
[logging]
level = "$RACK_LOG"

[base]
name = "rack-r$i"
state_file = "rack.flux"

[snake]
external = true
url = "$NATS_INSIDE"
$tls_rack

[store]
dir = "/data"

[rack]
bootstrap_secret = "fluxrig"
TOML
	done
}
render_site
echo "profile $PROFILE, NATS $([ "$SECURE" = 1 ] && echo "with TLS, client certificates and Rack permissions" || echo "in plaintext"), telemetry $TELEMETRY"

# cross_site_netem shapes the traffic between the two sites, both ways. The
# arguments are an environment knob.
cross_site_netem() {
	local args="${CROSS_SITE_NETEM:-delay 60ms 10ms distribution normal}" c from to shaped=0
	for from in a b; do
		local here="$SITE_A" there="$SITE_B"
		[ "$from" = b ] && { here="$SITE_B"; there="$SITE_A"; }
		for c in $here; do
			# A container a scenario stopped or killed is shaped when it
			# starts again, by the next call.
			running "$c" || continue
			docker exec "$c" tc qdisc del dev eth0 root 2>/dev/null || true
			docker exec "$c" tc qdisc add dev eth0 root handle 1: prio bands 3 || true
			docker exec "$c" tc qdisc add dev eth0 parent 1:3 handle 30: netem $args || true
			for to in $there; do
				docker exec "$c" tc filter add dev eth0 parent 1:0 protocol ip prio 1 u32 \
					match ip dst "$(addr_of "$to")/32" flowid 1:3 || true
			done
		done
	done
	# Every tc above tolerates "already there"; this is the check that the
	# shaping really is in place. A kernel without sch_netem (a microVM, some
	# CI hosts) would otherwise run the latency profile on a clean network.
	for c in $SITE_A $SITE_B; do
		running "$c" || continue
		if ! docker exec "$c" tc qdisc show dev eth0 | grep -q netem; then
			echo "cross_site_netem: netem is not in place on $c; this kernel may lack sch_netem. Run PROFILE=clean, or on a host with netem." >&2
			exit 2
		fi
		shaped=$((shaped + 1))
	done
	echo "cross-site netem applied to $shaped containers: $args"
}

# fault prints a line about a fault with the time it starts, so the run's log
# lines up with the containers' logs.
fault() { echo "$(date -u +%Y-%m-%dT%H:%M:%S.%3NZ) $*"; }

# running reports whether a container is running.
running() { [ "$(docker inspect -f '{{.State.Running}}' "$1" 2>/dev/null)" = true ]; }

# addr_of prints the fixed address of a testbed container (docker-compose.yml),
# whether or not it runs.
addr_of() {
	case "$1" in
	fluxrig-ha-m1) echo "$HA_NET.11" ;; fluxrig-ha-m2) echo "$HA_NET.12" ;; fluxrig-ha-m3) echo "$HA_NET.13" ;;
	fluxrig-ha-r1) echo "$HA_NET.21" ;; fluxrig-ha-r2) echo "$HA_NET.22" ;;
	fluxrig-ha-n1) echo "$HA_NET.31" ;; fluxrig-ha-n2) echo "$HA_NET.32" ;; fluxrig-ha-n3) echo "$HA_NET.33" ;;
	fluxrig-ha-s3) echo "$HA_NET.40" ;; fluxrig-ha-otlp) echo "$HA_NET.41" ;;
	*) echo "addr_of: no address for $1" >&2; return 1 ;;
	esac
}

FAILURES=0

# fail records a failed assertion; the script exits non-zero at the end.
fail() {
	echo "FAIL: $*" | tee -a "$RESULTS/failures.txt"
	FAILURES=$((FAILURES + 1))
}

# probe_write imports a scenario under a new name each time, so every call is
# a real control write: on a Mixer that does not hold the lease it goes
# through the forwarder to the holder. It prints the HTTP status.
# The name comes from the clock, not a counter: measure runs in a pipeline
# subshell, where a counter would reset on every call.
probe_write() {
	local seq f code
	seq="$(date +%s%N)"
	f="$RESULTS/ha_probe_$seq.yaml"
	sed "s/NNN/$seq/" "$PROBE_TEMPLATE" >"$f"
	# The body is kept next to the scenario file, so a refusal shows its reason.
	code=$(curl -s -m 30 -o "$f.answer" -w "%{http_code}" -X POST "$API/scenario/import" \
		-H 'Content-Type: application/yaml' --data-binary @"$f" || true)
	# Every acknowledged write goes into the scenario's ledger: the cluster
	# must still hold each one at the end (expect_no_lost_writes).
	case "$code" in 2??) echo "ha_probe_$seq" >>"$RESULTS/$SCENARIO.acked" ;; esac
	printf '%s' "$code"
}

# activate imports and activates the scenario file $1 through $API.
activate() {
	local code
	code=$(curl -s -m 30 -o "$RESULTS/$SCENARIO.activate.answer" -w "%{http_code}" -X POST "$API/scenario/import?activate=true" \
		-H 'Content-Type: application/yaml' --data-binary @"$1" || true)
	case "$code" in 2??) return 0 ;; esac
	fail "$SCENARIO: $(basename "$1") was not activated ($code: $(head -c 300 "$RESULTS/$SCENARIO.activate.answer"))"
	return 1
}

# start_dataplane activates the data-plane flow and waits until rack-r2
# receives it. Racks must be online first: an activation that names an
# unknown Rack is refused.
start_dataplane() {
	local start=$SECONDS
	activate "$DATAPLANE" || return 1
	local log
	while ((SECONDS - start < WRITE_WAIT)); do
		# The log is read whole first: grep -q leaves at the first match, and
		# with pipefail a large log then fails the pipe through SIGPIPE.
		log=$(docker logs fluxrig-ha-r2 2>&1)
		grep -q '"seq"' <<<"$log" && { echo "$SCENARIO: data-plane flow running $((SECONDS - start))s after activation"; return 0; }
		sleep 2 # poll interval
	done
	fail "$SCENARIO: rack-r2 received no data-plane message within ${WRITE_WAIT}s of activation"
	dump_racks
	return 1
}

# now prints the time to the nanosecond, for expect_dataplane.
now() { date -u +%Y-%m-%dT%H:%M:%S.%NZ; }

# DATAPLANE_MIN is how many messages must arrive after a fault for the flow to
# count as running again: two seconds of it at the default interval.
DATAPLANE_MIN="${DATAPLANE_MIN:-10}"

# expect_dataplane records the flow between the two Racks over the scenario
# and asserts that it runs again after $1 (an RFC 3339 time, the end of the
# fault), allowing WRITE_WAIT seconds for it, and that no message was lost.
expect_dataplane() {
	local since=$1 start=$SECONDS out gap_since=
	while :; do
		local rc=0
		out=$(docker logs --timestamps fluxrig-ha-r2 2>&1 | tool dataplane -after "$since" -min-after "$DATAPLANE_MIN" 2>&1) || rc=$?
		if [ "$rc" = 0 ]; then
			echo "$SCENARIO: $out" | tee -a "$RESULTS/dataplane.txt"
			[ -z "$gap_since" ] || echo "$SCENARIO: the missing messages arrived late, up to $((SECONDS - gap_since))s after the gap was seen" | tee -a "$RESULTS/dataplane.txt"
			return
		fi
		if [ "$rc" = 3 ]; then
			# A message the consumer delivered through the server that died
			# comes again after the ack wait: missing now is not lost yet.
			if [ -z "$gap_since" ]; then
				gap_since=$SECONDS
				echo "$SCENARIO: a gap, waiting up to ${LATE_WAIT}s for a redelivery: $out" | tee -a "$RESULTS/dataplane.txt"
			fi
			if ((SECONDS - gap_since >= LATE_WAIT)); then
				echo "$SCENARIO: $out" | tee -a "$RESULTS/dataplane.txt"
				# What the stream holds tells a message that never reached it
				# from one a consumer skipped.
				tool streamdump -url "$NATS_URL" "${TOOL_CONN[@]}" -subject flux.msg.rack-r1.dp-sender.out \
					>"$RESULTS/$SCENARIO.stream.txt" 2>&1 || true
				fail "$SCENARIO: the guaranteed lane lost messages: $out (stream in $SCENARIO.stream.txt)"
				return
			fi
			sleep 3 # poll interval
			continue
		fi
		if ((SECONDS - start >= WRITE_WAIT)); then
			fail "$SCENARIO: the data-plane flow did not recover after the fault: $out"
			dump_racks
			return
		fi
		sleep 3 # poll interval
	done
}

# dump_racks makes every Rack print the stack of each goroutine to its log
# and exit (SIGQUIT), so a flow that stopped shows where it stopped. The
# scenario is over by then: save_logs keeps the dump.
dump_racks() {
	local r
	for r in $RACKS; do docker kill -s QUIT "$r" >/dev/null 2>&1 || true; done
	sleep 2 # the dump reaches the log
}

# expect_lease_visible asserts that every Mixer's /health names the holder
# $1 (a machine_id), and that the Mixer metrics in the telemetry store show
# exactly one Mixer holding the lease, out of three.
expect_lease_visible() {
	local want=$1 c got start=$SECONDS out t0
	for c in $MIXERS; do
		# A Mixer reads the lease for /health within mixer.forward_timeout:
		# across a slow link one read can miss, so the check polls.
		t0=$SECONDS
		while :; do
			got=$({ curl -s -m 10 "$(api_of "$c")/health" || true; } | python3 -c 'import json,sys; print((json.load(sys.stdin).get("lease") or {}).get("holder",""))' 2>/dev/null || true)
			[ "$got" = "$want" ] && break
			if ((SECONDS - t0 >= WRITE_WAIT)); then
				fail "$SCENARIO: $c /health names lease holder '$got', want $want, for ${WRITE_WAIT}s"
				break
			fi
			sleep 2 # poll interval
		done
		((SECONDS - t0 > 0)) && echo "$SCENARIO: $c /health named the holder after $((SECONDS - t0))s"
	done
	# With otlp the Mixers keep no telemetry: the API answers 501 to a query,
	# and the metrics are at the collector.
	if [ "$TELEMETRY" = otlp ]; then
		echo "$SCENARIO: the lease metric is at the OTLP collector, not checked through the API"
		return
	fi
	# The metrics reach the store one telemetry batch after they are taken.
	while :; do
		out=$({ curl -s -m 5 "$API/telemetry/metrics?name=flux.mixer.lease.held&since=10m&limit=1000" || true; } | python3 -c '
import json, sys
latest = {}
for p in sorted(json.load(sys.stdin) or [], key=lambda p: str(p.get("timestamp", ""))):
    # flux.id is unique per Mixer; a name can be shared.
    latest[(p.get("attributes") or {}).get("flux.id") or p.get("entity_name")] = p.get("value")
holders = [e for e, v in latest.items() if v == 1]
print(len(latest), len(holders), " ".join(sorted(holders)))
' 2>/dev/null || true)
		set -- $out
		if [ "${1:-0}" = 3 ] && [ "${2:-0}" = 1 ]; then
			echo "$SCENARIO: flux.mixer.lease.held reported by 3 Mixers, held by ${3}"
			return
		fi
		if ((SECONDS - start >= WRITE_WAIT)); then
			fail "$SCENARIO: flux.mixer.lease.held not reported as one holder out of three Mixers (got: ${out:-nothing})"
			return
		fi
		sleep 3 # poll interval
	done
}

# expect_no_lost_writes asserts that every scenario import the API
# acknowledged during this scenario is stored in the registry, and with
# $2 set, that the registry has that many replicas. A read can be served by a
# replica that is behind, so it retries until WRITE_WAIT ends.
expect_no_lost_writes() {
	local what=$1 replicas=${2:-0} ledger="$RESULTS/$SCENARIO.acked" start=$SECONDS out
	[ -s "$ledger" ] || { fail "$what: no acknowledged control write to check"; return; }
	while :; do
		if out=$(tool scenariocheck -url "$NATS_URL" "${TOOL_CONN[@]}" -replicas "$replicas" <"$ledger" 2>&1); then
			echo "$what: $out"
			return
		fi
		if ((SECONDS - start >= WRITE_WAIT)); then
			fail "$what: acknowledged control writes not found in the registry: $out"
			return
		fi
		sleep 3 # poll interval
	done
}

# time_until_write polls control writes through $API until one succeeds, and
# prints the seconds since $1 (a SECONDS value): how long control writes were
# unavailable. It fails the scenario after $2 seconds.
time_until_write() {
	local since=$1 budget=$2 code
	while :; do
		code=$(probe_write)
		case "$code" in 2??) echo $((SECONDS - since)); return 0 ;; esac
		((SECONDS - since >= budget)) && return 1
		sleep 1 # poll interval
	done
}

# wait_api polls an API until /health answers 200, or the deadline passes.
# The lease can have a holder before a Mixer's API listens.
wait_api() {
	local api=$1 deadline=$((SECONDS + API_WAIT))
	while ((SECONDS < deadline)); do
		[ "$(curl -s -m 3 -o /dev/null -w "%{http_code}" "$api/health" || true)" = 200 ] && return 0
		sleep 2 # poll interval
	done
	return 1
}

# survivor prints the API of a Mixer other than the ones named, so a write
# through it is forwarded to the holder.
survivor() {
	local skip=" $* " c
	for c in $MIXERS; do
		case "$skip" in *" $c "*) continue ;; esac
		api_of "$c"
		return 0
	done
	return 1
}

# racks_online prints how many Racks the API reports online. An answer other
# than 200 prints nothing, so it never reads as "0 online".
racks_online() {
	local body code
	body=$(curl -s -m 5 -w "\n%{http_code}" "$API/racks" || true)
	code=${body##*$'\n'}
	[ "$code" = 200 ] || return 1
	printf '%s' "${body%$'\n'*}" | grep -o '"online":true' | wc -l | tr -d ' '
}

# wait_racks_online polls until every Rack reads online, or RACKS_WAIT ends.
# It prints the seconds it took.
wait_racks_online() {
	local want start=$SECONDS n
	want=$(echo "$RACKS" | wc -w | tr -d ' ')
	while ((SECONDS - start < RACKS_WAIT)); do
		n=$(racks_online || true)
		[ "$n" = "$want" ] && { echo $((SECONDS - start)); return 0; }
		sleep 3 # poll interval
	done
	return 1
}

# peers_of prints the NATS servers a container holds a client connection to,
# from its /proc/net/tcp: established (st 01) connections to port 4222 (hex
# 107E).
peers_of() {
	local rem st h peers=""
	while read -r _ _ rem st _; do
		[ "$st" = 01 ] || continue
		case "$rem" in *:107E) ;; *) continue ;; esac
		h=${rem%%:*}
		peers="$peers$((16#${h:6:2})).$((16#${h:4:2})).$((16#${h:2:2})).$((16#${h:0:2})) "
	done < <(docker exec "$1" cat /proc/net/tcp 2>/dev/null | tail -n +2)
	echo "${peers:-none}"
}

# rack_peers prints the NATS server each Rack holds its connections to.
rack_peers() {
	local r
	for r in $RACKS; do echo "$r connected to: $(peers_of "$r")"; done
}

# ip_of prints a container's address on the testbed network, running or not.
ip_of() { addr_of "$1"; }

# nats_of_ip prints the NATS container that has the address $1.
nats_of_ip() {
	local c
	for c in $NATS_NODES; do
		[ "$(ip_of "$c")" = "$1" ] && { echo "$c"; return 0; }
	done
	return 1
}

# nats_ips prints the addresses of the NATS servers.
nats_ips() {
	local c
	for c in $NATS_NODES; do ip_of "$c"; done
}

# cut_off drops every packet between the container $1 and the addresses after
# it. heal lifts it.
cut_off() {
	local c=$1 ip
	shift
	for ip in "$@"; do
		docker exec "$c" iptables -A INPUT -s "$ip" -j DROP || true
		docker exec "$c" iptables -A OUTPUT -d "$ip" -j DROP || true
	done
}
heal() {
	docker exec "$1" iptables -F INPUT || true
	docker exec "$1" iptables -F OUTPUT || true
}

# leader_of prints the NATS container that leads the stream $1.
leader_of() {
	local name
	name=$(tool jsleader -url "$NATS_URL" "${TOOL_CONN[@]}" 2>/dev/null | python3 -c "import json,sys; print(json.load(sys.stdin).get('$1',''))" 2>/dev/null || true)
	[ -n "$name" ] && echo "fluxrig-ha-$name"
}

# save_logs keeps every container's log for one scenario, before the next
# scenario's fresh start removes the containers.
save_logs() {
	local c
	for c in $NATS_NODES $MIXERS $RACKS; do
		docker logs --timestamps "$c" >"$RESULTS/$1.$c.log" 2>&1 || true
	done
	# A Mixer's own log goes to logging.filename (logs/mixer.log under its
	# working directory), not to stdout: enrollment, heartbeats and the lease
	# are only there.
	for c in $MIXERS; do
		docker cp "$c:/logs/mixer.log" "$RESULTS/$1.$c.mixer.log" >/dev/null 2>&1 || true
	done
}

# expect_write_recovers asserts that a control write succeeds again within
# WRITE_WAIT seconds, and prints how long it took. The first answer is the
# one measure already recorded in LAST_WRITE.
expect_write_recovers() {
	local what=$1 start=$SECONDS code=$LAST_WRITE
	until [[ "$code" == 2?? ]]; do
		if ((SECONDS - start >= WRITE_WAIT)); then
			fail "$what: no control write succeeded within ${WRITE_WAIT}s (last answer $code)"
			return
		fi
		sleep 2 # poll interval
		code=$(probe_write)
	done
	echo "$what: control write succeeded $((SECONDS - start))s after the first try"
}

# expect_spec_shared imports a spec through a Mixer that does not hold the
# lease ($2 is the holder), so the import is forwarded, and asserts that every
# Mixer then serves the same bytes under the same hash. The spec is the small
# example the documentation uses, under a name and version of its own.
expect_spec_shared() {
	local what=$1 holder=$2 via api code body want c f
	via=$(survivor "$holder")
	if ! wait_api "$via"; then
		fail "$what: the API of $via did not answer within ${API_WAIT}s"
		return
	fi
	f="$RESULTS/spec_$what.yaml"
	sed -e 's/^  id: "acme-auth"/  id: "ha-probe"/' -e "s/^  version: \"1.0.0\"/  version: \"1.0.$(date +%s)\"/" \
		"$ROOT/examples/specs/acme-auth.yaml" >"$f"
	grep -q 'id: "ha-probe"' "$f" || { fail "$what: the example spec no longer has the id this probe renames"; return; }
	code=$(curl -s -m 30 -o "$f.answer" -w "%{http_code}" -X POST "$via/specs" \
		-H 'Content-Type: application/x-yaml' --data-binary @"$f" || true)
	if [ "$code" != 201 ]; then
		fail "$what: spec import through a non-holder answered $code: $(head -c 300 "$f.answer")"
		return
	fi
	want=$(sed -n 's/.*"hash":"\([0-9a-f]*\)".*/\1/p' "$f.answer")
	body=$(cat "$f")
	local t0 listed served
	for c in $MIXERS; do
		api=$(api_of "$c")
		t0=$SECONDS
		while :; do
			listed=$(curl -s -m 10 "$api/specs/ha-probe" || true)
			served=$(curl -s -m 10 "$api/specs/ha-probe/latest" || true)
			grep -q "\"hash\":\"$want\"" <<<"$listed" && [ "$served" = "$body" ] && break
			if ((SECONDS - t0 >= WRITE_WAIT)); then
				grep -q "\"hash\":\"$want\"" <<<"$listed" || fail "$what: $c does not list the spec imported through $via after ${WRITE_WAIT}s: $(head -c 200 <<<"$listed")"
				[ "$served" = "$body" ] || fail "$what: $c does not serve the bytes imported through $via after ${WRITE_WAIT}s"
				break
			fi
			sleep 2 # poll interval
		done
		((SECONDS - t0 > 0)) && echo "$what: $c served the spec after $((SECONDS - t0))s"
	done
	echo "$what: spec imported through $via is served by every Mixer"
}

# expect_2xx asserts that a status code is a success.
expect_2xx() {
	case "$2" in 2??) ;; *) fail "$1 answered $2, want 2xx" ;; esac
}

holder_id() {
	tool leaseholder -url "$NATS_URL" "${TOOL_CONN[@]}" 2>/dev/null || true
}

# container_machine_id asks one Mixer's API for its own machine_id.
container_machine_id() {
	# A Mixer that does not answer has no id: that is not an error here.
	{ curl -s -m 3 "$1/health" || true; } | sed -n 's/.*"machine_id":"\([^"]*\)".*/\1/p'
}

# holder_container maps the lease's mixer_id to the container that reports it.
holder_container() {
	local want="$1" c
	[ -z "$want" ] && return 1
	for c in $MIXERS; do
		if [ "$(container_machine_id "$(api_of "$c")")" = "$want" ]; then echo "$c"; return 0; fi
	done
	return 1
}

# wait_holder polls until a Mixer holds the lease, other than the one whose
# id is $1 when it is set, and prints its container and id.
wait_holder() {
	local old=${1:-} deadline=$((SECONDS + HOLDER_WAIT)) id c
	while ((SECONDS < deadline)); do
		id=$(holder_id)
		if [ -n "$id" ] && [ "$id" != "$old" ]; then
			if c=$(holder_container "$id"); then echo "$c $id"; return 0; fi
		fi
		sleep 3 # poll interval
	done
	return 1
}

measure() {
	local name=$1
	echo "== $name"
	{
		echo "scenario $name $(date -u +%Y-%m-%dT%H:%MZ)"
		local t0 t1 code id racks_code
		t0=$(date +%s.%N)
		code=$(curl -s -o /dev/null -w "%{http_code}" "$API/health" || echo 000)
		t1=$(date +%s.%N)
		echo "health $code in $(echo "$t1 - $t0" | bc)s"
		# A real control write. Against a Mixer that does not hold the lease
		# it is forwarded to the holder; a 503 is what a refusal looks like.
		t0=$(date +%s.%N)
		code=$(probe_write)
		t1=$(date +%s.%N)
		echo "control-write $code in $(echo "$t1 - $t0" | bc)s"
		echo "$code" >"$RESULTS/$name.write_code"
		case "$code" in 2??) ;; *) echo "control-write answer: $(head -c 300 "$(ls -t "$RESULTS"/ha_probe_*.yaml.answer 2>/dev/null | head -1)" 2>/dev/null)" ;; esac
		id=$(holder_id)
		echo "holder_id ${id:-none}"
		racks_code=$(curl -s -m 5 -o "$RESULTS/$name.racks.json" -w "%{http_code} in %{time_total}s" "$API/racks" || true)
		echo "racks online: $(grep -o '"online":true' "$RESULTS/$name.racks.json" 2>/dev/null | wc -l | tr -d ' ') (GET /racks ${racks_code:-failed})"
		rack_peers
	} | tee "$RESULTS/$name.txt"
	LAST_WRITE=$(cat "$RESULTS/$name.write_code" 2>/dev/null || echo 000)
}

fresh() {
	compose down -v --remove-orphans >/dev/null 2>&1 || true
	compose up -d >/dev/null
	# Shaped from the start, so the cluster forms and elects over the slow link.
	[ "$PROFILE" = latam ] && cross_site_netem
	return 0
}

# start prints the holder after a fresh start, and sets API to a Mixer that
# does not hold the lease. It fails the scenario when no Mixer takes the lease.
start() {
	local started=$SECONDS
	fresh
	echo "waiting up to ${HOLDER_WAIT}s for the cluster and the lease"
	if ! read -r h hid < <(wait_holder); then
		fail "$SCENARIO: no Mixer took the lease within ${HOLDER_WAIT}s of the start"
		return 1
	fi
	# The first election, measured from the containers' start (the poll adds
	# up to one interval).
	echo "$SCENARIO: first lease holder $((SECONDS - started))s after the start" | tee -a "$RESULTS/first_election.txt"
	echo "holder is $h ($hid)"
	API="$(survivor "$h")"
	wait_api "$API" || { fail "$SCENARIO: the API of a non-holder did not answer within ${API_WAIT}s"; return 1; }
	# Every Mixer, not only the one the scenario writes through: one on the
	# far site can be ready seconds after the holder, and a check on it would
	# measure its start.
	local c t0=$SECONDS
	for c in $MIXERS; do
		wait_api "$(api_of "$c")" || { fail "$SCENARIO: the API of $c did not answer within ${API_WAIT}s"; return 1; }
	done
	echo "$SCENARIO: every Mixer's API answers, $((SECONDS - t0))s after the holder's"
	tool jsleader -url "$NATS_URL" "${TOOL_CONN[@]}" -created >"$RESULTS/$SCENARIO.streams.txt" 2>&1 || true
}

# rolling_scenario starts the cluster on PREV_REF's Mixer and upgrades one
# Mixer at a time, the lease holder last, with a clean stop. Control writes
# must come back after each step, and nothing written or sent is lost.
rolling_scenario() {
	local m took ver src="$HERE/.build/prev-src" ctxdir="$HERE/.build/prev-ctx"
	[ -n "$PREV_REF" ] || { fail "rolling needs PREV_REF, a version with snake.external"; return; }
	rm -rf "$ctxdir" && mkdir -p "$ctxdir/.build"
	git -C "$ROOT" worktree remove --force "$src" >/dev/null 2>&1 || rm -rf "$src"
	git -C "$ROOT" worktree add --detach "$src" "$PREV_REF" >/dev/null
	(cd "$src" && CGO_ENABLED=1 go build -o "$ctxdir/.build/fluxrig-mixer" ./cmd/fluxrig-mixer)
	git -C "$ROOT" worktree remove --force "$src" >/dev/null
	cp "$HERE/.build/fluxrig" "$HERE/.build/nats-server" "$ctxdir/.build/"
	cp "$HERE/Dockerfile" "$ctxdir/Dockerfile"
	docker build -q -t fluxrig-ha-mixer:prev "$ctxdir" >/dev/null
	echo "rolling: built the Mixer of $PREV_REF ($(git -C "$ROOT" rev-parse --short "$PREV_REF"))"
	compose down -v --remove-orphans >/dev/null 2>&1 || true
	HA_MIXER_IMAGE=fluxrig-ha-mixer:prev compose up -d >/dev/null
	[ "$PROFILE" = latam ] && cross_site_netem
	read -r h hid < <(wait_holder) || { fail "rolling: no Mixer took the lease on the previous version"; return; }
	API="$(survivor "$h")"
	wait_api "$API" || { fail "rolling: a non-holder's API did not answer"; return; }
	wait_racks_online >/dev/null || fail "rolling: the Racks did not come online on the previous version"
	expect_2xx "rolling: a control write on the previous version" "$(probe_write)"
	start_dataplane || true
	for m in $(for c in $MIXERS; do [ "$c" = "$h" ] || echo "$c"; done) "$h"; do
		echo "rolling: upgrading $m"
		API="$(survivor "$m")"
		docker stop "$m" >/dev/null
		local stopped=$SECONDS
		docker cp "$HERE/.build/fluxrig-mixer" "$m:/usr/local/bin/fluxrig-mixer"
		docker start "$m" >/dev/null
		[ "$PROFILE" = latam ] && cross_site_netem
		if took=$(time_until_write "$stopped" "$HOLDER_WAIT"); then
			echo "rolling: $m upgraded; control writes through another Mixer answered ${took}s after it stopped" | tee -a "$RESULTS/unavailability.txt"
		else
			fail "rolling: no control write within ${HOLDER_WAIT}s of upgrading $m"
		fi
		wait_api "$(api_of "$m")" || fail "rolling: the upgraded $m did not answer"
	done
	for m in $MIXERS; do
		ver=$({ curl -s -m 5 "$(api_of "$m")/health" || true; } | python3 -c 'import json,sys; print(json.load(sys.stdin).get("version",""))' 2>/dev/null || true)
		echo "rolling: $m runs $ver"
	done
	read -r h hid < <(wait_holder) || fail "rolling: no holder after the upgrade"
	API="$(survivor "$h")"
	expect_lease_visible "$hid"
	wait_racks_online >/dev/null || fail "rolling: the Racks are not online after the upgrade"
	expect_dataplane "$(now)"
	expect_no_lost_writes "rolling"
}

declare -A RUNS=()
for sc in $SCENARIOS; do
	# A scenario named twice runs under a numbered name, so each run keeps its
	# own ledger of acknowledged writes and its own logs.
	RUNS[$sc]=$((${RUNS[$sc]:-0} + 1))
	SCENARIO=$sc
	[ "${RUNS[$sc]}" = 1 ] || SCENARIO="$sc-${RUNS[$sc]}"
	if [ "$sc" = rolling ]; then
		rolling_scenario
		save_logs "$SCENARIO"
		continue
	fi
	if ! start; then
		save_logs "$SCENARIO"
		continue
	fi

	case "$sc" in
	up)
		measure up
		expect_2xx "up: control write" "$LAST_WRITE"
		expect_spec_shared up "$h"
		expect_lease_visible "$hid"
		if took=$(wait_racks_online); then
			echo "up: every Rack online ${took}s after the lease"
		else
			fail "up: not every Rack read online within ${RACKS_WAIT}s"
		fi
		;;
	kill-holder | stop-holder)
		measure "$sc-before"
		expect_2xx "$sc: control write before the fault" "$LAST_WRITE"
		wait_racks_online >/dev/null || fail "$sc: not every Rack read online before the fault"
		start_dataplane || true
		if [ "$sc" = kill-holder ]; then
			fault "killing the lease holder $h (SIGKILL)"
			docker kill "$h" >/dev/null
		else
			# A clean stop runs the shutdown path: the lease is released and
			# the router's subscriptions close.
			fault "stopping the lease holder $h (clean shutdown)"
			docker stop "$h" >/dev/null
		fi
		fault_at=$SECONDS
		# Written through a Mixer that stays up, from the moment of the fault:
		# the time to the first success is the control-write unavailability.
		if took=$(time_until_write "$fault_at" "$HOLDER_WAIT"); then
			echo "$sc: control writes unavailable for ${took}s after the fault" | tee -a "$RESULTS/unavailability.txt"
		else
			fail "$sc: no control write succeeded within ${HOLDER_WAIT}s of the fault"
		fi
		if read -r h2 hid2 < <(wait_holder "$hid"); then
			echo "new holder is $h2 ($hid2)"
			# Through the Mixer that is neither the old holder nor the new one,
			# so the write is forwarded to the new holder.
			API="$(survivor "$h" "$h2")"
			wait_api "$API" || fail "$sc: the remaining Mixer's API did not answer"
			measure "$sc-after"
			expect_2xx "$sc: control write after the handover" "$LAST_WRITE"
		else
			fail "$sc: no new holder within ${HOLDER_WAIT}s"
		fi
		expect_dataplane "$(now)"
		expect_no_lost_writes "$sc"
		;;
	cut-holder)
		# The lease holder loses every NATS server, while it keeps running:
		# a peer must take the lease, and the cut-off Mixer must refuse
		# control writes instead of answering for a registry it cannot reach.
		if took=$(wait_racks_online); then echo "every Rack online ${took}s after the start"; else fail "$sc: not every Rack read online before the fault"; fi
		measure "$sc-before"
		expect_2xx "$sc: control write before the fault" "$LAST_WRITE"
		start_dataplane || true
		fault "cutting the lease holder $h off from every NATS server"
		cut_start=$SECONDS
		# shellcheck disable=SC2046 # one address per argument
		cut_off "$h" $(nats_ips)
		if read -r h2 hid2 < <(wait_holder "$hid"); then
			echo "a peer took the lease: $h2 ($hid2) $((SECONDS - cut_start))s after the cut" | tee -a "$RESULTS/unavailability.txt"
		else
			fail "$sc: no peer took the lease within ${HOLDER_WAIT}s"
		fi
		measure "$sc-during"
		expect_write_recovers "$sc: during the cut"
		code=$(API="$(api_of "$h")" SCENARIO="$sc-cut-off" probe_write)
		case "$code" in 2??) fail "$sc: a control write through the cut-off $h answered $code" ;; *) echo "$sc: a control write through the cut-off $h answered $code" ;; esac
		while ((SECONDS - cut_start < CUT_HOLD)); do sleep 5; done # hold the cut
		measure "$sc-held"
		if n=$(racks_online); then
			[ "$n" = "$(echo "$RACKS" | wc -w | tr -d ' ')" ] || fail "$sc: $n Racks online after ${CUT_HOLD}s of the cut, want every Rack"
		else
			fail "$sc: GET /racks did not answer 200 after ${CUT_HOLD}s of the cut"
		fi
		fault "healing $h"
		heal "$h"
		heal_time=$(now)
		# Back on NATS, the Mixer serves writes again, forwarded to the holder.
		API="$(api_of "$h")"
		LAST_WRITE=$(probe_write)
		expect_write_recovers "$sc: through the healed $h"
		API="$(survivor "$h")"
		expect_dataplane "$heal_time"
		expect_no_lost_writes "$sc"
		;;
	kill-nats | kill-nats-idle)
		# The NATS server that leads the message stream dies, and comes back.
		# The traffic between the Racks and the control writes must go on.
		# kill-nats-idle first moves the leader to a server no Rack is
		# connected to: the Racks then lose no connection, only the leader.
		wait_racks_online >/dev/null || fail "$sc: not every Rack read online before the fault"
		measure "$SCENARIO-before"
		expect_2xx "$sc: control write before the fault" "$LAST_WRITE"
		start_dataplane || true
		if [ "$sc" = kill-nats-idle ]; then
			used=" $(peers_of fluxrig-ha-r1) $(peers_of fluxrig-ha-r2) "
			idle=""
			for c in $NATS_NODES; do grep -qw "$(ip_of "$c")" <<<"$used" || { idle=$c; break; }; done
			# Each Rack holds two connections, so all three servers often have
			# one: then there is nothing to measure, and the run says so.
			[ -n "$idle" ] || { echo "$sc: skipped, every NATS server has a Rack connected ($used)" | tee -a "$RESULTS/skipped.txt"; save_logs "$SCENARIO"; continue; }
			echo "moving the leader of flux-msg to ${idle#fluxrig-ha-}, which no Rack is connected to"
			tool jsleader -url "$NATS_URL" "${TOOL_CONN[@]}" -stepdown flux-msg -preferred "${idle#fluxrig-ha-}" 2>&1 | sed 's/^/  /'
			for _ in $(seq 1 15); do [ "$(leader_of flux-msg)" = "$idle" ] && break; sleep 1; done
			[ "$(leader_of flux-msg)" = "$idle" ] || { fail "$sc: the leader of flux-msg did not move to $idle"; save_logs "$SCENARIO"; continue; }
		fi
		victim=$(leader_of flux-msg)
		[ -n "$victim" ] || { fail "$sc: no leader of flux-msg"; save_logs "$SCENARIO"; continue; }
		fault "killing $victim, the leader of flux-msg (SIGKILL)"
		docker kill "$victim" >/dev/null
		fault_at=$SECONDS
		if took=$(time_until_write "$fault_at" "$HOLDER_WAIT"); then
			echo "$sc: control writes unavailable for ${took}s after the fault" | tee -a "$RESULTS/unavailability.txt"
		else
			fail "$sc: no control write succeeded within ${HOLDER_WAIT}s of the fault"
		fi
		expect_dataplane "$(now)"
		echo "starting $victim again"
		docker start "$victim" >/dev/null
		[ "$PROFILE" = latam ] && cross_site_netem
		measure "$SCENARIO-after"
		expect_write_recovers "$sc: after the restart"
		wait_racks_online >/dev/null || fail "$sc: not every Rack read online after the restart"
		expect_dataplane "$(now)"
		expect_no_lost_writes "$sc" 3
		;;
	cut-rack)
		# rack-r1 loses the NATS server it is connected to. It must move to
		# another one, and its traffic to rack-r2 must go on.
		wait_racks_online >/dev/null || fail "$sc: not every Rack read online before the fault"
		measure "$sc-before"
		start_dataplane || true
		rip=$(peers_of fluxrig-ha-r1 | awk '{print $1}')
		target=$(nats_of_ip "$rip") || { fail "$sc: rack-r1 is connected to no NATS server ($rip)"; save_logs "$SCENARIO"; continue; }
		fault "cutting rack-r1 off from $target ($rip)"
		cut_start=$SECONDS
		cut_off fluxrig-ha-r1 "$rip"
		moved=""
		while ((SECONDS - cut_start < WRITE_WAIT)); do
			now=$(peers_of fluxrig-ha-r1)
			if [ "$now" != none ] && ! grep -qw "$rip" <<<"$now"; then moved="$now"; break; fi
			sleep 2 # poll interval
		done
		if [ -n "$moved" ]; then
			echo "$sc: rack-r1 moved to $moved $((SECONDS - cut_start))s after the cut" | tee -a "$RESULTS/unavailability.txt"
		else
			fail "$sc: rack-r1 did not move to another NATS server within ${WRITE_WAIT}s"
		fi
		cut_time=$(now)
		expect_dataplane "$cut_time"
		while ((SECONDS - cut_start < CUT_HOLD)); do sleep 5; done # hold the cut
		if n=$(racks_online); then
			[ "$n" = "$(echo "$RACKS" | wc -w | tr -d ' ')" ] || fail "$sc: $n Racks online after ${CUT_HOLD}s of the cut, want every Rack"
		else
			fail "$sc: GET /racks did not answer 200 after ${CUT_HOLD}s of the cut"
		fi
		measure "$sc-held"
		heal fluxrig-ha-r1
		expect_dataplane "$(now)"
		;;
	events-failover)
		# Terminals connect to rack-r1 while the NATS server that leads the
		# event stream dies and comes back. Every terminal must get its logon:
		# the Rack parks an event it cannot publish, and publishes it later.
		wait_racks_online >/dev/null || fail "$sc: not every Rack read online before the fault"
		activate "$EVENTS" || { save_logs "$SCENARIO"; continue; }
		ready=""
		for _ in $(seq 1 30); do
			if tool logons -addr 127.0.0.1:8888 -duration 1s -timeout 5s >/dev/null 2>&1; then ready=1; break; fi
			sleep 2 # poll interval
		done
		[ -n "$ready" ] || { fail "$sc: rack-r1 sent no logon within 60s of the activation"; save_logs "$SCENARIO"; continue; }
		victim=$(leader_of flux-events)
		[ -n "$victim" ] || { fail "$sc: no leader of flux-events"; save_logs "$SCENARIO"; continue; }
		echo "terminals connect for ${LOGON_RUN}s; $victim, the leader of flux-events, dies at ${LOGON_KILL_AT}s and starts at ${LOGON_START_AT}s"
		tool logons -addr 127.0.0.1:8888 -duration "${LOGON_RUN}s" -timeout "$LOGON_TIMEOUT" >"$RESULTS/$sc.logons.txt" 2>&1 &
		logons_pid=$!
		run_start=$SECONDS
		while ((SECONDS - run_start < LOGON_KILL_AT)); do sleep 1; done
		docker kill "$victim" >/dev/null
		fault "$sc: killed $victim"
		while ((SECONDS - run_start < LOGON_START_AT)); do sleep 1; done
		docker start "$victim" >/dev/null
		[ "$PROFILE" = latam ] && cross_site_netem
		fault "$sc: started $victim"
		rc=0
		wait "$logons_pid" || rc=$?
		tail -1 "$RESULTS/$sc.logons.txt" | sed "s/^/$sc: /" | tee -a "$RESULTS/events.txt"
		[ "$rc" = 0 ] || fail "$sc: a terminal got no logon within $LOGON_TIMEOUT: $(grep -c 'no logon' "$RESULTS/$sc.logons.txt") of them, see $sc.logons.txt"
		;;
	telemetry)
		# The writer of the shared telemetry store, the lease holder, dies
		# while the Racks keep sending. The store must end with every
		# numbered line of rack-r1 once.
		[ "$TELEMETRY" != local ] || { fail "telemetry needs TELEMETRY=s3, nats or otlp"; save_logs "$SCENARIO"; continue; }
		wait_racks_online >/dev/null || fail "$sc: not every Rack read online before the fault"
		start_dataplane || true
		sleep "$TELEMETRY_BEFORE" # the flow runs
		fault "killing the lease holder $h, the writer of the telemetry store (SIGKILL)"
		docker kill "$h" >/dev/null
		if read -r h2 hid2 < <(wait_holder "$hid"); then
			echo "$sc: new holder $h2"
		else
			fail "$sc: no new holder within ${HOLDER_WAIT}s"
		fi
		API="$(survivor "$h")"
		sleep "$TELEMETRY_AFTER" # the flow runs under the new writer
		last=$(docker logs fluxrig-ha-r1 2>&1 | grep -o 'ha-seq [0-9]*' | awk '{print $2}' | sort -n | tail -1)
		[ -n "$last" ] || { fail "$sc: rack-r1 logged no numbered line"; save_logs "$SCENARIO"; continue; }
		echo "$sc: rack-r1 logged lines 1 to $last; waiting up to ${TELEMETRY_WAIT}s for the store to hold each once"
		start=$SECONDS
		while :; do
			if [ "$TELEMETRY" = otlp ]; then
				docker cp fluxrig-ha-otlp:/tmp/otlp.jsonl "$RESULTS/otlp.jsonl" >/dev/null 2>&1 || true
				counts=$(tool otlpreceiver -count "$RESULTS/otlp.jsonl" -match 'ha-seq ' 2>/dev/null || true)
			else
				# A large store (the Racks at debug) can take a while to answer.
				counts=$({ curl -s -m 60 "$API/telemetry/logs?since=30m&limit=1000000" || true; } | python3 -c '
import json, sys, collections
c = collections.Counter(l.get("body", "") for l in (json.load(sys.stdin) or []) if l.get("body", "").startswith("ha-seq "))
for body, n in c.items(): print(n, body)' 2>/dev/null || true)
			fi
			verdict=$(python3 -c '
import sys
last = int(sys.argv[1]); seen = {}
for line in sys.stdin:
    # An empty answer (the query timed out) is no line: everything is still
    # missing, and the next poll asks again.
    if not line.strip():
        continue
    n, _, seq = line.split()
    seen[int(seq)] = int(n)
missing = [s for s in range(1, last + 1) if s not in seen]
dups = sum(n - 1 for s, n in seen.items() if n > 1 and s <= last)
print(len(missing), dups, ",".join(map(str, missing[:10])))' "$last" <<<"$counts")
			read -r missing dups first_missing <<<"$verdict"
			[ "$missing" = 0 ] && break
			if ((SECONDS - start >= TELEMETRY_WAIT)); then break; fi
			sleep 10 # poll interval
		done
		echo "$sc: lines 1 to $last after $((SECONDS - start))s: $missing missing, $dups more than once" | tee -a "$RESULTS/telemetry.txt"
		if [ "$missing" != 0 ]; then
			# What the shared store holds, and each Mixer's copy of it, tell
			# a batch never stored from one a Mixer does not read.
			{
				echo "== s3 bucket"
				[ "$TELEMETRY" = s3 ] && curl -s -m 30 "http://$(addr_of fluxrig-ha-s3):9000/fluxrig-telemetry?list-type=2&max-keys=10000" | tr '<' '\n<' | grep -E '^(Key|Size|ETag)>' || true
				# Which numbered lines of rack-r1 the stream holds: missing
				# there, the Rack never sent them; there, the store lost them.
				echo "== flux.telemetry.logs in the stream (whole dump in $SCENARIO.telemetry-stream.txt)"
				tool streamdump -url "$NATS_URL" "${TOOL_CONN[@]}" -stream flux-telemetry -subject flux.telemetry.logs -field '*' -timeout 60s \
					>"$RESULTS/$SCENARIO.telemetry-stream.txt" 2>&1 || true
				python3 - "$RESULTS/$SCENARIO.telemetry-stream.txt" <<'PY' || true
import re, sys
seen = sorted({int(n) for n in re.findall(r"ha-seq (\d+)", open(sys.argv[1], errors="replace").read())})
if seen:
    gaps = [n for n in range(seen[0], seen[-1] + 1) if n not in set(seen)]
    print(f"{len(seen)} numbered lines of rack-r1, {seen[0]} to {seen[-1]}, {len(gaps)} missing in between ({gaps[:10]})")
else:
    print("no numbered line of rack-r1 (see the whole dump)")
PY
				for c in $MIXERS; do
					echo "== $c cache"
					docker exec "$c" sh -c 'find /data/telemetry -type f -exec ls -la {} +' 2>&1 || true
				done
			} >"$RESULTS/$SCENARIO.store.txt" 2>&1
			fail "$sc: the store misses $missing of $last lines ($first_missing...); see $SCENARIO.store.txt"
		fi
		if [ "$TELEMETRY" = otlp ]; then
			# One batch can arrive twice when the writer dies between the
			# export and its confirmation: recorded, not a failure.
			[ "$dups" = 0 ] || echo "$sc: $dups lines arrived twice, from the batch of the killed writer"
		else
			[ "$dups" = 0 ] || fail "$sc: $dups lines are in the store more than once"
		fi
		;;
	wasm-catalog)
		# The catalog and its keyring are the same on every Mixer, and an
		# import the bus does not take is refused, not reported as done.
		fx="$RESULTS/wasm"
		tool wasmfixture -out "$fx"
		wasm_import() { # $1 Mixer, $2 file, $3 query; prints the status, keeps the body
			curl -s -m 60 -o "$fx/answer" -w "%{http_code}" -X POST "$(api_of "$1")/wasm/import$3" \
				-H 'Content-Type: application/octet-stream' --data-binary @"$fx/$2" || true
		}
		code=$(wasm_import fluxrig-ha-m1 module.wasm "?allow_unsigned=true")
		[ "$code" = 200 ] || fail "$sc: an unsigned import through m1 answered $code: $(head -c 300 "$fx/answer")"
		h1=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["hash"])' "$fx/answer" 2>/dev/null || true)
		for c in fluxrig-ha-m2 fluxrig-ha-m3; do
			curl -s -m 10 "$(api_of "$c")/wasm/catalog" | grep -q "\"$h1\"" || fail "$sc: $c does not list the module imported through m1"
		done
		code=$(curl -s -m 10 -o /dev/null -w "%{http_code}" -X PUT "$(api_of fluxrig-ha-m2)/wasm/keys/ha-vendor" \
			-H 'Content-Type: application/octet-stream' --data-binary @"$fx/vendor.pub" || true)
		[ "$code" = 204 ] || fail "$sc: trusting a vendor through m2 answered $code"
		code=$(wasm_import fluxrig-ha-m3 signed.wasm "")
		status=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["status"])' "$fx/answer" 2>/dev/null || true)
		[ "$code" = 200 ] && [ "$status" = "Trusted: ha-vendor" ] ||
			fail "$sc: a signed import through m3, with the key trusted through m2, answered $code ($status)"
		curl -s -m 10 "$(api_of fluxrig-ha-m1)/wasm/keys" | grep -q '"vendor":"ha-vendor"' || fail "$sc: m1 does not list the vendor trusted through m2"
		code=$(curl -s -m 10 -o /dev/null -w "%{http_code}" -X DELETE "$(api_of fluxrig-ha-m3)/wasm/keys/ha-vendor" || true)
		[ "$code" = 204 ] || fail "$sc: revoking the vendor through m3 answered $code"
		code=$(wasm_import fluxrig-ha-m1 signed.wasm "")
		[ "$code" = 400 ] || fail "$sc: a signed import through m1 after the revocation through m3 answered $code, want 400"
		out=$("$HERE/.build/fluxrig" wasm keys list --api-url "$(api_of fluxrig-ha-m2 | sed 's#/api/v1$##')" 2>&1 || true)
		grep -q -- "- ha-vendor: revoked" <<<"$out" || fail "$sc: fluxrig wasm keys list through m2 does not show the revocation: $out"
		echo "$sc: one catalog and one keyring on every Mixer"
		# shellcheck disable=SC2046 # one address per argument
		cut_off fluxrig-ha-m1 $(nats_ips)
		code=$(wasm_import fluxrig-ha-m1 module.wasm "?allow_unsigned=true")
		heal fluxrig-ha-m1
		[ "$code" = 503 ] || fail "$sc: an import through m1 cut off from NATS answered $code, want 503: $(head -c 300 "$fx/answer")"
		echo "$sc: an import through a Mixer cut off from NATS answered $code"
		;;
	probe)
		# rack-r1's NATS user against what it must not do, and what it must.
		[ "$SECURE" = 1 ] || { fail "probe needs SECURE=1"; save_logs "$SCENARIO"; continue; }
		wait_racks_online >/dev/null || fail "$sc: not every Rack read online"
		if out=$(tool probe -url "$SCHEME://127.0.0.1:4223" -ca "$PKI/ca.crt" -cert "$PKI/rack-r1.crt" -key "$PKI/rack-r1.key" \
			-admin-cert "$PKI/admin.crt" -admin-key "$PKI/admin.key" 2>&1); then
			echo "$out" | tee "$RESULTS/probe.txt" | tail -1
		else
			echo "$out" | tee "$RESULTS/probe.txt"
			fail "probe: $(tail -1 <<<"$out")"
		fi
		;;
	leader-cost)
		[ "$PROFILE" = latam ] || cross_site_netem
		timed_writes() {
			local label=$1 n=${2:-20} t0 t1 total=0
			for _ in $(seq 1 "$n"); do
				t0=$(date +%s.%N)
				probe_write >/dev/null
				t1=$(date +%s.%N)
				total=$(echo "$total + ($t1 - $t0)" | bc)
			done
			echo "$label: $n writes, total ${total}s, mean $(echo "scale=4; $total / $n" | bc)s"
		}
		for site in a b; do
			echo "moving the leaders of flux-msg and KV_fluxrig_registry to site:$site"
			tool jsleader -url "$NATS_URL" "${TOOL_CONN[@]}" -stepdown flux-msg,KV_fluxrig_registry -tag "site:$site" 2>&1 | sed 's/^/  /'
			sleep 20 # the elections settle
			echo "LEADERS:"; tool jsleader -url "$NATS_URL" "${TOOL_CONN[@]}" 2>/dev/null | python3 -m json.tool
			timed_writes "write-latency-leaders-in-site-$site" 20 | tee "$RESULTS/leader-cost-$site.txt"
		done
		;;
	*)
		echo "unknown scenario: $sc" >&2
		exit 2
		;;
	esac
	save_logs "$SCENARIO"
done

echo "results in $RESULTS"
if ((FAILURES > 0)); then
	echo "$FAILURES assertion(s) failed; see $RESULTS/failures.txt"
	exit 1
fi
echo "all assertions passed"
