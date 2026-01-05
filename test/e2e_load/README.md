# E2E Load Test (Bento)

## Objective
Verify the Native Bento Gear integration and system performance under load. This test uses Bento's internal generator to produce messages and verify end-to-end delivery through the FluxRig pipeline.

## Verifications
1.  **Bento Loading**: `bento-gen` gear generates messages at a configured rate.
2.  **Bridging**: Messages are converted to `FluxMsg` and passed through the FluxRig internal bus.
3.  **Bento Sink**: `FluxMsg` is converted back to Bento format by `bento-sink` gear.
4.  **Payload Integrity**: A dynamic payload with counter is verified in the output file.
5.  **Metrics**: (Future) Verify throughput and latency via Telemetry.

## Scenarios
### 1. Smoke Test (Current)
- **Rate**: 1 msg/sec
- **Count**: 5 messages
- **Verify**: Output file exists and contains 5 lines.

## Expected Results
- ✅ Mixer and Rack start successfully.
- ✅ Scenario applies without error.
- ✅ Output file `/tmp/flux_e2e_load_out.txt` contains 5 JSON messages.
- ✅ "process_id" in log output increments.

## Usage
```bash
./test/e2e_load/run.sh
```

## Configs
- **Mixer**: `mixer/fluxrig.toml` (Port 9115, Snake 4225)
- **Rack**: `rack/fluxrig.toml` (Links to Mixer 9115)
- **Scenario**: `scenario.yaml` (Bento Logic)
