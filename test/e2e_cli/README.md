# CLI & Admin E2E Test

**Objective:**
Verifies the FluxRig CLI's administrative capabilities and correctness.
1.  **CLI Basics:** Checks version, help, and key generation commands.
2.  **API interaction:** Verifies successful Rack registration via API polling.
3.  **Admin Commands:** Uses `fluxrig admin` to list and remove racks, verifying against the database/API.

**Objective ID:** 27 (CLI Verification)

**Files:**
- `run.sh`: Main execution script.
- `mixer.toml`: Mixer configuration.
- `data/`: Persisted state.
- `logs/`: Execution logs.

**Usage:**
```bash
./test/e2e_cli/run.sh
```
