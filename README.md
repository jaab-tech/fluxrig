<!-- Copyright (c) 2025-2026 JAAB Tech SAS, Uruguay All Rights Reserved -->
<!-- See https://jaab.tech -->

# fluxrig

<p align="center">
  <a href="https://fluxrig.org">
    <img src="https://fluxrig.org/assets/fluxrig_logo.svg" alt="fluxrig Logo" width="600">
  </a>
</p>

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-Apache%202.0-blue.svg" alt="License"></a>
  <img src="https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go&logoColor=white" alt="Go Version">
  <img src="https://img.shields.io/badge/Version-v0.5.0--dev-green" alt="Version">
</p>

> **The high-performance connectivity and protocol orchestration platform for distributed mission-critical infrastructure.**

**fluxrig** is an open-source engine designed to route, transform, and monitor data streams across heterogeneous environments. Whether you are modernizing legacy payment systems (ISO8583), orchestrating industrial IoT fleets (Modbus/MQTT), or building high-speed microservice bridges (JSON/Protobuf), **fluxrig** provides a unified control plane to harmonize your data flow.

## Key Capabilities

*   **Protocol Agnostic**: Native support for ISO8583, JSON, XML, Protobuf, and raw binary protocols.
*   **Edge Autonomy**: Distributed agents (Racks) that process logic independently, ensuring business continuity during network failures.
*   **Unified Control**: A centralized Mixer for fleet-wide policy, identity management, and real-time telemetry.
*   **High-Fidelity Observability**: Native OpenTelemetry (OTel) integration for deep tracing and metrics without sidecars.
*   **Extensible Logic**: Custom processing modules (Gears) written in Go or WebAssembly (Wasm).

## Core Architecture

fluxrig uses a modular architecture inspired by the precision of a **Recording Studio** and the scale of a **Live Event Stage**:

1.  **The Mixer**: The central Front of House (FOH) control plane and entity registry.
2.  **The Rack**: The edge node agent that hosts and executes processing logic (Gears).
3.  **The Gear**: Modular units of logic (codecs, adapters, transformations) that run inside a Rack.

## Getting Started

> [!TIP]
> Want to see it in action immediately? Check out the **[5-Minute Quickstart](https://fluxrig.org/docs/tutorials/quickstart)** guide!

### Prerequisites
*   **Go 1.26+**
*   **Make**
*   **GCC/Clang** (Required for Mixer/DuckDB)
*   **golangci-lint** (For contributors)

### 1. Build from source
```bash
git clone https://github.com/jaab-tech/fluxrig.git
cd fluxrig
make build
```

### 2. Start the Control Plane
```bash
# Starts the Mixer and imports the getting_started scenario
./bin/fluxrig-mixer --auto-adopt examples/scenarios/getting_started.yaml
```

### 3. Connect an Edge Node
Open a new terminal and start the edge agent:
```bash
./bin/fluxrig rack
```

For the complete breakdown of this topology, how the declarative `bento` configuration works, and how to query the real-time telemetry, please follow the **[5-Minute Quickstart](https://fluxrig.org/docs/tutorials/quickstart)**.

### Automated Testing
For complete end-to-end working topologies, refer to our automated test suites:
*   **[test/e2e/](test/e2e/)**: High-fidelity Go-based integration scenarios (ISO8583, Telemetry, TLS).
*   **[test/robot/](test/robot/)**: Comprehensive acceptance tests and performance benchmarks.

## Documentation

Full documentation, including architecture guides and Gear references, is available at **[fluxrig.org/docs](https://fluxrig.org/docs)**.

## Contributing

We welcome contributions! Please see our [Contributing Guide](CONTRIBUTING.md) and [Code of Conduct](CODE_OF_CONDUCT.md).

## Community

*   **Discussions**: [GitHub Discussions](https://github.com/jaab-tech/fluxrig/discussions) — Questions, ideas, announcements.
*   **Discord**: Join our engineering community on [Discord](https://discord.gg/drwSCCWFV7).

## Acknowledgments

This project was developed with significant assistance from AI coding tools, including [Antigravity](https://deepmind.google/) (Google DeepMind), [Claude](https://anthropic.com/) (Anthropic), [Gemini](https://deepmind.google/technologies/gemini/) (Google), [Cursor](https://cursor.com/), and [GitHub Copilot](https://github.com/features/copilot). See [NOTICE](NOTICE) for full details.

## License

**fluxrig** is licensed under the Apache 2.0 License. See [LICENSE](LICENSE) for details.

Copyright (c) 2025-2026 [JAAB Tech SAS](https://jaab.tech), Uruguay.
