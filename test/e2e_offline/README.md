# Offline Mode E2E Test

**Objective:**
Verifies the Rack's ability to operate in Offline Mode using a cached passport.
1.  **Online Phase:** Starts Mixer and Rack to acquire a passport (`state.flux`).
2.  **Stop Phase:** Kills Mixer and Rack.
3.  **Offline Phase:** Starts *only* the Rack.
4.  **Verification:** Checks if the Rack:
    - Loads the cached passport.
    - Detects offline mode (Mixer unreachable).
    - Continues running without crashing.

**Objective ID:** 16 (Offline Mode)

**Files:**
- `run.sh`: Main execution script.
- `mixer.toml`: Mixer configuration.
- `rack.toml`: Rack configuration.
- `data/`: Persisted state (`state.flux`, keys).
- `logs/`: Execution logs for both phases.

**Usage:**
```bash
./test/e2e_offline/run.sh
```
