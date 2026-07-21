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

## Phase 3: open & flexible logic

| Version | Date | Status | Summary |
| :--- | :--- | :--- | :--- |
| [Unreleased](#unreleased) | | Active | Feature expansion ({{VERS}}-dev) |
| [v0.6.1](#v061) | 2026-07-20 | Delivered | Wasm runtime, supply chain security, polyglot gears |
| [v0.6.0](#v060) | 2026-06-06 | Delivered | Release metadata only, no code changes |
| [v0.5.0](#v050) | 2026-05-07 | Delivered | Sovereign identity (UUID v7) & telemetry hardening |
| [v0.4.5](#v045) | 2026-04-29 | Delivered | Documentation Hardening & Zero-Config |
| [v0.4.4](#v044) | 2026-04-23 | Delivered | Logic Extensibility & Secure Enrollment |
| [v0.4.3](#v043) | 2026-02-19 | Delivered | Operational Resilience & NATS V2 |
| [v0.4.2](#v042) | 2026-02-15 | Delivered | Spec Management & E2E Automation |
| [v0.4.1](#v041) | 2026-02-09 | Delivered | Stateless Context & I/O Decoupling |
| [v0.4.0](#v040) | 2026-02-01 | Delivered | ISO8583 Native Gear & Telemetry QoS |
| [v0.3.0](#v030) | 2026-01-21 | Delivered | Bento Integration & Load Testing |


<a name="unreleased"></a>
## [Unreleased] ({{VERS}}-dev)
#### Added
- 

<a name="v061"></a>
## [v0.6.1] - 2026-07-20
### Added
- **Wazero Integration**: Implemented a secure, native Wasm execution environment using `wazero`.
- **Wasm Supply Chain Security**: Embedded Ed25519 signatures within `.wasm` modules with Mixer-level trust roots and countersignature enforcement prior to Rack execution.
- **Dynamic Catalog Distribution**: Added NATS Snake hot-loading for edge distribution of Wasm logic.
- **PKI & Catalog CLI**: Introduced `fluxrig keys gen-cluster`, `fluxrig wasm sign`, and `fluxrig wasm import` commands.
- **Path Sanitization**: Added `pkg/utils/path` to centralize traversal-safe path handling.

### Changed
- **Security & Linting Hardening**: Resolved remaining Gosec and Staticcheck warnings, achieving a zero-warning build state.
- **Dependencies**: Upgraded the Go dependency group.

<a name="v060"></a>
## [v0.6.0] - 2026-06-06
### Changed
- Release metadata only. This tag contains no source changes relative to `v0.5.0`; the Wasm work intended for it was not merged and shipped in `v0.6.1` instead.

<a name="v050"></a>
## [v0.5.0] - 2026-05-07
#### Changed
- **Sovereign Identity Plane (v0.5.0 Foundation)**: Migrated the entire platform identity system to **128-bit UUID v7 (RFC 9562)**. This enhances entropy, ensures global uniqueness without centralized coordination, and provides time-ordered sequence integrity for high-performance storage indexes.
- **Deduplication Logic**: Updated NATS JetStream deduplication to utilize 128-bit identifiers, ensuring consistent exactly-once delivery across complex telemetry pipelines.
- **Telemetry Hardening**: Standardized dotted naming schema (e.g., `fluxrig.gear.messages_in`) across OTel, Prometheus, and DuckDB.
- **Directional Monitoring**: Split unified I/O counters into distinct Inbound and Outbound channels for precise protocol translation metrics.
- **Resource Guardrails**: Implemented mandatory `MaxHops` (64) and `MaxPayloadSize` (2MB) validation in `fluxmsg` to prevent bus exhaustion and "poison pill" scenarios.
- **Concurrency Resilience**: Integrated global `PanicMiddleware` to ensure Rack stability during individual Gear failures and hardened mutex locking for atomic hot-reloads.
- **Mixer Reliability**: Replaced fragile telemetry discovery with a robust recursive traversal engine, ensuring 100% visibility of historical Parquet data via the API.
- **Certification & Core Test Hardening**: Achieved 100% pass rate in critical certification shards (Mixer, PKI, ISO8583 IO).
- **Certified Coverage**: Consolidated core code coverage reached 60.1%.
- **Enrollment Architecture**: Implemented configuration-driven rack adoption with secure nonce-based passports.
- **CBOR Migration**: Transitioned internal wire-format to deterministic CBOR for 100% binary stability.
- **Data-Plane Integrity**: Enforced technical UTF-8 validation and hex-encoded binary metadata handling.
- **IO Stabilization**: Implemented robust connection polling and rate-limited background WAL replay.
- **Security Hardening (CodeQL Certification)**:
    - Fixed high-severity path traversal in scenario management by implementing robust name sanitization.
    - Hardened TLS configuration in the `snake` server with CA-based client verification support.
    - Resolved integer overflow/truncation risks in telemetry ingestion and ISO8583 codecs.
    - Upgraded core dependencies (NATS Server v2.14, NATS Go v1.52) to address multiple upstream vulnerabilities.

> [!CAUTION]
> **DESTRUCTIVE CHANGE**: This migration is a hard architectural break.
> - **Storage**: Existing DuckDB databases (V3 and below) and cached `.flux` state files are incompatible with this version.
> - **API**: REST handlers and NATS topics have transitioned from decimal integer IDs to standard UUID string representations.

<a name="v043"></a>
## [v0.4.3] - 2026-02-19
### Changed
- **License Headers**: Standardized all source files to SPDX format.

<a name="v042"></a>
## [v0.4.2] - 2026-02-12
### Added
- **Spec & Scenario Manager**: CAS-backed spec/scenario management with CLI (`fluxrig spec`, `fluxrig scenario`) and API integration.
- **E2E Test Suite**: Comprehensive test runner for spec lifecycle, API scenarios, and concurrent access.

### Changed
- **E2E Tests**: Renamed from flat naming to numbered convention (`01_simple/`, `09_io_tcp/`, etc.).
- **Gear Rename**: `io_tcp` → `simple_tcp`.

### Removed
- **Coat Check Gear**: Removed in favor of Spec Manager pattern.
- **Bus KV**: Removed (~3,084 lines deleted across 63 files).

<a name="v041"></a>
## [v0.4.1] - 2026-02-09
### Added
- **Coat Check Pattern**: Implemented architectural pattern to handle "Detached State" during connection handovers.
- **IO Refactor**: Decoupled TCP connection management from protocol logic.

<a name="v040"></a>
## [v0.4.0] - 2026-02-01
### Added
- **ISO8583 Native Gear (Alpha)**: First release of the high-performance payment switch gear.
- **Telemetry Governor**: Introduced QoS constraints for telemetry ingress to protect business traffic.

<a name="v030"></a>
## [v0.3.0] - 2026-01-05
### Added
- **Bento Integration**: Native support for the `warpstreamlabs/bento` ecosystem, enabling 100+ I/O connectors (AWS, SQL, Kafka, File).
- **Load Testing Suite**: Integrated `e2e_load` capabilities for stress testing.

## Phase 2: core runtime

| Version | Date | Status | Summary |
| :--- | :--- | :--- | :--- |
| [v0.2.0](#v020) | 2025-12-28 | Delivered | Observability Stack & TLS Foundations |

<a name="v020"></a>
## [v0.2.0] - 2026-01-08
### Added
- **Observability Stack**: Full OTel integration (Metrics, Traces) with DuckDB backend.
- **Configuration V2**: Unified TOML-based configuration schema.
- **TLS Support**: Enabled mutual TLS for internal bus and HTTPS for management API.

## Phase 1: architecture & foundation

| Version | Date | Status | Summary |
| :--- | :--- | :--- | :--- |
| [v0.1.0](#v010) | 2025-12-12 | Delivered | Initial engine architecture and Snake Protocol |

<a name="v010"></a>
## [v0.1.0] - 2025-12-28
### Added
- **Foundation**: Initial release of the 4-Repo Architecture.
- **Snake Protocol**: Secure tunneling implementation for Rack-to-Mixer connectivity.
- **FluxMsg**: Canonical JSON schema for inter-gear communication.

[Unreleased]: https://github.com/jaab-tech/fluxrig/compare/v0.6.1...HEAD
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
