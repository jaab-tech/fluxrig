# Changelog

All notable changes to this project will be documented in this file.

## [0.2.0-dev] - Unreleased

### Added
- **Phase 3 Development**: Started work on Gear Ecosystem.

## [v0.1.0-alpha] - 2025-12-27

### Added
- **Core Foundation**: Complete Mixer/Rack architecture.
- **Transport**: NATS JetStream integration via Watermill.
- **Registry**: DuckDB embedded registry with telemetry sink.
- **REST API**: 8 Control Plane endpoints (`/racks`, `/health`, etc.).
- **CLI**: `fluxrig` command with `keys`, `admin`, `logs`, `metrics` subcommands.
- **Telemetry**: OpenTelemetry SDK embedded with DuckDB exporter.
- **Security**: Ed25519 identity, StateEnvelope signing, Snake Tunnel.
- **Docs**: Comprehensive architecture, data, and security documentation.
- **CI**: GitHub Actions workflow for lint/test/build.

### Changed
- **Architecture**: Moved from Proof-of-Concept to Clean Slate architecture.
- **Coverage**: Achieved 70% unit test coverage.
