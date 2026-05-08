<!-- Copyright (c) 2025 JAAB Tech SAS, Uruguay All Rights Reserved -->
<!-- See https://jaab.tech -->

# Robot Framework Validation Suite

## 🧪 Overview
This directory contains the **comprehensive validation suites** for fluxrig, covering Business Logic, Topology, and Resilience. Unlike the bash-based regression tests (which focus on local process/CLI mechanics), these tests validate the **system behavior** as a whole.

## 📂 Structure

| Directory | Content |
| :--- | :--- |
| **`suites/`** | The actual Test Cases organized by domain. |
| **`lib/`** | Custom Python libraries (`fluxrigLibrary`, `NATSLibrary`) extending Robot capabilities. |
| **`resources/`** | Reusable keywords (`setup.resource`, `chaos.resource`). |

## 📚 Terminology
*   **Test Case**: A specific validation unit (e.g., "Verify QoS Priority").
*   **Suite**: A file containing multiple related Test Cases.
*   **Scenario**: **Reserved** for fluxrig Topology definitions (`scenario.yaml`). We do NOT use this word for tests.

## 🚀 Running Tests
Use the helper script to run tests in a clean virtual environment:

```bash
# Run all suites
./run.sh

# Run specific suite
./run.sh suites/topology/
```
