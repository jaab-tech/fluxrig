# Simple E2E Test

## Objective
Verify baseline FluxRig system functionality: Key Generation, Rack Registration, Passport Issuance, and CLI Interoperability.

## Verifications
1. **Key Generation**: Creates a cluster key pair.
2. **Mixer Startup**: Mixer starts and exposes Health API.
3. **Rack Registration**: Rack connects via Snake and registers with Mixer.
4. **Passport Issuance**: Rack receives and saves a signed Passport (`state.flux`).
5. **CLI Verification**: `fluxrig admin racks list` shows the registered rack.
6. **DB Verification**: Rack entry exists in the Registry (DuckDB).

## Expected Results
- ✅ Mixer is UP (Health API returns 200).
- ✅ Rack appears in `/api/v1/racks` within 30s.
- ✅ `state.flux` file exists and is valid (signature verified).
- ✅ ClusterID is populated (auto-generated or configured).
- ✅ CLI output includes rack name.
- ✅ DB contains rack entry with correct `machine_id`.

## Usage
```bash
./test/e2e_simple/run.sh
```

## Workspaces
Tests run in ephemeral `work_*` folders (e.g., `work_simple_20250101_120000`).
The `work` symlink points to the latest execution for easy debugging.

## Files
| File | Description |
|------|-------------|
| `run.sh` | Main execution script |
| `mixer/mixer.toml` | Mixer configuration |
| `rack/rack.toml` | Rack configuration |
| `work/mixer/data/` | Generated state and keys |
| `work/mixer/logs/` | Execution logs |
