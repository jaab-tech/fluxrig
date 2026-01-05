# Telemetry E2E Test

## Objective
Verify the complete telemetry pipeline: OTel SDK generation in the Rack, transport via NATS JetStream (Snake), ingestion by Mixer, and persistent storage in DuckDB/Parquet.

## Verifications

### Generation
- Rack generates Logs via OpenTelemetry SDK.
- Rack generates Metrics (Counter `heartbeats_sent`).

### Transport & Ingestion
- Telemetry data flows via Snake protocol.
- Mixer receives and ingests data via `TelemetrySink`.

### Storage
- Logs persisted to Parquet files.
- Registry data persisted to DuckDB.

### Access
- `fluxrig logs` CLI returns logs.
- `fluxrig metrics` CLI returns metrics.

## Expected Results
- ✅ Rack registers successfully.
- ✅ Parquet log files generated in `telemetry/logs/`.
- ✅ `fluxrig logs` returns at least 5 log entries.
- ✅ `fluxrig metrics` returns `heartbeats_sent` metric.
- ✅ Registry contains correct counts (1 Mixer, 1 Rack, 1 Snake).

## Usage
```bash
./test/e2e_telemetry/run.sh
```

## Workspaces
Tests run in ephemeral `work_*` folders.
The `work` symlink points to the latest execution for easy debugging.

## Files
| File | Description |
|------|-------------|
| `run.sh` | Main execution script |
| `mixer/mixer.toml` | Mixer configuration (Port 8092) |
| `rack/rack.toml` | Rack configuration |
| `work/*/data/` | Generated telemetry data |
| `work/*/logs/` | Execution logs |
