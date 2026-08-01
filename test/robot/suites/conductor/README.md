# Conductor Robot suites

Two Robot suites that validate the Conductor payment switch beyond functional
correctness (the functional/release-gate coverage lives in the `payment_switch`
e2e). Both bring up a Mixer + a single `rack-switch` Rack and drive it with the
`iso8583-tool` simulators (`scheme` hosts and `auth` terminals).

## `stress_load.robot`

High-concurrency, multi-scheme load. Many terminals send a random mix of BIN4
(approve) and BIN5 (decline) authorizations as fast as possible. Asserts the
core correlation guarantee holds under load: every reply carries its own STAN
(no cross-wiring), `ok == total`, `failed == 0`, and both DE39 outcomes execute.
The JSON report records achieved TPS and latency percentiles.

Reference numbers (M2, embedded observability): ~9,000 txns, ~1,000-1,800 TPS,
p99 ~30-130ms, 0 cross-wired.

## `chaos.robot`

Communication-failure / chaos. Both scheme uplinks run through Toxiproxy
(`:20001` / `:20002`). While mixed load flows, one scheme's link is cut and then
restored. Asserts the switch:

- degrades gracefully: the affected BIN sees declines (link-state `no_destination`
  or timeout), never a dropped terminal (`failed == 0`);
- stays isolated: the other BIN is unaffected (all approvals);
- never cross-wires a reply, in any phase;
- recovers: the affected BIN returns to approvals once the link is restored.

## Requirements

- `toxiproxy-server` on `PATH` (chaos suite only). macOS: `brew install toxiproxy`.
- Python deps from `test/robot/requirements.txt` (installed by `run.sh` into
  `.venv`), including `toxiproxy-python`.

## Running

```sh
cd test/robot
./run.sh suites/conductor                    # both suites
./run.sh suites/conductor/stress_load.robot  # just stress
./run.sh suites/conductor/chaos.robot        # just chaos
```
