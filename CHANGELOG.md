---
id: changelog_project
title: Changelog
---

# Changelog

[![Keep a Changelog](https://img.shields.io/badge/changelog-Keep%20a%20Changelog%201.0.0-orange.svg)](https://keepachangelog.com/en/1.0.0/)
[![Semantic Versioning](https://img.shields.io/badge/semver-2.0.0-blue.svg)](https://semver.org/)

All notable changes to the **FluxRig** project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [v0.4.1] - 2026-02-09
### Added
- **Coat Check Pattern**: Implemented architectural pattern to handle "Detached State" during connection handovers.
- **IO Refactor**: Decoupled TCP connection management from protocol logic.

## [v0.4.0] - 2026-02-01
### Added
- **ISO8583 Native Gear (Alpha)**: First release of the high-performance payment switch gear.
- **Telemetry Governor**: Introduced QoS constraints for telemetry ingress to protect business traffic.

## [v0.3.0] - 2026-01-05
### Added
- **Bento Integration**: Native support for the `warpstreamlabs/bento` ecosystem, enabling 100+ I/O connectors (AWS, SQL, Kafka, File).
- **Load Testing Suite**: Integrated `e2e_load` capabilities for stress testing.

## [v0.2.0] - 2026-01-08
### Added
- **Observability Stack**: Full OTel integration (Metrics, Traces) with DuckDB backend.
- **Configuration V2**: Unified TOML-based configuration schema.
- **TLS Support**: Enabled mutual TLS for internal bus and HTTPS for management API.

## [v0.1.0] - 2025-12-28
### Added
- **Foundation**: Initial release of the 4-Repo Architecture.
- **Snake Protocol**: Secure tunneling implementation for Rack-to-Mixer connectivity.
- **FluxMsg**: Canonical JSON schema for inter-gear communication.

[Unreleased]: https://github.com/jaab-tech/fluxrig/compare/v0.4.1...HEAD
[v0.4.1]: https://github.com/jaab-tech/fluxrig/releases/tag/v0.4.1
[v0.4.0]: https://github.com/jaab-tech/fluxrig/releases/tag/v0.4.0
[v0.3.0]: https://github.com/jaab-tech/fluxrig/releases/tag/v0.3.0
[v0.2.0]: https://github.com/jaab-tech/fluxrig/releases/tag/v0.2.0
[v0.1.0]: https://github.com/jaab-tech/fluxrig/releases/tag/v0.1.0
