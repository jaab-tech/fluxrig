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
| [v0.10.0](#v0100) | 2026-09-07 | Delivered | The semantic layer stops being documentation: a spec's rules are enforced on traffic |
| [v0.9.0](#v090) | 2026-09-03 | Delivered | Enrichment from outside the message, and correlation keys that survive a bus hop |
| [v0.8.0](#v080) | 2026-08-24 | Delivered | EMV chip data: BER-TLV parsing with unknown-tag preservation |
| [v0.7.1](#v071) | 2026-08-10 | Delivered | ISO 8583 TLV length hardening |
| [v0.7.0](#v070) | 2026-08-01 | Delivered | Payment switch: Conductor gear, gear manifests, ISO 8583 TLS |


## [v0.11.0] - 2026-09-24 {#v0110}

**Before upgrading.** Four changes alter what a running deployment does, and one of
them cannot be undone by installing the old binary.

- **The Mixer's message store is encrypted on the first start of this version, and
  an older Mixer cannot read it.** Copy the `snake` directory of the Mixer's store
  before the first start if you may need to go back. The key is derived from
  `cluster.key`: replacing that file makes the store unreadable, unless you first
  print the derived key with `fluxrig keys store-key` and keep it as
  `snake.store_old_key_file`. To use a key of your own, set `snake.store_key_file`.
- **The bus now keeps a message for 24 hours and a stream up to 1 GiB**
  (`snake.stream_max_age`, `snake.stream_max_bytes`). It had no limit. Raise them if a
  consumer needs older messages.
- **A wire inside one Rack is delivered through memory, at most once.** A message
  still queued when the Rack process ends is lost, where the bus stored it. Ask for
  `lane: "guaranteed"` on a wire that needs the message stored. An emission that no
  wire consumes is no longer published to the bus, so anything outside the scenario
  that listened for it on the bus stops receiving it.
- **The `io_tcp` server gear and the Bento input gear no longer write the payload of a
  message to the log at INFO.** A procedure that read card data or payloads from those
  logs stops finding them. TRACE writes it with card numbers masked.
- **A message the `io_tcp` server gear cannot route unambiguously is now refused
  instead of guessed at or silently queued.** A message naming a `conn.id` whose
  connection has disconnected used to be queued for whoever connected next, which
  could hand it to the wrong client; it now returns an error. A message naming no
  `conn.id` while several connections are active used to go to "the first" one,
  arbitrarily; it now returns an error too. A message naming none with exactly one
  connection active, or none at all, is unaffected.
- **`Activate` now fails when a scenario's deploy pins reach no Rack at all**, not
  only when one names a Rack that was never enrolled. A `deploy:` pin not listed
  under the scenario's own `racks:` section used to compute zero push targets and
  report success anyway; the scenario stays the Mixer's active one either way, and
  a Rack that enrolls afterwards still receives it.
- **The simulator control endpoints (`POST /api/v1/control/sim/{start,stop,rate,reset}`)
  now answer 503, naming the gear, when nothing acknowledges the command**, instead
  of 200 as soon as NATS accepted the publish. `api.control_confirm_timeout`
  (default `2s`) bounds how long it waits.

### Added
- `gears.RegisterExtension`: a module outside this repository adds gears to the
  gear factory. Its binary registers them before the factory is built, and the
  engine never imports that module. The Mixer command line moved to
  `cmd/fluxrig-mixer/mixercmd`, so another module can build a Mixer whose gear
  catalog lists those gears too.
- The Robot runner takes a suite directory given as an absolute path, and
  `FLUXRIG_BIN_DIR` points the suites at another set of binaries. The e2e helpers
  (`test/e2e/utils/e2e_utils.sh`) honor `FLUXRIG_BIN_DIR` too. A suite or an e2e test
  kept in another repository runs on this tooling and exercises the binaries built
  there.
- A Rack keeps its own copy of the last scenario it applied, and resumes it at
  start without waiting for the Mixer to send it again (`base.scenario_file`,
  `scenario.flux` beside its passport, mode `0600`; empty keeps no copy). When the
  Mixer then sends the same scenario the gears keep running instead of
  restarting. A copy projected for another Rack is ignored. A Rack that starts
  with the bus unreachable starts it on its own when none of its wires uses the
  bus, and keeps a scenario that does waiting for the bus, whole, saying so in its
  log.

- **Wires have a lane.** A wire says `lane: "hot"` or `lane: "guaranteed"`. A hot
  wire carries its messages through the Rack's memory: nothing is stored and
  nothing reaches the Mixer. A guaranteed wire goes over the bus, which stores each
  message before the gear is told it was accepted. A wire that names none is hot
  when both gears run on one Rack and guaranteed between Racks. A hot wire between
  Racks is rejected at import. A Rack now keeps running the flows that stay inside
  it while the Mixer is away. `rack.lane_queue_size` (1024) and
  `rack.lane_send_timeout` (5s) bound each hot wire, and a graceful stop delivers
  what is queued.
- **The Mixer encrypts its message store at rest.** On by default, with a key
  derived from the cluster key, or with a key file the operator provides
  (`snake.store_encryption`, `store_cipher`, `store_key_file`, `store_old_key_file`).
  It covers every message, stream and key-value bucket of the bus. An existing store
  is converted on the first start with no message lost, a store that was encrypted
  does not open without its key, and `fluxrig keys store-key` prints the derived key
  for a rotation. A copy of the store directory or a backup no longer shows a
  message. The Mixer, and a client of the bus, still see clear data.
- `snake.stream_max_age` (24h) and `snake.stream_max_bytes` (1 GiB) bound the
  streams of the bus, which had no limit.
- A log payload is masked at TRACE: card numbers written as digits keep their first
  six and last four digits.

- **A Rack starts without the Mixer.** With a Passport and a saved scenario whose
  wires all stay inside the Rack, it runs the scenario on its own and serves
  traffic. When the Mixer returns the Rack joins it without stopping the gears: a
  client that was connected stays connected, and the gears are not started again.
  Scenarios that use the bus wait for it. Until it is bound, the gears'
  control plane carries commands in memory between the gears of the Rack (a link
  that goes down, a connection to close), and once the Rack has joined it also
  carries the Mixer's commands to gears that were already running. A Manager can
  change the bus under its running gears (`Manager.SetBus`).
- `io_tcp` gains `max_buffered_messages` (default `1000`): the cap on outbound
  messages queued when nothing is connected yet, or a message names no `conn.id`.
  It was an unconfigurable, hardcoded 1000 before. The oldest is dropped past the
  cap, now counted on `flux.gear.messages_dropped`.
- `api.control_confirm_timeout` (default `2s`): how long a simulator control
  command waits for a gear to acknowledge receiving it before the request answers
  that nobody is listening.

### Changed
- **A wire inside one Rack no longer goes through the bus by default.** It goes
  through memory, delivered at most once and in order: a message still queued when
  the Rack process ends is lost, where the bus stored it. Nothing of a hot wire is
  stored, so a card number on one is no longer written to the Mixer's disk. A
  scenario that needs the message stored asks for `lane: "guaranteed"` on that wire.
  An emission that no wire consumes is dropped and no longer published to the bus.
- The `io_tcp` server gear and the Bento input gear no longer write the payload of
  each message to the log at INFO, and no gear writes it at DEBUG. Those logs were
  shipped to the Mixer and kept there.

### Fixed
- The spec loader reads `format.layout` of a date or time field (`MMDD`, `hhmmss`,
  `MMDDhhmmss`, `YYMM`). The reference spec declared them and the loader dropped them,
  so nothing that generates traffic could write DE 14 as an expiry.
- Documentation said a Rack keeps processing while the Mixer is unreachable, and
  that a silent Mixer does not stop traffic. Gears exchange their messages over
  the bus embedded in the Mixer, wires inside one Rack included, so while the
  Mixer is away a running Rack stays up and reconnects by itself but what its
  gears emit fails. The operations, deployment, architecture, security, data,
  observability and conductor pages, the payment switch tutorial and the README
  now describe what a Rack does in each case, and list isolated operation as
  roadmap. The pages also said a Rack embeds NATS JetStream (it connects to the
  Mixer's), that a scenario that fails to apply rolls back to the previous one
  (it stops the gears it started), and that every signal a Rack handles goes
  through a write-ahead log (only its logs do).
- Decoder gear (`codec_iso8583`) nil-pointer panics on composite fields:
  nil guards on `GetField()`, `GetSubfields()`, `Bitmap()` and subfields,
  plus per-subfield panic recovery.
- A Rack holding a passport no longer waits out the whole bus retry schedule
  (about 37 seconds) before starting offline. The first connection retry, added
  for a bus that is not accepting connections yet, applied to every Rack alike.
  A Rack with a passport now gives the bus `snake.offline_start_timeout` (3s by
  default) and then runs offline; a Rack without one keeps the full
  `snake.initial_retry_*` schedule. `0s` restores the unbounded retry.
- Activating a scenario that deploys to a Rack that is not enrolled and active
  answers HTTP 409 with the Rack's name, where it answered 500. The scenario
  stays imported, and activates once the Rack enrolls. Any other activation
  failure remains a 500.
- A Rack that started offline now finds the Mixer when it comes back. It probes
  the bus every `snake.offline_retry_interval` (5s by default) and, once the bus
  answers, restarts its session and goes online: hello, passport, telemetry and
  scenario as on any start. Before, it stayed offline until it was restarted by
  hand. `0s` turns the probe off.
- `snake.initial_retry_wait`, `snake.offline_start_timeout`, `snake.offline_retry_interval`
  and `rack.lane_send_timeout` discarded a parse error and silently became zero, where
  their six sibling settings already failed fast on a typo. `"0s"`, their documented
  way to disable the bound, still parses without error.
- **`codec_iso8583`: a panic anywhere in `decode()` after `Unpack` succeeded read
  as a successful, empty decode.** Only a panic inside `Unpack` itself was turned
  into an error; the field-extraction loop after it (bitmap iteration, composite
  subfields) was guarded by a recover that only logged, so the message reached
  `Process()` looking cleanly decoded, with an empty MTI and no fields.
- **`coatcheck`: restoring a previously-vaulted field into a message whose nested
  data had crossed the bus (CBOR-shaped) silently did nothing.** The merge built a
  normalized copy of the nested map, merged the restored value into that copy, and
  never wrote the copy back onto the live message.
- **`iso8583-tool decode` had no panic recovery around `Unpack`**, unlike the gear
  it mirrors: a malformed payload crashed the process instead of reporting a clean
  error the way every other failure in the tool already does.
- A data race on `RegisterEntity`'s read of `autoAdopt`, racing `SetAutoAdopt`.
- Mixer startup with a pinned scenario logged a false "Failed to activate startup
  scenario" warning on every normal cold start, since no Rack has enrolled yet at
  that point by definition. It now logs at Info that the scenario is active and
  will reach Racks as they enroll; any other activation failure still warns.
- Deploy targets naming a Rack **group** now get a distinct error
  ("cannot be matched against active Racks yet") instead of the generic "unknown
  or inactive target": nothing in the registry lets a live Rack carry labels yet,
  so a group can never be resolved today, and the old message wrongly implied
  enrolling a Rack would fix it.
- The `io_tcp` server gear's outbound message buffer now delivers its whole
  backlog to a newly-connected client, in the order it was queued, instead of one
  message per connection with the rest stranded; a write failure partway through
  requeues what was not sent instead of losing it.
- `Unsubscribe` on a hot-lane subscription could block forever if the gear's
  handler never returned, deadlocking every later scenario apply or shutdown on
  that Rack. It now gives up after a bounded wait and logs, rather than hanging.
- The hot lane stopped delivering to every subscriber of a subject as soon as one
  of them failed (a full or stuck queue). A stuck subscriber's failure is still
  reported, but only after every other subscriber has had its turn.
- A Rack's initial NATS connection retry ignored a shutdown signal, so a SIGTERM
  during that retry (NATS down at startup) was not observed until the whole retry
  schedule elapsed (about 37s by default, longer with a configured
  `snake.initial_retry_timeout`).
- `io_tcp` client, `read_responses: false`: a peer that closed or reset the
  connection was never noticed, so the reconnect logic never ran again until the
  gear was stopped by hand.
- Updating an existing JetStream stream never cleared `snake.stream_max_bytes`
  back to unlimited when set to `0`, unlike `snake.stream_max_age`, which already
  did.


## [v0.10.0] - 2026-09-07 {#v0100}

The semantic layer stops being documentation: a spec could state its rules and
nothing applied them.

**Before upgrading.** Two changes refuse specs that used to load, and both are
about a spec declaring what it is. Every spec must carry `spec.id` and
`spec.version`, and every spec must carry a `wire` block saying where its wire
layer comes from. A spec missing either is refused at boot, with a message naming
it, never per transaction. Every spec shipped in this repository is already in
that shape; both entries under **Changed** say what one that is not needs. New
behaviour is off by default: `validation` starts at `off`, so a message accepted
yesterday is not rejected today because the code was upgraded.

### Added

- **The spec's rules can be enforced on traffic.** `codec_iso8583` gains `validation`: `off` (default), `warn` or `enforce`. Per-MTI field usage, the conditions that decide whether a rule applies, closed value sets and cross-field `checks` are answered against every message the codec decodes or encodes. A rejection follows `on_error`, exactly as a decode failure does; violations travel on the message as `codec.violations` and are counted on `flux.iso8583.violations` by severity, kind and MTI.
  - The default is `off` deliberately: a message accepted yesterday must not be rejected today because the code was upgraded. `warn` is how you find out whether your spec matches your traffic before enforcing it.
- **A spec can state when a rule applies, and what a message must satisfy.** Fields carry a per-MTI matrix (`usage`, `when`, `values_ref`) and a spec carries `checks`, written in a total, side-effect-free expression language over the message being validated.
  - A `check` carries a severity: `reject` fails the message, `warn` records it and lets it through, which is what makes a rule deployable to a live fleet before it is enforced on one.
  - An element compares the way its `format.kind` says it does. `amount`, `date`, `time`, `datetime` and the new `numeric` kind compare as numbers on every operator, so a rule can write `field(4) == 1000` rather than the element's own zero padding. Every other kind compares as the characters it carries, which is what a response code needs: `"00"` is not `"0"`. `pan` is deliberately excluded, because a leading zero makes it a different card.
- **`fluxrig spec doc` renders the protocol reference from the spec.** The Mixer serves it too, at `GET /api/v1/specs/{name}/{tag}/doc`, and every entry in the spec listing carries the path to its own. Markdown for a repository or a docs site, HTML for a page that is read, printed or mailed. The HTML is self-contained: no scripts, stylesheets or fonts are fetched, so it opens offline. `--scope public` omits every field marked `scope: private` and states how many it withheld; `--scope complete` is the internal view. The per-message tables are derived on every render from the rules stored on the fields, so the two cannot drift.
- **`format.kind: numeric`.** A value that is a number rather than a code (a trace number, a sequence, a count), which had no way to be declared.
- **A stored spec's versions can be listed.** `fluxrig spec history <name>` and `GET /api/v1/specs/{name}` return every version of one spec, newest first by version rather than by arrival. `fluxrig spec list` and the `/specs` listing now carry when each version was filed, its size, the document's title and which version `latest` reaches; `--json` on both.

### Changed

- **BREAKING: a spec declares where its wire layer comes from.** Every spec now carries a `wire` block: `source` names a Moov base (`moov:<name>`) or a wire document beside the spec, `fields` states what the dialect changes, and each is valid alone. Specs in the legacy vocabulary (`meta:` with a top-level `fields:` fusing both layers, `llvar_n`-style type tokens) no longer load. Every spec shipped in this repository is already in the new shape; the [SDL reference](https://fluxrig.org/docs/reference/protocol/iso8583) describes what a spec that is not needs.
- **BREAKING: a spec must declare `spec.id` and `spec.version`.** The schema has required them all along and the loader never read them, so a spec with no version at all loaded clean and its traffic carried a content hash that says which *file* ran and not which *contract*. A spec missing either is now refused at load: at boot, with a message naming the spec, never per transaction. Every spec shipped in this repository already declares both; a spec that does not needs the two fields added before upgrading.
- **`codec_iso8583` manifest: `on_error` is documented as defaulting to `drop`.** It has always defaulted to `drop`; the manifest said `reject`, so an operator reading the published contract configured for one behaviour and got the other.

### Fixed

- **`unknown_tags: drop` rejected instead of dropping.** The policy documented three behaviours and implemented two: anything that was not `preserve` took the same branch, so a spec asking to drop an unknown TLV tag had the whole message rejected, which is the opposite of what it asked for.
- **`VerifyConnectivity` could report convergence on a cancelled context.** The bus delivers to its handlers without consulting the caller's context, so a probe could land after cancellation and Go picked at random between the two ready cases. A caller shutting down was told the telemetry plane is ready.

## [v0.9.0] - 2026-09-03 {#v090}

### Added

- **`coatcheck`: `key_normalize`.** Key field values are canonicalized before they are joined, so the two sides of an exchange agree on a key even when they render the same value differently. `trim` (default) removes surrounding whitespace; `numeric` also drops leading zeros, for fixed-width fields decoded against specs that declare different widths.
- **`coatcheck`: `await_store`.** A store can forward the message without waiting for the write. The default stays blocking, which is right when the reply depends on the entry; set it false when the entry only enriches a record and holding a message for a control-plane write costs more than losing one.
- **`codec_iso8583`: `iso8583.mti_class`.** A decode now also exposes the leading MTI digits, which a request and its reply share. Correlation keys can be scoped by message class, so an authorization and a reversal reusing a trace number are no longer indistinguishable.
- **Scenarios can name what a diagram cannot derive.** A Rack's `role` label reaches its description, and two gear labels, `peer` and `peer_played_by`, say who is on the far side of a socket and whether anything is standing in for it.
- **`iso8583-tool`: `-scheme-move`.** The host simulator can report a request field back in another, so a host receiving a private field can show which value arrived. It reports into a different field deliberately, and pads to the destination's declared width.
- **Enriching an authorization from outside the message.** A [use case](use_cases/mobile_network_signals.md) on the GSMA Open Gateway APIs in payments, and a [tutorial](tutorials/roaming_enrichment.md) building one end to end: an operator call under a hard deadline, four named reasons for having no signal, and the degradation paths, all as configuration.

### Fixed

- **Nested field paths silently stopped resolving after a bus hop.** `GetValue` descended only into `map[string]any`, but CBOR decodes a nested object as `map[any]any`. A correlation key built from an ISO 8583 field therefore worked in a single-gear pipeline and returned "field missing" in any real deployment, with nothing logged.
- **The `bento` gear injected a memory buffer over a config that declared its own input.** A buffer acknowledges the input before the pipeline has produced anything, so a synchronous responder answered with an empty body and no error anywhere. The injection now happens only when the gear supplies the input itself.
- **A `bento` gear in a relay path dropped the wire bytes.** `RawPayload` survived only when the structured view was empty, so any gear that merely read fields destroyed the payload a downstream io gear had to write. It now travels across the bridge.
- **`fluxrig scenario viz` drew both ends of one socket as strangers.** Every I/O gear produced its own external box, so a client and the server it dials appeared as two unrelated outsiders. When both ends are in the scenario they are now one relationship.

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
