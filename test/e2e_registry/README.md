# Registry E2E Test

**Test ID**: 40
**Target**: `test/e2e_registry/run.sh`

## Objective
Verify the FluxRig Registry capabilities, focusing on identity assignment, topology tracking, and metadata synchronization.

## Scope
1.  **Identity Assignment**: Verify Rack registration and ID assignment.
2.  **Topology Tracking**: Verify Snake connection tracking (To Mixer / From Rack).
3.  **Metadata Sync**: Verify Rack version, attributes, and stats (IP, Port, Goroutines).

## Execution
```bash
./test/e2e_registry/run.sh
```

## Workspaces
Tests run in ephemeral `work_*` folders.
The `work` symlink points to the latest execution for easy debugging.

## Success Criteria
- Rack registers successfully.
- DuckDB `registry` table contains exactly 1 Mixer, 1 Rack, 1 Snake.
- Snake attributes correctly reflect topology (`to_mixer`, `from_rack`).
- Rack attributes contain `ip`.
- Rack version matches binary version.
