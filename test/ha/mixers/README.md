# Mixer HA testbed

Measures the product in its HA mode: three stock NATS servers in one cluster,
three Mixers on it with `snake.external` and the write lease on, and two Racks
that know every NATS server. Everything runs in Docker on one host, from the
binaries of this checkout.

| Site | NATS | Mixers | Racks |
| :--- | :--- | :--- | :--- |
| a | n1, n2 | m1, m2 | r1 |
| b | n3 | m3 | r2 |

```bash
test/ha/mixers/run.sh                          # every scenario, on a clean network
PROFILE=latam test/ha/mixers/run.sh            # about 220 ms round trip between the sites
SCENARIOS="cut-holder kill-nats" test/ha/mixers/run.sh
SECURE=0 test/ha/mixers/run.sh                 # plaintext NATS
make test-ha                                   # every scenario, at the latam profile
```

With `SECURE=1`, the default, NATS requires TLS and maps each client
certificate to a user (`verify_and_map`). The Mixers and the testbed's tools
are unrestricted. Each Rack gets the permissions that
`fluxrig scenario permissions` writes for the scenarios the script activates.
The routes between the NATS servers stay in plaintext: they belong to the
operator of NATS.

Results go to `results/<timestamp>/`: a file per measurement, every
container's log for each scenario, and `failures.txt` when an assertion fails.
The script exits non-zero when one does.

## Scenarios

| Scenario | Fault | Asserts |
| :--- | :--- | :--- |
| `up` | none | A control write; a spec imported through a Mixer without the lease is served by every Mixer; every `/health` names the holder; the lease metric shows one holder of three; both Racks online |
| `kill-holder` | `docker kill` of the lease holder | A peer takes the lease; control writes come back; the traffic between the Racks loses nothing; no acknowledged write is lost |
| `stop-holder` | `docker stop` of the lease holder | The same, through the shutdown path |
| `cut-holder` | The lease holder loses every NATS server, and keeps running | A peer takes the lease; a write through the cut-off Mixer is refused; the Racks stay online; after the heal, writes through it work again; nothing lost |
| `kill-nats` | `docker kill` of the NATS server that leads `flux-msg`, then a restart | Control writes and the traffic between the Racks go on; after the restart the registry is back at three replicas with every write |
| `kill-nats-idle` | The same, after moving the leader to a server no Rack is connected to. Not in the default run: each Rack holds two connections, so often every server has one, and the run is then skipped | The same as `kill-nats` |
| `cut-rack` | rack-r1 loses the NATS server it is connected to | rack-r1 moves to another server; its traffic to rack-r2 goes on; both stay online |
| `events-failover` | Terminals connect to rack-r1 every 0.5 s while the leader of `flux-events` dies and starts again | Every terminal gets its logon, which comes from a reader of the durable connection events |
| `telemetry` | `docker kill` of the lease holder, the writer of the telemetry, with `TELEMETRY=s3`, `nats` or `otlp` | The store ends with every numbered line of rack-r1 once. With `otlp` the lines of the batch the killed writer exported and did not confirm can arrive twice, and are counted |
| `wasm-catalog` | A Mixer cut off from NATS at the end | A module imported through one Mixer is listed by the others; a vendor trusted through one Mixer is trusted by another, and a revocation through a third refuses it everywhere; an import through the cut-off Mixer answers 503 |
| `probe` | none (`SECURE=1` only) | rack-r1's NATS user is refused another Rack's messages, events, durable readers, inbox and notifications, the registry and changes to the streams, and keeps what it needs |
| `leader-cost` | none | Measures control-write latency with the leaders of `flux-msg` and the registry in each site |
| `rolling` | Upgrades one Mixer at a time from `PREV_REF` | Control writes come back after each step; nothing lost. Needs a `PREV_REF` that has `snake.external` |

Three things a Rack's permissions do not refuse, and `probe` does not assert:

- telemetry under another Rack's name: the Mixer attributes each log by its
  payload, not by its subject;
- a message on the subject of a wire the Rack consumes: the Rack publishes a
  sync probe there before its gears start, and other consumers of that wire
  skip a probe but would take any other message;
- the description of another Rack's consumer on a stream both use, if the
  Rack knows its name: its filter subject and its position, not its messages.
  A Rack looks up its own consumers when it misses heartbeats, and their
  names are random.

The traffic between the Racks is one numbered message every 200 ms from
rack-r1 to rack-r2 on the guaranteed lane, across the two sites;
`tools/dataplane` reads rack-r2's log for gaps and missing numbers. A number
still missing `LATE_WAIT` seconds (30) after the gap was seen fails the
scenario, and `tools/streamdump` then writes what the stream holds on that
subject, with the state of its consumers. A message the consumer sent through
the NATS server that died arrives again after `snake.ack_wait` (10 s), and the
scenario reports it as late. A number that arrives twice is
counted and does not fail: the lane delivers at least once, and a message
whose acknowledgement was lost with a NATS server is delivered again.

## Tools

`tools/` holds the small programs the script runs: `leaseholder`, `jsleader`
and `scenariocheck` read the cluster, `dataplane` and `logons` measure the
traffic, `streamdump` prints what a stream holds on a subject, `probe` tries
what a Rack's permissions must refuse, and `clusterkey` writes the Mixers' shared signing key, and `wasmfixture` a minimal Wasm module, signed and unsigned, with its vendor key. Each takes `-url`, and
`-ca`, `-cert` and `-key` for a cluster with TLS.
