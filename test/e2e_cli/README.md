# CLI & Admin E2E Test

## Objective
Verify the FluxRig CLI's administrative capabilities, helper commands, and API interactions.

## Verifications

### Helper Commands
- `fluxrig version` displays correct version info.
- `fluxrig help` shows usage information.
- `fluxrig keys gen-cluster` generates a valid key pair.

### API Interaction
- Rack registers successfully via Snake.
- `/api/v1/racks` returns the registered rack.

### Admin Commands
- `fluxrig admin racks list` shows registered racks.
- `fluxrig admin racks remove` removes a rack.
- Removal is verified via API and DB.

## Expected Results
- ✅ Version command outputs version, commit, and build date.
- ✅ Help command lists available subcommands.
- ✅ Key generation creates cluster.key and cluster.key.pub.
- ✅ Rack appears in list after registration.
- ✅ Rack is removed successfully (API returns 404 after removal).
- ✅ DB no longer contains the removed rack.

## Usage
```bash
./test/e2e_cli/run.sh
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
| `work/cli/` | CLI test artifacts |
| `work/*/logs/` | Execution logs |
