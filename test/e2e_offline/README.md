# Offline Mode E2E Test

## Objective
Verify the Rack's ability to operate in Offline Mode using a cached passport when the Mixer is unreachable.

## Verifications

### Phase 1: Online Enrollment
- Start Mixer and Rack.
- Rack acquires and saves a Passport (`state.flux`).
- Rack sends heartbeats (online activity).

### Phase 2: Offline Startup
- Stop Mixer and Rack completely.
- Restart Rack **only** (Mixer is dead).
- Rack detects offline mode and continues running.

## Expected Results
- ✅ Passport acquired during online phase.
- ✅ Heartbeats sent during online phase.
- ✅ Rack loads cached passport on restart.
- ✅ Rack detects "Offline Mode".
- ✅ Rack process stays alive (doesn't crash).

## Usage
```bash
./test/e2e_offline/run.sh
```

## Workspaces
Tests run in ephemeral `work_*` folders.
The `work` symlink points to the latest execution for easy debugging.

## Files
| File | Description |
|------|-------------|
| `run.sh` | Main execution script |
| `mixer/mixer.toml` | Mixer configuration |
| `rack/rack.toml` | Rack configuration |
| `work/*/data/` | Persisted state (`state.flux`, keys) |
| `work/*/logs/` | Execution logs |
