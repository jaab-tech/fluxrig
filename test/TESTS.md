<!-- Copyright (c) 2025 JAAB Tech SAS, Uruguay All Rights Reserved -->
<!-- See https://jaab.tech -->

# fluxrig Testing Strategy

We employ a **Hybrid Testing Strategy** to ensure both infrastructure stability and business logic correctness.

## 1. Infrastructure Regression (Bash)
*   **Location**: `test/e2e_*`
*   **Focus**: Local process lifecycle, CLI admin commands, File rotation, OS Signals.
*   **Tooling**: Pure Bash scripts.
*   **Execution**: `make regression`

## 2. Business & Resilience Validation (Robot Framework)
*   **Location**: `test/robot/`
*   **Focus**: Distributed Topology (Multi-Rack), QoS, Network Chaos, ISO8583 Logic.
*   **Tooling**: Robot Framework (Python) + Jinja2 Reporting.
*   **Execution**:
    *   Full Suite: `make robot`
    *   Single Suite: `./test/robot/run.sh test/robot/suites/topology/cross_rack.robot`
*   **Features**:
    *   **Dashboard Reports**: HTML-based visual reporting of Registry and Logs (Jinja2).
    *   **Log Parity**: Automated comparison of physical log files vs Parquet telemetry.
*   **Documentation**: [Topology Suite README](robot/suites/topology/README.md)

## 3. Unit Tests (Go)
*   **Location**: `**/*_test.go`
*   **Focus**: Function-level correctness, Mocked interfaces.
*   **Execution**: `make test`

---
**Note on Terminology**:
*   **Scenario**: Refers **only** to the fluxrig Topology Definition (YAML).
*   **Test Case**: Refers to a specific validation step in Robot Framework.
