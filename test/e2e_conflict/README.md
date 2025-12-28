# Conflict & Identity E2E Test

**Objective:**
Verify the Registry's Identity Management rules, specifically concerning Name Uniqueness, Active Session Protection, and Zero-Config Auto-Scaling.

**Objective ID:** 15 (Conflict Handling)

## Scenarios

### 1. Active Conflict (Protection)
**Goal**: Verify that a second Rack cannot claim the identity of an **Active** Rack.
1.  Start **Rack A** (`name="rack-shared"`).
2.  Wait for Rack A to Register and send Heartbeat.
3.  Start **Rack B** (`name="rack-shared"`, diff data dir) immediately.
4.  **Expectation**: Rack B fails to register (Mixer returns 409 Conflict).

### 2. Session Recovery (Identity Reclaim)
**Goal**: Verify that a Rack can reclaim its identity after a crash/restart.
1.  Kill **Rack A**.
2.  Wait for Session Timeout (simulate or force inactive status). (Or simply start B, but B must succeed if A is dead? No, protection is based on LastSeen).
    *   *Test Adjustment*: We might need to manually expire the session in DB or wait 30s. For speed, we might assume "Active" means < 5s heartbeat?
    *   *Refined Rule*: If Rack A is dead (process gone), it stops heartbeating. If we wait > 30s, B initiates Recovery.
3.  Start **Rack B** (`name="rack-shared"`).
4.  **Expectation**: Rack B successfully registers and receives the **Same MachineID** as Rack A.

### 3. Zero-Config (Anonymous)
**Goal**: Verify that Racks without names are treated as "Cattle" (Auto-Scale).
1.  Start **Rack C** (No Name / Empty).
2.  **Expectation**: Rack C registers successfully.
3.  **Check**: Rack C Name is `node-<ID>` (e.g., `node-2`).
4.  Start **Rack D** (No Name).
5.  **Expectation**: Rack D registers successfully.
6.  **Check**: Rack D Name is `node-<ID+1>` (e.g., `node-3`). IDs are unique.

## Files
- `run.sh`: Main execution script.
- `mixer.toml`: Mixer config.
- `logs/`: Output logs.
