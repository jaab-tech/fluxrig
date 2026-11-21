# HA chaos testbed

Measures what the Mixer high-availability design relies on: how long a
replicated JetStream key-value bucket refuses writes when a node fails or
the network between nodes degrades, and whether a write it acknowledged
survives.

Three JetStream nodes run in Docker, clustered over routes on a fixed
bridge network. The failover tool (`../failover`) runs on the host as the
client: it makes revision-checked updates, as the Mixer registry does, and
applies one fault per round.

```bash
test/ha/testbed/run.sh                                  # every scenario
SCENARIOS="baseline partition" ROUNDS=5 test/ha/testbed/run.sh
make test-ha-failover                                   # the same, via Make
```

Results go to `results/<timestamp>/`: one file per scenario and a
`summary.txt`.

## Scenarios

| Scenario | Network between nodes | Fault on the leader |
| :--- | :--- | :--- |
| `baseline` | clean | killed (`docker kill`), then restarted |
| `wan` | `WAN_NETEM` | killed, then restarted |
| `lossy` | `LOSSY_NETEM` | killed, then restarted |
| `harsh` | `HARSH_NETEM` | killed, then restarted |
| `spikes` | `SPIKE_BASE`, switching to `SPIKE_NETEM` on a timer | killed, then restarted |
| `partition` | clean | cut off from its peers for `PARTITION_DURATION`, then healed |
| `wan-partition` | `WAN_NETEM` | cut off, then healed |

Faults apply only to traffic from a node to its peers, never to the client
on the host. That models Mixers in different sites, each with a local
client.

## Settings

Every value is an environment variable of `run.sh`:

| Variable | Default | Meaning |
| :--- | :--- | :--- |
| `SCENARIOS` | all seven | Which scenarios to run, in order. |
| `ROUNDS` | `10` | Fault rounds per scenario. |
| `WAN_NETEM` | `delay 110ms 20ms 25% distribution normal` | Link shaping for `wan`: 110 ms each way is about a 220 ms round trip, the order of a link from southern Latin America (Uruguay) to a US region. |
| `LOSSY_NETEM` | `delay 130ms 30ms distribution pareto loss gemodel 0.1% 10%` | A poor link: long-tailed latency, about 1% loss in bursts of about 10 packets. |
| `HARSH_NETEM` | `delay 130ms 30ms distribution pareto loss gemodel 1% 10%` | A failing link: the same tail, about 9% loss in bursts of about 10 packets. |
| `SPIKE_BASE` | `delay 110ms 20ms` | Latency between spikes. |
| `SPIKE_NETEM` | `delay 600ms 60ms` | Latency during a spike. |
| `SPIKE_PERIOD` | `10` | Seconds from one spike to the next. |
| `SPIKE_LENGTH` | `2` | Seconds a spike lasts. |
| `PARTITION_DURATION` | `20s` | How long a partition holds. |
| `SETTLE` | `3s` | Pause between rounds once the cluster is whole. |
| `OP_TIMEOUT` | `2s` | Timeout of one write. It must exceed a replicated write's round trip on the slowest profile: a LAN-sized value times out every write at ~220 ms round trip and a healthy cluster reads as down. |
| `WRITE_INTERVAL` | `5ms` | Pause between writes. |
| `RECOVER_TIMEOUT` | `90s` | How long a write may take to be acknowledged after a fault. |
| `FORM_TIMEOUT` | `90s` | How long the cluster may take to form, or to become whole again. |
| `HA_NET` | `172.30.99` | First three octets of the bridge network. |

The `*_NETEM` values go to `tc qdisc ... netem` unchanged: any netem
option (reorder, duplicate, corrupt, rate) works. For burst loss,
`loss gemodel p r` enters the bad state with probability `p` and leaves it
with probability `r`, dropping every packet while there: overall loss is
`p/(p+r)`, in bursts of `1/r` packets on average. `gemodel 1% 10%` is about
9% loss, not 1%.

## What a round reports

- **leader known after**: from the fault taking effect to a new leader
  being reported.
- **writes back after**: from the fault taking effect to the
  acknowledgement of the first write issued after it. A write issued
  before the fault does not count.
- **durability**: after the heal, the value on the stream leader, read
  through the leader's message API rather than a direct read, must be at
  least the last acknowledged one. `LOST` here means an acknowledged
  write is gone. This is a floor, not a proof: the writer keeps writing
  while the check reads, so a clean round means no loss was detected in
  it, not that none can happen.
- **no recovery**: a round in which no write was acknowledged within
  `RECOVER_TIMEOUT` (plus the partition, if any). It is a result: the
  round is healed and the run goes on.
- **stale reads**: key-value reads that returned an older value. A direct
  read may be served by any replica. This is staleness, not loss.

## Manual faults

`chaos.sh` drives the same faults by hand against the running containers:

```bash
test/ha/testbed/chaos.sh netem all "delay 50ms 10ms"
test/ha/testbed/chaos.sh isolate fluxrig-ha-n2
test/ha/testbed/chaos.sh heal fluxrig-ha-n2
test/ha/testbed/chaos.sh spikes start "delay 110ms" "delay 800ms" 15 3
test/ha/testbed/chaos.sh spikes stop
```

Requirements: Docker with Compose, and `NET_ADMIN` for the containers
(granted in the compose file). No image is pulled except
`debian:bookworm-slim`.
