# Telemetry E2E Test

**Test ID**: 30
**Target**: `test/e2e_telemetry/run.sh`

## Objective
Verify the complete telemetry pipeline, from OTel SDK generation in the Rack to persistent storage in the Mixer.

## Scope
1.  **Generation**: Rack generates Logs and Metrics via OpenTelemetry SDK.
2.  **Transport**: Data flows via NATS JetStream (snake protocol).
3.  **Ingestion**: Mixer receives data via `TelemetrySink`.
4.  **Storage**: Data is persisted to DuckDB (and Parquet for archival).
5.  **Access**: Data is queryable via API and CLI.

## Execution
```bash
./test/e2e_telemetry/run.sh
```

## Success Criteria
- Rack registers successfully.
- Application generates logs and metrics (Counter `heartbeats_sent`).
- `fluxrig logs` returns at least 5 logs (including debug rack logs).
- `fluxrig metrics` returns `heartbeats_sent`.
- Data verification confirms correct entity names and values.
