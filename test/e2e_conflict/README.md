# Conflict & Identity E2E Test

## Objective
Verify the Registry's Identity Management rules: Name Uniqueness, Active Session Protection, Session Recovery, and Zero-Config Auto-Scaling.

## Verifications

### Scenario 1: Active Conflict (Security)
- Start Rack A with name `rack-shared` → Registers successfully.
- Start Rack B with same name (no secret) → Rejected (hijack attempt).

### Scenario 2: Session Recovery
- Kill Rack A.
- Restart Rack A with cached passport → Recovers session.

### Scenario 3: Zero-Config (Cattle)
- Start Rack C with `prefix="probes-"` → Assigned name like `probes-XXXX`.
- Start Rack D with same prefix → Gets unique ID.

### Scenario 4: Default Zero-Config
- Start Rack E with no prefix → Assigned name like `node-XXXX`.

## Expected Results
- ✅ Rack A registers successfully.
- ✅ Rack B is rejected (no passport issued).
- ✅ Rack A recovers session from cached passport.
- ✅ Rack C and D get unique auto-generated names.
- ✅ Rack E gets default `node-` prefix.

## Usage
```bash
./test/e2e_conflict/run.sh
```

## Workspaces
Tests run in ephemeral `work_*` folders.
The `work` symlink points to the latest execution for easy debugging.

## Files
| File | Description |
|------|-------------|
| `run.sh` | Main execution script |
| `mixer/mixer.toml` | Mixer configuration |
| `rack_*/rack.toml` | Rack configurations (A-E) |
| `work/*/logs/` | Execution logs |
