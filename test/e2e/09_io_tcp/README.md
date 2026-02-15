<!-- Copyright (c) 2025 JAAB Tech SAS, Uruguay All Rights Reserved -->
<!-- See https://jaab.tech -->

# Simple TCP Gear E2E Test

## Objective
Verify the production readiness of the Native TCP Gear (`io_tcp`) within the FluxRig Runtime.

## Verifications

### Scenario Loading
- Mixer imports and activates scenario via API.
- Rack receives scenario via Snake push.
- Gears start and bind to configured ports.

### I/O Verification
- Gateway gear binds to `:9001`.
- Send `TEST_MSG` via netcat.
- Receive echo response.

### Traceability
- `FluxMsg` contains valid `flux_id` with correct MachineID.
- `flux_path` contains persistent Port IDs (from Mixer).
- All IDs logged in `0x` hex format.

### Registry Persistence
- Gears, Ports, Wires registered in DuckDB.
- Entity types correctly mapped.
### Distributed Tracing (Observability)
- **Context Propagation**: Verify that OTel spans are passed across the NATS bus.
- **Span Linking**: Verify that the "Process" span has a valid `parent_span_id` linking back to the "Source" span.
- **Validation**:
    - Query Parquet `spans` table.
    - Assert `count(parent_span_id != '') > 0`.

## Expected Results
- ✅ Mixer is UP (Health API returns 200).
- ✅ Topology synchronized (`sync_status: synchronized`).
- ✅ I/O Verification PASSED (Echo receives `TEST_MSG`).
- ✅ Registry contains Gear, Ports (In/Out), Wire, Snake, Scenario.
- ✅ Distributed Tracing PASSED (Spans are linked via `parent_span_id`).

## Usage
```bash
./test/e2e_io_tcp/run.sh
```

## Files
| File | Description |
|------|-------------|
| `run.sh` | Main execution script |
| `scenario.yaml` | Test scenario definition |
| `mixer/fluxrig.toml` | Mixer configuration |
| `rack/fluxrig.toml` | Rack configuration |
| `work_*/` | Ephemeral test workspace (gitignored) |
