# Simple E2E Test

**Objective:**
Verifies the baseline functionality of the FluxRig system:
1.  **Key Generation:** Creates a cluster key.
2.  **Enrollment:** Starts Mixer and Rack, verifying the Rack successfully registers and receives a passport (`state.flux`).
3.  **CLI Verification:** checks that `fluxrig cli` can list the registered rack.

**Objective ID:** 12 (Baseline Verification)

**Files:**
- `run.sh`: Main execution script.
- `mixer.toml`: Mixer configuration.
- `rack.toml`: Rack configuration.
- `data/`: Generated state and keys.
- `logs/`: Execution logs (`mixer.log`, `rack.log`).

**Usage:**
```bash
./test/e2e_simple/run.sh
```
