# Spec & Scenario Manager E2E Test Suite

End-to-End integration tests for the **Spec Manager** (CLI) and **Scenario Controller** (Mixer API).

## What Is Tested

### 1. CLI Spec Lifecycle (Local Store)

Tests the `fluxrig spec` CLI operating directly on a local CAS (Content-Addressable Store):

| Test | Description |
|------|-------------|
| **Import** | Import a spec file with explicit `--name` and `--tag` |
| **Idempotency** | Re-importing the same content/version is a safe no-op |
| **Invalid Version** | Importing with a non-semver tag is rejected |
| **Conflict Detection** | Overwriting an existing version with different content fails (Immutable History) |
| **List** | Stored specs are correctly listed via CLI |

### 2. API Scenario Lifecycle (Mixer Integration)

Starts a live **Mixer** process and tests the HTTP API:

| Test | Description |
|------|-------------|
| **Activation needs an enrolled Rack** | `POST /api/v1/scenario/import?activate=true` for a scenario that deploys to `rack-1`, before `rack-1` exists, answers `409`. The scenario stays filed and none becomes active |
| **Scenario Import and Activation** | With `rack-1` enrolled (the Mixer adopts it), the same request answers `200` |
| **Topology Status** | `GET /api/v1/topology/status` returns `active_ver` and `sync_status` |
| **Persistence** | Scenario is written to disk (verifies `scenarios/active` file exists) |

The test checks the Mixer side only. The scenario names a spec (`visa:v1.0.0`) that lives in the Mixer's store, so the Rack logs that it could not apply it; nothing here asserts on the Rack's runtime.

### 3. Concurrent Access (CLI + Mixer)

Validates that the CLI and Mixer can safely operate on the **same data directory** simultaneously:

| Test | Description |
|------|-------------|
| **CLI Import During Mixer Runtime** | Import a new spec via CLI while Mixer is actively serving |
| **Mixer Stability** | Mixer process remains alive after concurrent CLI access |

## How to Run

From the project root:

```bash
./test/e2e/12_specs/run.sh
```

The script automatically builds all binaries (`fluxrig`, `fluxrig-mixer`) before running.

## Structure

| Path | Description |
|------|-------------|
| `run.sh` | Main test runner |
| `scenarios/visa.yaml` | Standard spec (v1.0.0) |
| `scenarios/visa_mod.yaml` | Modified content (conflict detection) |
| `scenarios/mc.yaml` | Spec with internal metadata |
| `scenarios/scenario_v1.yaml` | Scenario referencing `visa:v1.0.0` with `codec_iso8583` gear |
| `work` → `/tmp/fluxrig/work_specs_*` | Symlink to the latest workspace (auto-created) |

## Dependencies

- `test/e2e/utils/e2e_utils.sh` — Shared utility functions (logging, port wait, cleanup)
- `make build-bin` — Automatically invoked by `run.sh`
