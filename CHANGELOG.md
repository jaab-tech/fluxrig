---
id: changelog
slug: /changelog
title: Changelog
---

# Changelog

[![Keep a Changelog](https://img.shields.io/badge/changelog-Keep%20a%20Changelog%201.0.0-orange.svg)](https://keepachangelog.com/en/1.0.0/)
[![Semantic Versioning](https://img.shields.io/badge/semver-2.0.0-blue.svg)](https://semver.org/)

All notable changes to the **fluxrig** project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Phase 4: scale & hardening

| Version | Date | Status | Summary |
| :--- | :--- | :--- | :--- |
| [v0.8.0](#v080) | 2026-08-24 | Delivered | EMV chip data: BER-TLV parsing with unknown-tag preservation |
| [v0.7.1](#v071) | 2026-08-10 | Delivered | ISO 8583 TLV length hardening |
| [v0.7.0](#v070) | 2026-08-01 | Delivered | Payment switch: Conductor gear, gear manifests, ISO 8583 TLS |


## [v0.8.0] - 2026-08-24 {#v080}

The EMV release: fluxrig parses BER-TLV composite fields, and carries the tags it does not model through untouched.

### Added
- **BER-TLV composite parsing**: a field declared `structure: tlv`, such as ICC data in DE 55, is parsed into its EMV tags. Tags the spec does not declare are retained rather than discarded and re-emitted on encode, which is what forwarding to a scheme depends on, and are exposed to downstream gears under `iso8583.unknown_tags`. Validated by a round trip across a serialization boundary and by a Robot suite against a running Rack.
  - Tag order is canonical rather than preserved: tags are re-emitted sorted, so every value survives but the byte layout of a field may differ from the one that arrived.

### Changed
- **Binary field values cross the bus as bytes**: a field whose value is not valid UTF-8, such as chip data, PIN blocks or MACs, is placed on the `fluxMsg` as bytes rather than as a string, including when reached through an `alias` or as a composite subfield. Gears and pipelines reading such a field now receive `[]byte` where they received a `string`. Serialization requires it: a Go string holding binary is not valid CBOR text, so a spec declaring a binary field previously lost the message at the first rack boundary.

### Fixed
- **The bus no longer drops undecodable messages silently**: a message that cannot be decoded is logged with its subject and the reason, instead of being discarded without trace.


## [v0.7.1] - 2026-08-10 {#v071}

### Fixed
- **ISO 8583 TLV length hardening**: a crafted BER-TLV long-form length in a composite field (for example ICC data in DE 55) could wrap to a negative value, bypass the bounds check and crash the codec while skipping the unknown tag. Length decoding now rejects unsupported long forms, out-of-range values, invalid BCD length bytes and non-numeric ASCII lengths, so malformed input is reported as an error instead of failing the message.


## [v0.7.0] - 2026-08-01 {#v070}

The payment-switch release: the **Conductor** transaction switch, a manifest system that makes every gear self-describing, and native TLS on the ISO 8583 I/O path.

### Added
- **Conductor gear (transaction switch)**: routes each request across a destination tree of strategy nodes (`failover`, `round_robin`, `least_loaded`) whose leaves are output ports, correlates the reply under a ticket, and surfaces timeouts on a dedicated `error` port. Availability sensing binds each local destination to its uplink link-state. Validated end-to-end by the `payment_switch` e2e and the Conductor stress and chaos Robot suites.
- **Valet correlation engine**: the local-by-default ticket store behind the Conductor's reply matching, with per-ticket TTLs and idempotent redemption.
- **Cross-Conductor handoff**: a request can exit one Conductor and its reply return through another, routed by an in-band origin stamp, so active/active multi-region topologies need no shared session state.
- **Named multi-port gear I/O**: gears declare multiple named input/output ports; the Bento gear honors every declared output port and fails loudly on an ambiguous multi-output config.
- **Gear manifests**: every gear publishes a manifest (identity, ports, config JSON Schema, terminus). The runtime validates a gear's config against its schema at activation, the Mixer API serves the manifest catalog, and `fluxrig gears doc` generates the gear reference documentation from it.
- **Native TLS / mTLS on `io_iso8583`**, plus connection link-state signals (`conn.up` / `conn.down`) on the control plane that drive Conductor availability sensing.
- **`fluxrig scenario viz`**: generates an interactive LikeC4 topology model (zones, racks, gears, sockets, the Snake) from a scenario file.
- **Lean Rack build**: `-tags nobento` compiles a Rack without the Bento gear for minimal-footprint deployments.

### Changed
- **Port addressing**: port names are dot-free and wires address the fully-qualified `rack.gear.port`, so a wire endpoint is unambiguous across a multi-rack topology.

### Fixed
- **Reply-correlation timeout under load**: JetStream deduplication is now keyed per subject, fixing a Conductor timeout where distinct requests collided on the dedup key.
- **Rack stability**: panics on the gear `emit` path are recovered and nil emits dropped, so a misbehaving source gear can no longer crash the Rack.
- **Conductor field resolution**: correlation, match, and park fields resolve as dotted paths (e.g. `iso8583.field.11`) consistently.
- **Loud topology validation**: scenario topology inconsistencies now fail at load instead of surfacing later as runtime errors.
- **Gear schema completeness**: the `io_tcp` and `bento` config schemas now declare every field the gears actually accept (previously undocumented options such as `io_tcp` delimiter framing and `bento` `log_level`).

## Phase 3: open & flexible logic

| Version | Date | Status | Summary |
| :--- | :--- | :--- | :--- |
| [v0.6.1](#v061) | 2026-07-20 | Delivered | Wasm runtime, supply chain security, polyglot gears |
| [v0.6.0](#v060) | 2026-06-06 | Delivered | Release metadata only, no code changes |
| [v0.5.0](#v050) | 2026-05-07 | Delivered | Sovereign identity (UUID v7) & telemetry hardening |
| [v0.4.5](#v045) | 2026-04-29 | Delivered | Documentation Hardening & Zero-Config |
| [v0.4.4](#v044) | 2026-04-23 | Delivered | Logic Extensibility & Secure Enrollment |
| [v0.4.3](#v043) | 2026-02-19 | Delivered | Operational Resilience & NATS V2 |
| [v0.4.2](#v042) | 2026-02-15 | Delivered | Spec Management & E2E Automation |
| [v0.4.1](#v041) | 2026-02-09 | Delivered | Stateless Context & I/O Decoupling |
| [v0.4.0](#v040) | 2026-02-01 | Delivered | ISO8583 Native Gear & Telemetry QoS |
| [v0.3.0](#v030) | 2026-01-08 | Delivered | Bento Integration & Load Testing |

## [v0.6.1] - 2026-07-20 {#v061}
### Added
- **Wazero Integration**: Implemented a secure, native Wasm execution environment using `wazero`.
- **Wasm Supply Chain Security**: Embedded Ed25519 signatures within `.wasm` modules with Mixer-level trust roots and countersignature enforcement prior to Rack execution.
- **Dynamic Catalog Distribution**: Added NATS Snake hot-loading for edge distribution of Wasm logic.
- **PKI & Catalog CLI**: Introduced `fluxrig keys gen-cluster`, `fluxrig wasm sign`, and `fluxrig wasm import` commands.
- **Path Sanitization**: Added `pkg/utils/path` to centralize traversal-safe path handling.

## [v0.6.0] - 2026-06-06 {#v060}
### Changed
- Release metadata only. This tag contains no source changes relative to `v0.5.0`; the Wasm work intended for it was not merged and shipped in `v0.6.1` instead.

## [v0.5.0] - 2026-05-07 {#v050}
### Changed
- **Sovereign Identity Plane (v0.5.0 Foundation)**: Migrated the entire platform identity system to **128-bit UUID v7 (RFC 9562)**. This enhances entropy, ensures global uniqueness without centralized coordination, and provides time-ordered sequence integrity for high-performance storage indexes.
- **Deduplication Logic**: Updated NATS JetStream deduplication to utilize 128-bit identifiers, ensuring consistent exactly-once delivery across complex telemetry pipelines.
- **Telemetry Hardening**: Standardized the dotted metric naming schema (e.g., `flux.gear.messages_in`) across OTel, Prometheus, and DuckDB.
- **Directional Monitoring**: Split unified I/O counters into distinct Inbound and Outbound channels for precise protocol translation metrics.
- **Resource Guardrails**: Implemented mandatory `MaxHops` (64) and `MaxPayloadSize` (2MB) validation in `fluxmsg` to prevent bus exhaustion and "poison pill" scenarios.
- **Concurrency Resilience**: Integrated global `PanicMiddleware` to ensure Rack stability during individual Gear failures and hardened mutex locking for atomic hot-reloads.
- **Mixer Reliability**: Replaced fragile telemetry discovery with a robust recursive traversal engine, ensuring full visibility of historical Parquet data via the API.
- **Security Hardening (CodeQL Certification)**:
    - Fixed high-severity path traversal in scenario management by implementing robust name sanitization.
    - Hardened TLS configuration in the `snake` server with CA-based client verification support.
    - Resolved integer overflow/truncation risks in telemetry ingestion and ISO8583 codecs.
    - Upgraded core dependencies (NATS Server v2.14, NATS Go v1.52) to address multiple upstream vulnerabilities.

> [!CAUTION]
> **DESTRUCTIVE CHANGE**: This migration is a hard architectural break.
> - **Storage**: Existing DuckDB databases (V3 and below) and cached `.flux` state files are incompatible with this version.
> - **API**: REST handlers and NATS topics have transitioned from decimal integer IDs to standard UUID string representations.

## [v0.4.5] - 2026-04-29 {#v045}
### Added
- **Zero-Config Getting Started**: Global Gears (a gear with no `deploy` target runs on every connected Rack), enabling scenarios that work without knowing Rack names in advance.

## [v0.4.4] - 2026-04-23 {#v044}
### Added
- **Enrollment Architecture**: Implemented configuration-driven rack adoption with secure nonce-based passports.
- **CBOR Migration**: Transitioned internal wire-format to deterministic CBOR for binary stability.
- **Data-Plane Integrity**: Enforced technical UTF-8 validation and hex-encoded binary metadata handling.
- **IO Stabilization**: Implemented robust connection polling and rate-limited background WAL replay.

## [v0.4.3] - 2026-02-19 {#v043}
### Added
- **Documentation Website**: Docusaurus-based documentation site with diagram support and full-text search.

## [v0.4.2] - 2026-02-15 {#v042}
### Added
- **Spec & Scenario Manager**: CAS-backed spec/scenario management with CLI (`fluxrig spec`, `fluxrig scenario`) and API integration.
- **E2E Test Suite**: Comprehensive test runner for spec lifecycle, API scenarios, and concurrent access.

## [v0.4.1] - 2026-02-09 {#v041}
### Added
- **Coat Check Pattern**: Implemented architectural pattern to handle "Detached State" during connection handovers.
- **Bus KV**: Implemented the `Bus.KV()` key-value interface with a NATS backend, backing the Coat Check ticket store.

### Changed
- **IO Refactor**: Decoupled TCP connection management from protocol logic.
- **Gear Rename**: `simple_tcp` → `io_tcp` (renamed as part of the IO refactor above).

## [v0.4.0] - 2026-02-01 {#v040}
### Added
- **ISO8583 Native Gear (Alpha)**: First release of the high-performance payment switch gear.
- **Telemetry Governor**: Introduced QoS constraints for telemetry ingress to protect business traffic.

## [v0.3.0] - 2026-01-08 {#v030}
### Added
- **Bento Integration**: Native support for the `warpstreamlabs/bento` ecosystem, enabling the Bento connector ecosystem. The standard binary ships the Pure Logic and Local I/O sets; institutional connectors (Kafka, SQL, AWS) require a custom build.
- **Load Testing Suite**: Integrated `e2e_load` capabilities for stress testing.

## Phase 2: core runtime

| Version | Date | Status | Summary |
| :--- | :--- | :--- | :--- |
| [v0.2.0](#v020) | 2026-01-05 | Delivered | Observability Stack & TLS Foundations |

## [v0.2.0] - 2026-01-05 {#v020}
### Added
- **Observability Stack**: Full OTel integration (Metrics, Traces) with DuckDB backend.
- **Configuration V2**: Unified TOML-based configuration schema.
- **TLS Support**: Enabled mutual TLS for internal bus and HTTPS for management API.

## Phase 1: architecture & foundation

| Version | Date | Status | Summary |
| :--- | :--- | :--- | :--- |
| [v0.1.0](#v010) | 2025-12-27 | Delivered | Initial engine architecture and Snake Protocol |

## [v0.1.0] - 2025-12-27 {#v010}
### Added
- **Foundation**: Initial release of the 4-Repo Architecture.
- **Snake Protocol**: Secure tunneling implementation for Rack-to-Mixer connectivity.
- **FluxMsg**: Canonical JSON schema for inter-gear communication.

[v0.7.0]: https://github.com/jaab-tech/fluxrig/releases/tag/v0.7.0
[v0.6.1]: https://github.com/jaab-tech/fluxrig/releases/tag/v0.6.1
[v0.6.0]: https://github.com/jaab-tech/fluxrig/releases/tag/v0.6.0
[v0.5.0]: https://github.com/jaab-tech/fluxrig/releases/tag/v0.5.0
[v0.4.5]: https://github.com/jaab-tech/fluxrig/releases/tag/v0.4.5
[v0.4.4]: https://github.com/jaab-tech/fluxrig/releases/tag/v0.4.4
[v0.4.3]: https://github.com/jaab-tech/fluxrig/releases/tag/v0.4.3
[v0.4.2]: https://github.com/jaab-tech/fluxrig/releases/tag/v0.4.2
[v0.4.1]: https://github.com/jaab-tech/fluxrig/releases/tag/v0.4.1
[v0.4.0]: https://github.com/jaab-tech/fluxrig/releases/tag/v0.4.0
[v0.3.0]: https://github.com/jaab-tech/fluxrig/releases/tag/v0.3.0
[v0.2.0]: https://github.com/jaab-tech/fluxrig/releases/tag/v0.2.0
[v0.1.0]: https://github.com/jaab-tech/fluxrig/releases/tag/v0.1.0
