# Roaming enrichment Robot suite

Enriching an ISO 8583 authorization with a network signal the message does not
carry: the handset's roaming country, asked of a mobile operator while the
payment waits.

The suite is the worked example behind the
[tutorial](https://fluxrig.org/docs/tutorials/roaming_enrichment), and it is the
proof the tutorial's claims are true rather than plausible.

## `roaming.robot` (Functional)

Eleven tests over one topology. Four cover the comparison itself: a handset in
the merchant's country, a handset elsewhere, a country code that covers several
countries, and an empty country list, which is its own outcome and not a
mismatch. Four cover the ways the signal can be missing: a reply without country
fields, an operator slower than the time budget, an operator error, and a card
absent from the enrolment snapshot.

The last three are the ones worth reading, because they assert what must **not**
happen. Every authorization is approved regardless of the signal, so enrichment
never becomes a decline. The reply to the scheme carries no private field. And
the phone number reaches neither the wire nor the logs.

## `stress_load.robot` (Load / Correlation)

Three load cases over the **exact enrichment path shipped in the public example**
(`examples/scenarios/roaming_enrichment.yaml` with `await_store: false`, which
spawns one unbounded goroutine per message):

| Test | Shape | What it proves |
|:---|:---|:---|
| Sustained Load - Match Outcome | 2 conn, 10 TPS, 10s, one PAN | The match path stays correlated at a steady rate |
| Concurrent Multi-Outcome | 7 terminals x 30 conn x 60 txns | Correlation holds while every telco outcome runs at once |
| High Concurrency | 7 terminals x 60 conn x 60 txns | Peak goroutine pressure on Coat Check with the write detached |

The correlation key is `(mti_class, DE 41, DE 11)`, so each terminal in a fleet
gets its own STAN block: sharing one would make the keys collide and route a
reply to the wrong terminal. The fleet also fails if any terminal does not
finish, since a killed terminal writes no report and would otherwise pass
silently on zero work.

Every case asserts `ok == total`, `cross_wired == 0` and `failed == 0`. The
multi-outcome case also asserts that both a match (`RS00`) and a mismatch
(`RS01`) came back, proving the compare-countries path ran under load, not only
the no-signal fallbacks. Enrichment never declines, so every reply is `DE39=00`;
the outcome travels in `DE 42`, where the issuer reports it.

## Topology

Four participants, and only two of them are fluxrig:

| Participant | What runs it | Role |
|:---|:---|:---|
| Scheme | `ISO8583Library`, from Robot | Sends the authorization |
| `rack-fluxrig` | fluxrig | The enrichment under test |
| `rack-telco` | fluxrig | Simulates the operator's CAMARA endpoint |
| Issuer | `iso8583-tool` | Authorizes, and reports what it received |

The telco is a separate Rack on purpose: a suite where fluxrig plays every
counterparty proves less than one where the calls cross a real socket. It
answers `POST /retrieve`, the operation the
[CAMARA Device Roaming Status API](../../../../examples/reference/camara/PROVENANCE.md)
defines.

The suite runs in clear text to keep the example readable. Production carries
both the ISO 8583 and the REST legs over TLS.

## Requirements

- Python deps from `test/robot/requirements.txt`, installed by `run.sh` into
  `.venv`.
- `bin/iso8583-tool`, built by `make build-bin`.

## Running

```sh
make test-robot-roaming     # from the repository root (functional only)

cd test/robot               # or directly
./run.sh suites/roaming     # functional
./run.sh suites/roaming/stress_load.robot   # stress
```
