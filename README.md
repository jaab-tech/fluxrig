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

> **The specialized protocol patchbay for mission-critical orchestration.**

**fluxrig** is a high-performance, open-source platform designed to route, transform, and simulate business data streams across any protocol.

Whether you are modernizing legacy payments (ISO8583), building IoT fleets (Protobuf), or orchestrating microservices (JSON), **fluxrig** provides a high-fidelity "Rig" to wire your systems together with sovereign precision.

## Key Concepts

*   **Protocol Agnostic**: Native support for ISO8583, JSON, XML, Protobuf, and raw binary.
*   **Hot-Path / Cold-Path Architecture**: A stateless, low-latency **Rack** (Go/Wasm) for data processing and a stateful **Mixer** for fleet control.
*   **Smart Orchestration**: Route traffic based on deep business payload inspection.
*   **Wasm Extensibility**: Write custom logic in Go, Rust, or TypeScript without recompiling the core engine.
*   **Ops-Ready**: Native OpenTelemetry (OTel) tracing, metrics, and hot-reload capabilities.

## Getting Started

### Prerequisites
*   **Go 1.25+**
*   **Make**
*   **GCC/Clang** (Required for Mixer/DuckDB)

### Build & Run

1.  **Build**:
    ```bash
    make build
    ```
    Binaries will be placed in `bin/`.

2.  **Basic Usage**:
    ```bash
    # Start the Mixer (Control Plane)
    ./bin/fluxrig-mixer
    
    # Start a Rack Agent
    ./bin/fluxrig agent start
    ```

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
