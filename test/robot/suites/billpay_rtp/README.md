# billpay_rtp: the ISO 8583 to ISO 20022 bridge

Runs the tutorial `iso8583_iso20022_bridge` as written, and fails when the
tutorial and the code disagree.

```bash
make test-robot-billpay
```

## What it proves

| Case | The terminal gets | Path |
| :--- | :--- | :--- |
| A payment the bank accepts | `0210`, DE 39 `00` | 0200 → pacs.008 → rail → pacs.002 `ACTC` → 0210 |
| A payment over the bank limit (1000.00) | `0210`, DE 39 `13` | pacs.002 `RJCT`/`AM02` through the translation table |
| An unknown biller | `0210`, DE 39 `03` | `to-rail` rejects on its `error` port before the rail; the decline answers |
| The rail is down | `0210`, DE 39 `91` | `rail` reports its link down; the conductor sends the request to the decline at once |

## What it uses

- `examples/scenarios/billpay_rtp.yaml`, `examples/specs/` and
  `examples/lookups/billers.yaml`, unchanged. The suite has no copy of them.
- The Mixer API for every artefact: the specs and the records are uploaded with
  `POST /api/v1/specs` in the tutorial's order, and no file is copied to a Rack.
  Each Rack receives the artefacts with the scenario.
- The tutorial's ports: 8583 (terminals) and 7020 (the rail), with the Mixer on
  8090 and the Snake on 4222.

## What it does not prove

Throughput or latency. It sends one message per case, so it has no load and
no bound on time other than the tool's response timeout.
