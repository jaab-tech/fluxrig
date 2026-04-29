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
  <img src="https://img.shields.io/badge/Version-v0.4.4-green" alt="Version">
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

FluxRig uses a modular architecture inspired by the precision of a **Recording Studio** and the scale of a **Live Event Stage**:

1.  **The Mixer**: The central Front of House (FOH) control plane and entity registry.
2.  **The Rack**: The edge node agent that hosts and executes processing logic (Gears).
3.  **The Gear**: Modular units of logic (codecs, adapters, transformations) that run inside a Rack.

## Getting Started

### Prerequisites
*   **Go 1.25+**
*   **Make**
*   **GCC/Clang** (Required for Mixer/DuckDB)
*   **golangci-lint** (For contributors)

### Build & Run

1.  **Build**:
    ```bash
    make build
    ```
    Binaries will be placed in `bin/`.

2.  **Run (Zero-Config)**:
    FluxRig is designed for "Zero-Config" first runs. The Mixer automatically generates a persistent Cluster Authority keypair if one is missing, ensuring that the control plane is immediately operational for development.

> [!TIP]
> **Global Gears**: In the `getting_started.yaml` example, gears do not have an explicit `deploy` target. These are treated as "Global Gears" and will automatically run on any Rack that connects to the Mixer.

    ```bash
    # 1. Optional: Perform a diagnostic check
    ./bin/fluxrig check

    # 2. Start the Mixer (Control Plane + Embedded NATS)
    ./bin/fluxrig-mixer --auto-adopt

    # 3. Start a Rack Agent (Edge Execution)
    ./bin/fluxrig rack
    ```

### Your First Scenario: "Hello World"

FluxRig uses YAML-based scenarios to define edge logic. Follow these steps to run a basic telemetry generator:

1.  **Start the Mixer with the Scenario**:
    The Mixer can load a scenario directly on startup as a positional argument (we use `--auto-adopt` here to bypass manual approval for this demo):
    ```bash
    ./bin/fluxrig-mixer --auto-adopt examples/scenarios/getting_started.yaml
    ```

2.  **Start a Rack Agent**:
    Open a new terminal and start the agent. It will connect to the Mixer and automatically pull the "Getting Started" logic:
    ```bash
    ./bin/fluxrig rack
    ```

3.  **Verify Execution**:
    The Rack will initialize the `bento` gear. You should see periodic "Hello from FluxRig" messages and random metrics in the Rack console.

### Advanced Configuration

For production environments, you can customize settings using TOML files or environment variables. To get started with a custom setup, copy the templates from the `examples/` directory to your project root:

```bash
cp examples/configs/fluxrig-mixer.toml.example fluxrig-mixer.toml
cp examples/configs/fluxrig.toml.example fluxrig.toml
```

See [examples/configs/](examples/configs/) for additional templates.

    **Security Keys**:
    The "Zero-Config" auto-generated key is perfect for local development. For production, you should explicitly generate and manage your keys using the `keys` command. This ensures full control over the key lifecycle and backup procedures.
    ```bash
    ./bin/fluxrig keys gen-cluster --dir ./my-secrets
    ```
    See the **[Security Reference](https://fluxrig.org/docs/reference/core/security)** for details on the "Offline Trust" model.

4.  **Working Examples**:
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
