<!-- Copyright (c) 2025 JAAB Tech SAS, Uruguay All Rights Reserved -->
<!-- See https://jaab.tech -->

# fluxrig

<p align="center">
  <a href="https://fluxrig.org">
    <img src="https://fluxrig.org/assets/fluxrig_logo.svg" alt="fluxrig Logo" width="600">
  </a>
</p>

> **The Universal Patchbay for Business Logic and Protocol Orchestration.**

**fluxrig** is a high-performance, open-source platform designed to route, transform, and simulate business data streams across any protocol.

Whether you are modernizing legacy payments (ISO8583), building IoT fleets (Protobuf), or orchestrating microservices (JSON), **fluxrig** provides a unified "rig" to wire your systems together.

## Features

*   **Protocol Agnostic**: Native support for ISO8583, JSON, XML, Protobuf, and raw binary.
*   **Smart Orchestration**: Route traffic based on business payload contents.
*   **High Performance**: "Split-Brain" architecture with a stateless, low-latency Rack (Go/Wasm) and a stateful Mixer.
*   **Wasm Extensibility**: Write custom logic in Go, Rust, or TypeScript without recompiling the Rig.
*   **Ops-Ready**: Hot-Reload, Distributed Tracing (OTel), and Metrics out of the box.

## Getting Started

### Prerequisites
*   Go 1.23+
*   Make

### Build & Run

1.  **Build**:
    ```bash
    make build
    ```
    Binaries will be placed in `bin/`.

2.  **Basic Usage**:
    ```bash
    ./bin/fluxrig --help
    ```

## Documentation

Full documentation is available at **[fluxrig.org](https://fluxrig.org)**.

## Contributing

We welcome contributions! Please see our [Contributing Guide](https://fluxrig.org/contributing).

## License

**fluxrig** is licensed under the Apache 2.0 License. See [LICENSE](LICENSE) for details.
