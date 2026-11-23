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
| [v0.16.0](#v0160) | 2026-11-23 | Delivered | A Coat Check daemon no longer loses a timeout to the bucket's expiry and takes up what was parked before it started, `delete_on_restore` can make a parked entry single use, the moov modules move, and Go 1.26.11 fixes GO-2026-6859 |
| [v0.15.1](#v0151) | 2026-11-21 | Delivered | A Rack that enrolls again gets the active scenario from the Mixer, which v0.15.0 dropped, and `enrollment.push_timeout` bounds that push |
| [v0.15.0](#v0150) | 2026-11-21 | Delivered | Several Mixers share one control plane on an operator-run NATS cluster, with a write lease, shared telemetry and per-Rack permissions. Faster failover detection, batched event and log shipping, and a hardened API and Wasm surface |
| [v0.14.0](#v0140) | 2026-11-01 | Delivered | A server can greet a client that sent nothing, through durable connection events, a misspelt gear option is refused, a late reply can reach the new connection of its peer, and the Wasm gear refuses an unsigned catalog module and applies its memory limit |
| [v0.13.1](#v0131) | 2026-10-25 | Delivered | Go 1.26.10 and x/net v0.60.0 close 23 reachable vulnerabilities, an approved Rack gets the active scenario, `fluxrig check` reports a bad configuration and answers in seconds, dead letter publishes and file wire sources are bounded, and a server reply carries the swapped TPDU |
| [v0.13.0](#v0130) | 2026-10-20 | Delivered | ISO 20022 support: an ISO 8583 network bridges to the RTP rail, specs and lookup data upload to the Mixer, the registry lives on the bus, and a Rack parks messages while the bus is down |
| [v0.12.0](#v0120) | 2026-09-28 | Delivered | The Mixer's API and a Rack's enrollment and heartbeat now require authentication, a Rack pins the Mixer's signing key, and guaranteed-lane messages ack only once handled |
| [v0.11.0](#v0110) | 2026-09-24 | Delivered | The message store is encrypted at rest, wires choose a lane (hot or guaranteed), and the first licensed enterprise gears ship alongside the core release |
| [v0.10.0](#v0100) | 2026-09-07 | Delivered | The semantic layer stops being documentation: a spec's rules are enforced on traffic |
| [v0.9.0](#v090) | 2026-09-03 | Delivered | Enrichment from outside the message, and correlation keys that survive a bus hop |
| [v0.8.0](#v080) | 2026-08-24 | Delivered | EMV chip data: BER-TLV parsing with unknown-tag preservation |
| [v0.7.1](#v071) | 2026-08-10 | Delivered | ISO 8583 TLV length hardening |
| [v0.7.0](#v070) | 2026-08-01 | Delivered | Payment switch: Conductor gear, gear manifests, ISO 8583 TLS |


## [v0.16.0] - 2026-11-23 {#v0160}

### Added

- **`delete_on_restore` for the Coat Check `restore` mode, off by default.**
  By default a restored entry stays until its TTL and then sends
  `flux.event.timeout`, so **that event does not mean that no reply came**.
  Earlier versions do the same. With the option on, `restore` removes the
  revision that it restored and the key does not time out, unless the reply
  comes at the expiry or the removal fails. A repeated reply, or a second
  restore of the key, then follows `on_missing`. The Rack needs no new NATS
  permissions.

### Changed

- **`moov-io/iso8583` v0.26.4.** A Track 2 (field 35 of the shipped specs)
  that ends right after the service code, with no discretionary data, now
  encodes and decodes if its expiry is a valid year and month. ISO/IEC 7813
  allows it. Before, the codec refused it with `invalid track data`. With an
  expiry that is not valid it is still refused, now with
  `invalid expired time`.

- **`moov-io/rtp20022` v0.12.3 and `moov-io/base` v0.68.2.** The RTP
  messages, their validation and the masking of the values that break a rule
  are unchanged. `golang.org/x/net` v0.61.0, `x/crypto` v0.58.0, `x/sync`
  v0.24.0 and `x/text` v0.43.0 come with them.

### Fixed

- **A Coat Check daemon no longer loses a timeout to the expiry of the
  bucket.** Since v0.13.0 the
  server could remove an entry a moment before the daemon's timer fired. The
  daemon read that as a delete and never sent `flux.event.timeout`, with
  nothing in the log. The expiry of the server is now the timeout as well,
  and a daemon sends one event for the expiry of an entry that it saw.
- **A Coat Check daemon that starts takes up the entries already parked.**
  It saw only new ones, so an activation, which restarts the gears, lost the
  timeouts that were pending. An entry that expired while no daemon ran
  still has no event: the bucket marks it like a deleted entry, so an event
  for it could be a false timeout. **A scenario that acts on the event gets
  events that were lost before.**
- **A Coat Check entry that is parked again keeps a TTL.** The second park
  stored it with none, so it lived for up to `max_ttl` (default `5m`) when no
  daemon removed it, whatever its own TTL. It now takes the TTL of the last
  park. Entries written by an earlier version keep the lifetime they have.
- **A Coat Check TTL under one second, or with a fraction, works.** A TTL
  under a second, which the schema accepts, failed the write to the bucket,
  and with it the message when `await_store` is on. A fraction was dropped.
  The bucket now rounds a TTL up to whole seconds, and the daemon times out
  at the exact TTL.
- **Built with Go 1.26.11.** It fixes GO-2026-6859 in `net/http`: with
  `MaxBytesReader`, an HTTP/1 handler that reads the request body while it
  writes the response header can crash the server. `govulncheck` flagged the
  code because it calls `net/http`, and finds nothing reachable now. The
  Mixer API handlers that use `MaxBytesReader` read the body before they
  answer.

## [v0.15.1] - 2026-11-21 {#v0151}

### Added

- **`enrollment.push_timeout`** (default `10s`) bounds the push of the active
  scenario that follows an enrollment.

### Fixed

- **A Rack that enrolls again gets the active scenario from the Mixer.** In
  v0.15.0 the push that follows a Hello was dropped every time, with nothing
  in the log: a Rack that restarted ran the copy of the scenario it had saved,
  and a Rack that was down when a scenario was activated did not get it when
  it came back. An approval still pushed the scenario. The push now runs
  until the Mixer stops, bounded by `enrollment.push_timeout`.

## [v0.15.0] - 2026-11-21 {#v0150}

**Before upgrading.** Three changes can stop something that runs today.
Read [Upgrading](reference/upgrading.md#v014-to-v015), and upgrade the Mixer
and every Rack together.

- **The Mixer and the Racks must run the same version.** The Mixer sends a
  Rack its status, its commands and its passport on
  `flux.agent.notify.<rack>.<machine-id>`, not on
  `flux.agent.notify.<machine-id>`. A Rack of v0.14 listens on the old subject.
- **A trusted Wasm key file whose vendor name is not 1 to 64 letters, digits,
  `-` and `_` is ignored**, with a Warn. A module signed with that key is then
  refused at import, unless the import allows unsigned modules. Rename the
  file in `wasm.trusted_keys_dir`.
- **Deleting a key file from `wasm.trusted_keys_dir` no longer stops trusting
  the vendor.** The trusted keys now live on the bus, and the keys directory
  only adds the vendors that the bus does not hold yet. Use
  `fluxrig wasm keys revoke`.

### Added

- **Mixer HA on an external NATS.** With `snake.external = true` (default
  `false`) the Mixer starts no embedded NATS and uses the NATS cluster at
  `snake.url` that the operator runs. Several Mixers on one cluster give HA.
  Every Mixer serves the API, and the Racks talk to NATS directly.
  `snake.replicas` (default 1) sets the copies of each stream and bucket. More
  than 1 needs `snake.external`, and the Mixer warns below 3. At startup the
  Mixer needs NATS 2.15.0 or later with JetStream, and a cluster when replicas
  exceed 1. A NATS, stream or bucket that is not ready yet is tried again for
  `snake.form_timeout` (default `2m`). The default stays one Mixer on the
  embedded NATS, as in v0.14. The operator keeps the NATS store and its
  encryption at rest.
- **The write lease.** With `registry.lease_enabled` (default `false`) one
  Mixer holds a lease and does every control write. The others forward each
  control write to the holder and wait up to `mixer.forward_timeout` (default
  `5s`). Reads run on every Mixer. With no holder, a control write answers 503
  and can be tried again. The lease expires after `registry.lease_ttl`
  (default `30s`) without a renew, and the holder renews every
  `registry.lease_renew` (default `10s`), at most half of the TTL. Every Mixer
  must share the cluster key. A Mixer that does not hold the lease still
  follows the active scenario, polling it every `mixer.follow_interval`
  (default `5s`), and shares one queue group for Hello and heartbeats so each
  message is handled once.
- **Lease visibility.** With the lease on, `/health` names the holder
  (`holder`, `holder_since`, `renewed`, `held`) and the Mixer exports
  `flux.mixer.lease.held` and `flux.mixer.lease.transitions`.
- **One spec store and one Wasm catalog for every Mixer.** Specs live in
  `registry.spec_bucket`, keyrings in `wasm_keys`: a spec or key trusted
  through one Mixer counts on every Mixer. The keyring is in the API
  (`GET /api/v1/wasm/keys`, trust and revoke by vendor) and in the CLI
  (`fluxrig wasm keys list`, `trust`, `revoke`).
- **Telemetry storage for several Mixers.** `observability.storage.type`
  (default `local`) can be `s3`, `nats` or `otlp`, all needing the lease. Only
  the holder reads the telemetry stream and confirms each batch once the store
  has it, so a new holder continues from the last confirmed batch. The holder
  commits a batch every `observability.storage.batch_interval` (default `15s`).
  With `otlp` the Mixers keep no telemetry (queries answer 501) and one batch
  can arrive twice.
- **A Rack survives its server.** After `snake.failover_after` (default 2)
  publishes that find no stream, the Rack reconnects to another server of
  `snake.url`. A lost subscription consumer is replaced and resumes at the
  first unsettled message, with at most one duplicate. Each Rack has its own
  reply inbox, `_INBOX_<rack>`, and a Rack that starts before any Mixer waits
  for streams instead of exiting.
- **`fluxrig scenario permissions` writes one NATS user per Rack**, limited to
  the subjects that Rack uses. Gear manifests declare the rest in
  `sdk.Manifest.BusNeeds` (events read, buckets used), which the command
  reads. Generate the users again when a scenario changes.
- **Client certificates and credentials for NATS.** A Mixer or a Rack presents
  `snake.client_cert_file` with `snake.client_key_file`, or a
  `snake.credentials_file`. Set both certificate files or neither.

### Changed

- **A client of NATS notices a silent server in about 15 to 20 seconds.**
  `snake.ping_interval` (default `5s`) and `snake.max_pings_out` (default 3)
  set the pings of every connection. Before, the client defaults (a ping every
  2 minutes, 2 unanswered) kept a client on a crashed or cut server for
  minutes. Both values must be positive.
- **A message of the guaranteed lane that a consumer sent through a NATS
  server that died arrives again after 10 seconds, not 30.**
  `snake.ack_wait` (default `10s`) is how long a guaranteed-lane or durable
  consumer waits for an acknowledgement before it delivers the message again.
  Before, it was the JetStream default, 30 seconds. It must be longer than
  `snake.operation_timeout`: a shorter value would deliver every slow message
  twice, and the Mixer and the Rack refuse it.
- **A connection event that a reader's consumer sent through a NATS server
  that died arrives again after `snake.ack_wait`, not after `max_event_age`.**
  Before, the event came back as old as `max_event_age` and the reader dropped
  it, so a connecting client got no logon. `max_event_age` of each entry of
  `ports.events` must now be longer than `snake.ack_wait` (`10s` by default),
  or the gear does not start.
- **A Rack sends the connection events and ships its log in batches.** What
  waits in a gear's event queue leaves in one batch, in order, and the log
  shipper sends consecutive lines in one batch too. Before, each item waited
  for the stream before the next one: across a link with a round trip of about
  220 ms to the stream leader, a busy Rack fell behind. `sdk.EventBatchPublisher`
  is the capability a native gear can use for the same.
- **A Wasm import that the bus does not take answers 503**, and nothing is
  imported. Before, the import succeeded with a Warn and the module stayed on
  the Mixer's disk, where no Rack could fetch it. The catalog
  `GET /api/v1/wasm/catalog` answers 503 when the bus cannot answer, and the
  status of a module names its vendor (`Trusted: <vendor>`, else `Untrusted`).
- **An API request that failed because the cluster could not answer yet
  answers 503**, not 500 or 400: no Mixer holds the write lease, or JetStream
  elects a leader or has no stream that answers. The request can be tried
  again.
- **A Rack keeps its gears when the Mixer sends again the scenario they run.**
  Before, each copy restarted every gear. An activation still restarts the
  gears, once per activation. A Rack with no identity keeps sending Hello
  after `rack.enrollment_timeout` (default `15s`) until a passport arrives.
  The embedded NATS server now logs to the Mixer log, and a Rack no longer
  purges its messages from the stream when its scenario changes.

### Fixed

- **A graceful stop of `io_iso8583` logs no `accept error`, and `Stop` is safe
  to call twice.** The accept loop logged an error for each retry until
  `Stop`, and the second `Stop` panicked.
- **Telemetry keeps UTC on a Mixer whose host is not in UTC**, uses
  `telemetry.stream_name` for its connection (it used `flux-telemetry`),
  and keeps `logging.level` on stdout after enrollment. A refused NATS
  permission is now logged at ERROR with the connection name.
- **A Rack starts a scenario when a sync probe takes longer than 500 ms.**
  Before, the Rack never saw that its probe had arrived, failed the scenario
  at `rack.convergence_timeout`, and after five attempts left it in the dead
  letter queue.
- **Subscriptions and replaced consumers stop with the bus.** A durable
  subscription is created with a bounded wait and the replica count of its
  stream, and the consumer that shares Hello and heartbeat across the Mixers
  takes `snake.replicas` copies. One dead NATS server replaces a live consumer
  once, not twice, and a guaranteed-lane batch awaits each acknowledgement
  with its own budget instead of one timer for the whole batch.
- **Failover counts what the server did.** A publish a caller cancels is no
  longer counted as a missing stream, while every timeout counts, including
  one where the caller's own deadline expired. The failover probe gives up
  when its own timeout ends even if the request is stuck, and a write that
  cannot be flushed is bounded by `snake.operation_timeout` instead of the
  client's one-minute default.
- **A poison message is routed to the dead letter queue once**, a write to
  that stream that fails leaves the message for redelivery (counted as
  `DLQFailures`), and opening an events reader no longer blocks its own
  `Close`.
- **Shutdown paths stop cleanly.** A push to a Rack after enrollment stops
  when the Mixer shuts down, a scenario activation no longer holds the
  controller lock while it publishes to every Rack, a stalled lease read,
  renew or holder lookup is bounded, a telemetry batch taken over is committed
  once, a live emission cannot overtake the parked suffix, and the outbox
  drain is linear in the queue size. `fluxrig-mixer --repair` works with a
  store with TLS configured, `/api/v1/control/sim` refuses a gear name that
  is not one subject token, `/health` reports `"unavailable"` when it cannot
  read the lease, `/api/v1/config` removes credentials from `snake.url`, and a
  testbed failover run is wired into the nightly.
### Security

- **ISO 8583 closed-set violations no longer log or return the value.** The
  rejection reason said `carries %q value`. It now says `carries (value
  withheld)`. The value reached the Warn log, the rejection error and the
  `error.message` metadata of the error port. ISO 20022 already masked it.
- **The Mixer warns at startup about telemetry and key exposure.** It warns
  when the telemetry store is shared, when S3 or OTLP uses plaintext, when the
  external NATS URL is not TLS, and when a credential or key file is
  world-readable.
- **S3 telemetry objects request SSE-S3.** The Mixer sets
  `x-amz-server-side-encryption: AES256` on each upload. Before, the bucket
  policy was the only encryption.

## [v0.14.0] - 2026-11-01 {#v0140}

**Before upgrading.** Four changes can stop something that runs today. Read
[Upgrading](reference/upgrading.md#v013-to-v014), and upgrade the Mixer first.

- **A gear option that the gear does not declare is refused**, also in a scenario
  that is already active: the Rack refuses it when it applies the scenario.
- **The Wasm gear applies `memory_limit_pages`**, and a module that declares more
  memory than the limit does not load.
- **A Wasm module from `snake://` with no valid cluster signature does not run.**
- **`snake.stream_subjects` must not include `flux.evt.>`.**

### Changed

- **BREAKING: the reference ISO 8583 spec packs a standard bitmap.** It packed one 128-bit bitmap on every message and never set bit 1, so a peer that follows ISO 8583 read the second half as data. The spec now declares an 8-byte primary bitmap, and a secondary bitmap only when a field above 64 is present, announced by bit 1. The spec's version is now `3.0.0`: a Mixer that already holds `v2.2.0` keeps serving the old layout under that name. Upload the spec and change each `v2.2.0` reference to `v3.0.0`. Reported by [Galih Lasahido](https://github.com/galihlasahido).
- **BREAKING: the Mixer refuses to start without its API.** With no `api.auth_token`, or an API port that is in use, it used to report ready with no management API. It now exits with code 1 before it reports ready, and an API that stops later stops the Mixer. Set `api.auth_token`, or `api.auth_disabled_dangerously = true` for a test.
- **A gear configuration with a key that the gear does not declare is refused.** Before, a misspelt option was ignored and the default stayed in force. This applies at the top level and in every nested object the schema describes with properties. The Mixer refuses the scenario on import, a dry run included, with a 400 naming the gear and the key. A Rack refuses it when it applies the scenario. The key `labels` stays allowed at the top level. Free-form gears declare `additionalProperties: true`. **A scenario using an undeclared key now fails to import. Remove the key.** Known case: `name` in `codec_iso8583`, which nothing reads. The conductor and `io_tcp` schemas now declare `valet.max_in_flight` and `tags`.
- **The Wasm gear applies `memory_limit_pages`, default now 256 pages (16 MiB).** Before, the option had no effect and a module could grow to 4 GiB. The old 16-page default sat below what a module with a 1 MiB stack declares. **An over-limit module no longer loads. A scenario pinning `memory_limit_pages: 16` fails with such a module. Raise the value or remove the key.**

### Added

- **Durable connection events.** `io_iso8583` publishes `opened` and `closed`, in server mode and in client mode, on `flux.evt.conn.<rack>.<gear>`. A `closed` event carries a `reason`: `peer_closed`, `idle_timeout`, `read_error`, `invalid_frame`, `closed_by_command`. A `bento` gear reads them through `ports.events`. Each event arrives as one message with the `conn.id`, so a server can send a logon to a silent client. Several gears can read the same events. A reader that was down receives what it missed inside `max_event_age` (default `30s`). A failing event is redelivered up to `max_deliver` (default 5). A socket never waits for the bus. Each gear's events pass through a bounded queue (`event_queue_size`, default 1024). When the bus is down, an event waits in the Rack outbox. A full outbox drops it with a Warn. The events need a Mixer, also for two gears on one Rack. `publish_events` (default `true`) turns them off. The Mixer provisions the `flux-events` stream, with `snake.events_max_age` (default `1h`) and `snake.events_max_bytes` (default 256 MiB). **A `snake.stream_subjects` that includes `flux.evt.>` collides with this stream.** Native gears read the events with `pkg/events`.
- **`io_iso8583` reply handling and conductor observability.** `on_missing_connection` (server mode, default `drop`) sets what the server does with a reply whose connection is gone. `drop` refuses the reply, as before. `same_remote_ip` sends it on the newest active connection from the request's remote IP. `same_listener` sends it on the newest active connection of the server. A reply for a live connection is never redirected. Behind a NAT or concentrator sharing one IP, `same_remote_ip` can reach another terminal of that IP. Client mode needs no such option. `stop_timeout` (default `5s`) bounds shutdown waits for connection handlers and their events. `0s` does not wait. The conductor logs why a message leaves on the error port. `no_key`, `no_route` and `no_destination` write a Warn. `timeout` and `unmatched_reply` write a Debug. Each line has the reason, the flux id and the connection. It adds the route, destination or missing field where known. It never carries the correlation key value.

### Fixed

- **Wasm module trust and guest memory handling.** A Rack refuses a catalog Wasm module it cannot verify. A `snake://` module with no cluster signature ran with a warning, and ran silently when the Rack had no key. Both now fail at gear start. A `file://` module can still run unsigned for development. The gear decodes a reply before freeing it. The gear frees the input of a dropped or failed message. A returned input block is freed once, not twice. A null-returning `alloc` is now an error. The host no longer writes a message at address 0.
- **`io_iso8583` shutdown and scenario topology.** Connection handlers stop before Stop returns. A graceful Rack stop ends when connections stop reading. Sockets stay open for replies until `Stop`. A handler that was busy stops reading too. `Stop` is twice-safe. The Mixer records every gear, port and wire in its topology registry. A unique index on `(machine_id, type_id)` kept one row per kind per machine and logged `entity name already exists` for the rest. Migration 3 drops the index on Mixer start. The data is kept.
## [v0.13.1] - 2026-10-25 {#v0131}

### Added

- **New timeouts.** `snake.dlq_publish_timeout` (default `5s`, Rack and Mixer) bounds how long one dead letter publish waits for the server. Before, it had only the client's default. `fluxrig check --timeout` (default `5s`) bounds each network check to one attempt. A zero or negative value uses the default. `api.approval_push_timeout` (default `10s`) bounds the push of the active scenario to a Rack that an operator approves.

### Fixed

- **A Rack approved after an activation gets the active scenario.** A pending Rack was not a target of the activation or of the push after its enrollment, so after approval it ran no global gear until something else pushed. Approval now pushes the active scenario to it once. The push does not stop when the client that asked for the approval disconnects.
- **`fluxrig check` is strict and fast.** `fluxrig check -c` reports a configuration that does not load. Before, it ignored the error, so a misspelled key or a bad duration passed while the Rack or Mixer refused to start. `fluxrig check` answers in seconds when the bus is down. Before, it went through the bus client's reconnect loop, about 50 seconds.
- **ISO 8583 fixes.** `iso8583-tool -mode decode` keeps the declared width of a fixed-length field, as `codec_iso8583` does since v0.13.0. STAN `000001` printed as `1`. `io_iso8583` `tpdu_swap` now does what it documents. With `tpdu_enabled`, `preserve_headers` and a 5 byte TPDU, a server reply now carries the TPDU with its destination and source addresses exchanged (`60 DDDD SSSS` becomes `60 SSSS DDDD`). Before, the option was never read and the reply carried the request's TPDU unchanged. Set `tpdu_swap: false` to keep the old behavior. Contributed by [Galih Lasahido](https://github.com/galihlasahido). `io_iso8583` client mode keeps a preserved binary header valid. The header is stored hex-encoded in `iso8583.raw_header`, as in server mode. A header with bytes of 0x80 or above made the message fail validation. Contributed by [Galih Lasahido](https://github.com/galihlasahido). The `payment_switch_two_regions` example loads and declines correctly. The decline builders set `root.mti` and `root."39"`, which the encoder does not read, so no valid response was produced. They now set the metadata key `iso8583.mti` and the field `iso8583.field.39`. The `ports` declaration of the builders moved under `config`, where the gear reads it. Contributed by [Galih Lasahido](https://github.com/galihlasahido).
- **A file `wire.source` cannot leave the spec's directory through a symlink.** The read goes through `os.Root` on that directory.

### Security

- **Built with Go 1.26.10 and `golang.org/x/net` v0.60.0.** `govulncheck` found 23 known vulnerabilities that the code can reach: 18 in the Go standard library and 5 in `x/net`. It finds none now. A binary built from this release is not affected. A binary built from an earlier release, or one you build with an older Go, still is.
## [v0.13.0] - 2026-10-20 {#v0130}

**Before upgrading,** follow [Upgrading fluxrig: from v0.12 to v0.13](https://fluxrig.org/docs/reference/operations/upgrading#v012-to-v013). It gives the steps in order. The BREAKING entries below explain each change.

### Added

- **ISO 20022 support.** New gears `codec_iso20022` (pacs.008/pacs.002, driven by a protocol spec) and `translate` (field maps between specs), plus the bridge tutorial, which `make test-robot-billpay` runs.
- **Mixer spec store and scenario delivery.** `POST /api/v1/specs` and `fluxrig spec import --api` file an artefact that the Mixer checks with its kind's loader. The Mixer sends each Rack the specs its gears name, so a Rack runs a translation with none of its files. `fluxrig scenario import --api --activate` activates after the import.
- **Delivery resilience.** Conductor overload shedding sheds on the error port past `valet.max_in_flight` (default `0`, unbounded). An offline outbox parks bus-bound emissions in memory (`rack.outbox_max_messages`, 10000 by default) and drains on rejoin. A restart with a non-empty outbox loses it. A dead letter queue keeps a message no consumer can process in the `flux-dlq` stream instead of dropping it.
- **Operator tooling and housekeeping.** `fluxrig-mixer --repair` diagnoses a managed stream (healthy, drifted, absent, unreadable) and fixes or recreates it, reporting what was discarded. A telemetry retention sweep deletes old Parquet files. `fluxrig gears doc --write --skip-types` leaves gears undocumented, and `iso8583-tool` sends a bill payment with new auth flags.

### Changed

- **BREAKING: the reference ISO 8583 spec packs a standard bitmap.** `examples/specs/iso8583-v87-ascii.yaml` packed one 128-bit bitmap on every message and never set bit 1. A peer that follows ISO 8583 reads 16 hex characters of primary bitmap, so it read the second half as data, and its own messages did not unpack. The spec now declares an 8-byte primary bitmap, and a secondary bitmap only when a field above 64 is present, announced by bit 1. A peer that was built against the old layout must change with it. The spec's version is now `3.0.0`: a Mixer that already holds `iso8583-v87-ascii:v2.2.0` keeps serving the old layout under that name. Upload the spec and change each `v2.2.0` reference to `v3.0.0`. Reported by [Galih Lasahido](https://github.com/galihlasahido).
- **BREAKING: the Mixer refuses to start without its API.** With no `api.auth_token`, or an API port that is in use, the Mixer used to report ready with no management API. It now exits with code 1 before it reports ready, and an API that stops later stops the Mixer. Set `api.auth_token`, or `api.auth_disabled_dangerously = true` for a test.
- **BREAKING: the registry lives on the bus.** Rack records, the name index, scenario files and the active-scenario pointer now live in a JetStream key-value bucket (`registry.bucket`, default `fluxrig_registry`), not in the Mixer's DuckDB file. Every write is checked against the revision it read, so two Hellos that claim one name can no longer both succeed. Nothing is migrated. Each Rack enrolls again on its own (it waits in `pending` unless `enrollment.auto_adopt` is on, approve each one), import each scenario and activate it, and upgrade every Rack before the Mixer. A Rack of an earlier version under this Mixer stops authenticating. A Rack of this version works under an earlier Mixer.
- **`on_error: reject` emits on an `error` port.** `codec_iso8583`, `codec_iso20022` and `translate` send a rejected message out of their `error` port, so a scenario can answer the sender. An unwired `error` port still drops it.
- **Registry lifecycle.** A restarted Mixer restores its active scenario and reports it, pushing nothing on its own. Presence is separate from status, in a liveness bucket with 90 s expiry. New `[registry]` section: `bucket`, `liveness_bucket`, `replicas` (1), `max_value_size` (1 MiB), `liveness_ttl` (90s), `write_retries` (3), `sweep_interval` (10m). A configuration with a stray or misspelled key now refuses to start and names it. Every pull request needs an issue, and community pull requests target `dev`. Robot runs keep their workspaces under the run root they print.

### Fixed

- **Store and codec correctness.** The roaming example writes its correlation entry before the authorization moves on. `coatcheck` bounds the awaited store write by `store_timeout` (default `5s`). `codec_iso8583` decodes a fixed-length text field at the width the wire carried. `iso8583-tool` packs the reference spec nil-safe and reads fixed fields with their padding.
- **Scenario and enrollment delivery.** Activation pushes a scenario with global gears to every active Rack, and a spec imported while the Mixer runs goes out with the next scenario. A Rack with a new `state_dir` can enroll, and the Conductor routes to an `io_tcp` client. The Mixer's Hello throttle no longer grows without bound.
- **Bus, references and metrics.** The raw bus subscription path now records receive metrics. A fully qualified `urn:flux:<kind>:<name>:<tag>` reference now resolves. A Coat Check entry now carries its own TTL, so it expires without the daemon. Concurrent spec imports no longer lose a version.
- **Small hardening.** The registry repairs what a crash between two writes leaves behind. A duration that does not parse now refuses to start and names the field. `idgen.RandomSuffix` returns an error again instead of a placeholder.
## [v0.12.0] - 2026-09-28 {#v0120}

**Before upgrading.** Most of the changes below are self-resolving once
both sides run the new version, or a one-time config change. One is not:
it is permanent and needs a manual fix. Look for **"Permanent, needs a manual fix"** below and act on it first.

- **Correlation keys are joined unambiguously now, and `coatcheck restore` refuses more.** Coatcheck and conductor key parts used length-ambiguous joins that field bytes (PAN, Track2, binary PIN/MAC/EMV data) could themselves contain. Parts are now length-prefixed. Entries stored by an older version are unreachable, not lost, until their `coatcheck.ttl` passes. `on_missing` now defaults to `error`, not `forward`. Scenarios relying on the old default must set `on_missing: forward`. An unrecognized value is now an error too.
- **`io_tcp` framing and idleness changed.** Client mode now applies configured framing to outbound writes, while server mode framed already. The scanner buffer now fits the largest message the framing allows (up to ~1MB), not `bufio.Scanner`'s ~64KiB default. `idle_timeout` (default 60s) is now enforced. Genuinely idle long-lived connections need it raised or `0` to disable.
- **Conductor reply origins are validated.** The origin stamp must look like a peer identifier (letters, digits, `-`, `_`, up to 128 characters). It was built into a wire port name unvalidated from sender-controlled metadata. Meshes using other characters need renaming. `known_origins` is an opt-in allowlist. Rejections emit `invalid_origin` on the error port alongside existing `error.reason` values.
- **Management and heartbeat paths require secrets.** Every Mixer API route but `/api/v1/health` needs `Authorization: Bearer <api.auth_token>`, compared in constant time. Set `api.auth_token` first, or the Mixer will not start. The opt-out is `api.auth_disabled_dangerously = true`, with a warning on every start. Rack heartbeats authenticate against the registered secret and drop on mismatch. Upgrade Mixer and Racks together. A previous Rack sends no `secret` and is rejected until upgraded.
- **Rack identity is pinned and must be repaired by hand where empty.** Permanent, needs a manual fix: a registry row with an empty stored secret can no longer heartbeat or re-register. Upgrading the Rack does not fix it. An empty secret never matches, by design, on either side. Find affected rows with a direct query against the Mixer's registry store (`store.database_file`, `flux.duckdb` by default): `duckdb flux.duckdb "SELECT machine_id, name FROM registry WHERE type_id = 4 AND (secret IS NULL OR secret = '');"`. For each row returned, `admin racks remove <machine_id>` and let it re-enroll as a fresh Zero-Config identity. This version issues it a real secret on the way back.
- **The guaranteed lane is reliable now.** Activation purges only that Rack's own subjects, not the whole `flux-msg` stream. Messages are acked only after handler success. Failed handlers get up to 5 redeliveries before drop. Undecodable messages are terminated (`Term`), not acked. Teardown waits up to 5 seconds for the hot lane to drain. Neither retry count nor timeout is config-exposed. Emitting gears get `runtime.ErrLaneClosed` on mid-send teardown instead of bare `nil`. Multi-port emissions clone the message before setting `FluxID` and appending a hop. The clone covers `Path` only. OTel context is injected before serialization. `bus.Handler` now returns `error`. `NatsBus.Purge` now takes a `subject string`.
- **Timeouts, environment, CLI, IDs and transport defaults changed.** A `0s` or negative Rack timeout or interval now falls back to its documented default. This covers `snake.connect_timeout`, `snake.reconnect_wait`, `rack.enrollment_timeout`, `rack.enrollment_interval`, `rack.heartbeat_interval` and `snake.inactive_threshold`. Genuine typos now fail loudly. `snake.initial_retry_wait`, `snake.initial_retry_attempts`, `snake.offline_start_timeout`, `snake.offline_retry_interval` and `rack.lane_send_timeout` are unchanged. Mixer env mapping now handles multi-word fields like `FLUXRIG_SNAKE_STREAM_MAX_BYTES`. Rack mapping handles `FLUXRIG_TELEMETRY_*`. List-valued `stream_subjects` from env now split on commas. `fluxrig admin racks remove/suspend/activate/shutdown` now prompt. Scripts must pass `--force` (`-f`). Pre-enrollment entity IDs keep `uuid.NewV7` random bytes in the machine-hint region instead of Nil zero-fill. `idgen.IDGenerator.NewEntityID` now panics if `uuid.NewV7` fails. `NextEntityID` inherits it. The Snake now refuses plaintext connections once TLS certs exist unless `snake.allow_non_tls` is set.

### Added

- Secrets are never serialized and are compared safely. `Secret`, `BootstrapSecret` and `AuthToken` carry `json:"-"` and `cbor:"-"` in registry and config structs. All comparisons (API token, bootstrap, heartbeat) go through one constant-time helper (`security.SecretsEqual`).

### Fixed

- **Coatcheck TTLs and tag lookup.** The per-message TTL override (`Metadata["coatcheck.ttl"]`) applies regardless of `include_values`. The override always reaches the daemon's stored partial clone. Coatcheck entries expire by their own resolved TTL (override, else `default_ttl`, capped at `max_ttl`), not by bucket `max_ttl`. `iso8583.unknown_tags` resolves through `Get("iso8583.unknown_tags")` and coatcheck `key_fields`/`value_fields`.
- **Telemetry and storage.** Deferred telemetry dials carry the main dial's TLS posture (`insecure_skip_verify`). A bad telemetry row no longer aborts its batch. Nil-entity metrics are logged, not silently dropped. `pkg/store/duckdb` `Migrate()` is safe concurrently in-process and cross-process. `FlushTelemetry`/`FlushArchiverBuffer` use bound parameters, not formatted SQL. Concurrent spec imports take `index.lock` on `index.json`, merge into on-disk `index.json` and replace it atomically.
- **Provisioning and activation answers.** Mixer-provisioned KV buckets (currently only `wasm_catalog`) honor `snake.kv_max_bytes`, `snake.kv_max_value_size` and `snake.kv_ttl` (defaults 1 GiB / 1 MiB / no expiry). `archiver_buffer` now flushes to Parquet under `<data_dir>/messages/`. Old unflushed rows appear once on first start. Scenario activation with no target Rack now answers `202 Accepted`, not `500`.
- **Small hardening.** `io_tcp` accepts the largest message its framing allows instead of failing past 64 KiB. A fresh registration issues a unique random secret, and the enrollment reply topic is sanitized. An undecodable message on the bus is explicitly terminated. OTel trace context is injected before serialization. A standalone security regression suite covers auth and secret redaction, and mTLS client-cert verification gets config surface (`snake.tls_ca_file`, `snake.tls_verify`).
## [v0.11.0] - 2026-09-24 {#v0110}

**Before upgrading.** Four changes alter what a running deployment does, and one
cannot be undone by installing the old binary.

- **The Mixer's message store is encrypted on the first start of this version, and
  an older Mixer cannot read it.** Copy the `snake` directory of the Mixer's store
  before the first start if you may need to go back. The key is derived from
  `cluster.key`: replacing that file makes the store unreadable, unless you first
  print the derived key with `fluxrig keys store-key` and keep it as
  `snake.store_old_key_file`. To use a key of your own, set `snake.store_key_file`.
- **The bus now keeps a message for 24 hours and a stream up to 1 GiB**
  (`snake.stream_max_age`, `snake.stream_max_bytes`). It had no limit. Raise them if
  a consumer needs older messages.
- **A wire inside one Rack is delivered through memory, at most once.** A message
  still queued when the Rack process ends is lost, where the bus stored it. Ask for
  `lane: "guaranteed"` on a wire that needs the message stored. An emission that no
  wire consumes is no longer published to the bus, so anything outside the scenario
  that listened for it on the bus stops receiving it.
- **The `io_tcp` server gear and the Bento input gear no longer write the payload
  of a message to the log at INFO.** A procedure that read card data or payloads
  from those logs stops finding them. TRACE writes it with card numbers masked.
- **A message the `io_tcp` server gear cannot route unambiguously is now refused
  instead of guessed at or silently queued.** A message naming a `conn.id` whose
  connection has disconnected used to be queued for whoever connected next, which
  could hand it to the wrong client. It now returns an error. A message naming no
  `conn.id` while several connections are active used to go to "the first" one,
  arbitrarily. It now returns an error too. A message naming none with exactly one
  connection active, or none at all, is unaffected.
- **`Activate` now fails when a scenario's deploy pins reach no Rack at all**, not
  only when one names a Rack that was never enrolled. A `deploy` pin not listed
  under the scenario's own `racks:` section used to compute zero push targets and
  report success anyway, while the scenario stays the Mixer's active one either
  way, and a Rack that enrolls afterwards still receives it.
- **The simulator control endpoints (`POST /api/v1/control/sim/{start,stop,rate,reset}`)
  now answer 503, naming the gear, when nothing acknowledges the command**, instead
  of 200 as soon as NATS accepted the publish. `api.control_confirm_timeout`
  (default `2s`) bounds how long it waits for a gear acknowledgement.

### Added
- **Wires have a lane.** A wire says `lane: "hot"` or `lane: "guaranteed"`. A hot wire inside one Rack goes through memory, delivered at most once and in order. Nothing on a hot wire is stored or reaches the bus. A guaranteed wire goes over the bus, which stores each message before the gear is told it was accepted. A wire naming no lane is hot when both gears run on one Rack and guaranteed between Racks. A hot wire between Racks is rejected at import. A card number on a hot wire is never written to the Mixer's disk. An emission that no wire consumes is dropped instead of published to the bus. `rack.lane_queue_size` (1024) and `rack.lane_send_timeout` (5s) bound each hot wire. A graceful stop delivers what is queued.
- **The Mixer encrypts its message store at rest.** It is on by default, with a key derived from the cluster key or a key file the operator provides (`snake.store_encryption`, `store_cipher`, `store_key_file`, `store_old_key_file`). It covers every message, stream and key-value bucket of the bus. An existing store is converted on the first start with no message lost. A store that was encrypted does not open without its key. `fluxrig keys store-key` prints the derived key for a rotation. The copy of the store directory or a backup no longer shows a message. The Mixer, and a client of the bus, still see clear data. `snake.stream_max_age` (24h) and `snake.stream_max_bytes` (1 GiB) bound the streams of the bus, which had no limit.
- **A Rack starts without the Mixer.** It keeps its own copy of the last scenario it applied (`base.scenario_file`, `scenario.flux` beside its passport, mode `0600`. Empty keeps no copy). With a Passport and a saved scenario whose wires all stay inside the Rack, it runs the scenario on its own and serves traffic. When the Mixer returns, the Rack joins it without stopping the gears. A client that was connected stays connected. Scenarios that use the bus wait for the bus, whole, saying so in the log. Until the Rack is bound, the control plane carries commands in memory between the gears of the Rack. Once joined, it also carries the Mixer's commands to gears that were already running. A Manager can change the bus under its running gears. A Rack with a passport gives the bus `snake.offline_start_timeout` (3s by default) and then runs offline, instead of waiting out the whole retry schedule (about 37 seconds). `0s` restores the unbounded retry. It probes the bus every `snake.offline_retry_interval` (5s by default) and goes online when the bus answers. `0s` turns the probe off. A Rack without a passport keeps the full `snake.initial_retry_*` schedule.
- `gears.RegisterExtension` lets a module outside this repository add gears to the gear factory. The engine never imports that module. The Mixer command line moved to `cmd/fluxrig-mixer/mixercmd`. The Robot runner takes a suite directory given as an absolute path. `FLUXRIG_BIN_DIR` points the suites at another set of binaries. The e2e helpers (`test/e2e/utils/e2e_utils.sh`) honor `FLUXRIG_BIN_DIR`. The `io_tcp` server gear and the Bento input gear no longer write the payload of each message to the log at INFO. No gear writes it at DEBUG. At TRACE, card numbers written as digits keep their first six and last four digits. `io_tcp` gains `max_buffered_messages` (default `1000`). It caps outbound messages queued when nothing is connected, or a message names no `conn.id`. The oldest is dropped past the cap, now counted on `flux.gear.messages_dropped`. `api.control_confirm_timeout` (default `2s`) bounds how long a simulator control command waits for a gear acknowledgement.

### Fixed
- The `io_tcp` server gear's outbound buffer now delivers its whole backlog to a newly-connected client, in the order it was queued. A write failure partway through requeues what was not sent instead of losing it. The hot lane now delivers to every subscriber even when one subscriber's queue is full or stuck. `Unsubscribe` on a hot-lane subscription gives up after a bounded wait and logs, instead of blocking forever. The `io_tcp` client with `read_responses: false` now notices a peer close or reset and reconnects.
- Activating a scenario that deploys to a Rack that is not enrolled and active answers HTTP 409 with the Rack's name, where it answered 500. The scenario stays imported, and activates once the Rack enrolls. Any other activation failure remains a 500. Deploy targets naming a Rack **group** now get a distinct error ("cannot be matched against active Racks yet"). Mixer startup with a pinned scenario no longer logs a false "Failed to activate startup scenario" warning on a normal cold start.
- The spec loader reads `format.layout` of a date or time field (`MMDD`, `hhmmss`, `MMDDhhmmss`, `YYMM`). The `codec_iso8583` decoder nil-pointer panics on composite fields are guarded: nil guards on `GetField()`, `GetSubfields()`, `Bitmap()` and subfields, plus per-subfield panic recovery. A panic anywhere in `decode()` after `Unpack` succeeded is now an error instead of a successful, empty decode. `coatcheck` restoring a vaulted field into a message whose nested data crossed the bus now writes the merged value back onto the live message. `iso8583-tool decode` recovers around `Unpack` and reports a clean error. The data race on `RegisterEntity`'s read of `autoAdopt` is fixed.
## [v0.10.0] - 2026-09-07 {#v0100}

The semantic layer stops being documentation: a spec could state its rules and
nothing applied them.

**Before upgrading.** Two changes refuse specs that used to load, and both are
about a spec declaring what it is. Every spec must carry `spec.id` and
`spec.version`, and every spec must carry a `wire` block saying where its wire
layer comes from. A spec missing either is refused at boot, with a message naming
it, never per transaction. Every spec shipped in this repository is already in
that shape. Both entries under **Changed** say what one that is not needs. New
behaviour is off by default: `validation` starts at `off`, so a message accepted
yesterday is not rejected today because the code was upgraded.

### Added

- **The spec's rules can be enforced on traffic.** `codec_iso8583` gains `validation`: `off` (default), `warn` or `enforce`. Per-MTI field usage, the conditions that decide whether a rule applies, closed value sets and cross-field `checks` are answered against every message the codec decodes or encodes. A rejection follows `on_error`, exactly as a decode failure does. Violations travel on the message as `codec.violations` and are counted on `flux.iso8583.violations` by severity, kind and MTI.
  - The default is `off` deliberately: a message accepted yesterday must not be rejected today because the code was upgraded. `warn` is how you find out whether your spec matches your traffic before enforcing it.
- **A spec can state when a rule applies, and what a message must satisfy.** Fields carry a per-MTI matrix (`usage`, `when`, `values_ref`) and a spec carries `checks`, written in a total, side-effect-free expression language over the message being validated.
  - A `check` carries a severity: `reject` fails the message, `warn` records it and lets it through, which is what makes a rule deployable to a live fleet before it is enforced on one.
  - An element compares the way its `format.kind` says it does. `amount`, `date`, `time`, `datetime` and the new `numeric` kind compare as numbers on every operator, so a rule can write `field(4) == 1000` rather than the element's own zero padding. Every other kind compares as the characters it carries, which is what a response code needs: `"00"` is not `"0"`. `pan` is deliberately excluded, because a leading zero makes it a different card.
- **`fluxrig spec doc` renders the protocol reference from the spec.** The Mixer serves it too, at `GET /api/v1/specs/{name}/{tag}/doc`, and every entry in the spec listing carries the path to its own. Markdown for a repository or a docs site, HTML for a page that is read, printed or mailed. The HTML is self-contained: no scripts, stylesheets or fonts are fetched, so it opens offline. `--scope public` omits every field marked `scope: private` and states how many it withheld. `--scope complete` is the internal view. The per-message tables are derived on every render from the rules stored on the fields, so the two cannot drift.
- **`format.kind: numeric`.** A value that is a number rather than a code (a trace number, a sequence, a count), which had no way to be declared.
- **A stored spec's versions can be listed.** `fluxrig spec history <name>` and `GET /api/v1/specs/{name}` return every version of one spec, newest first by version rather than by arrival. `fluxrig spec list` and the `/specs` listing now carry when each version was filed, its size, the document's title and which version `latest` reaches. `--json` on both.

### Changed

- **BREAKING: a spec declares where its wire layer comes from.** Every spec now carries a `wire` block: `source` names a Moov base (`moov:<name>`) or a wire document beside the spec, `fields` states what the dialect changes, and each is valid alone. Specs in the legacy vocabulary (`meta:` with a top-level `fields:` fusing both layers, `llvar_n`-style type tokens) no longer load. Every spec shipped in this repository is already in the new shape. The [SDL reference](https://fluxrig.org/docs/reference/protocol/iso8583) describes what a spec that is not needs.
- **BREAKING: a spec must declare `spec.id` and `spec.version`.** The schema has required them all along and the loader never read them, so a spec with no version at all loaded clean and its traffic carried a content hash that says which *file* ran and not which *contract*. A spec missing either is now refused at load: at boot, with a message naming the spec, never per transaction. Every spec shipped in this repository already declares both. A spec that does not needs the two fields added before upgrading.
- **`codec_iso8583` manifest: `on_error` is documented as defaulting to `drop`.** It has always defaulted to `drop`. The manifest said `reject`, so an operator reading the published contract configured for one behaviour and got the other.

### Fixed

- **`unknown_tags: drop` rejected instead of dropping.** The policy documented three behaviours and implemented two: anything that was not `preserve` took the same branch, so a spec asking to drop an unknown TLV tag had the whole message rejected, which is the opposite of what it asked for.
- **`VerifyConnectivity` could report convergence on a cancelled context.** The bus delivers to its handlers without consulting the caller's context, so a probe could land after cancellation and Go picked at random between the two ready cases. A caller shutting down was told the telemetry plane is ready.

## [v0.9.0] - 2026-09-03 {#v090}

### Added

- **`coatcheck`: `key_normalize`.** Key field values are canonicalized before they are joined, so the two sides of an exchange agree on a key even when they render the same value differently. `trim` (default) removes surrounding whitespace. `numeric` also drops leading zeros, for fixed-width fields decoded against specs that declare different widths.
- **`coatcheck`: `await_store`.** A store can forward the message without waiting for the write. The default stays blocking, which is right when the reply depends on the entry. Set it false when the entry only enriches a record and holding a message for a control-plane write costs more than losing one.
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
- **Conductor gear (transaction switch)**: routes each request across a destination tree of strategy nodes (`failover`, `round_robin`, `least_loaded`) whose leaves are output ports. It correlates the reply under a ticket. It surfaces timeouts on a dedicated `error` port. Availability sensing binds each local destination to its uplink link-state. The ticket store behind the reply matching is local by default, with per-ticket TTLs and idempotent redemption. A request can exit one Conductor and its reply return through another, routed by an in-band origin stamp. Validated end-to-end by the `payment-switch` e2e and the Conductor stress and chaos Robot suites.
- **Gear manifests**: every gear publishes a manifest (identity, ports, config JSON Schema, terminus). The runtime validates a gear's config against its schema at activation. The Mixer API serves the manifest catalog. `fluxrig gears doc` generates the gear reference documentation from it.
- **Named multi-port gear I/O**: gears declare multiple named input/output ports. The Bento gear honors every declared output port and fails loudly on an ambiguous multi-output config. Native TLS / mTLS on `io_iso8583`. Connection link-state signals (`conn.up` / `conn.down`) on the control plane drive Conductor availability sensing.
- **`fluxrig scenario viz`**: generates an interactive LikeC4 topology model (zones, racks, gears, sockets, the Snake) from a scenario file. `-tags nobento` compiles a Rack without the Bento gear for minimal-footprint deployments.

### Changed
- **Port addressing**: port names are dot-free and wires address the fully-qualified `rack.gear.port`, so a wire endpoint is unambiguous across a multi-rack topology.

### Fixed
- **Reply-correlation timeout under load**: JetStream deduplication is now keyed per subject. Distinct requests no longer collide on the dedup key. Panics on the gear `emit` path are recovered and nil emits dropped. Correlation, match, and park fields resolve as dotted paths (e.g. `iso8583.field.11`) consistently.
- **Loud topology validation**: scenario topology inconsistencies now fail at load instead of surfacing later as runtime errors. The `io_tcp` and `bento` config schemas declare every field the gears actually accept.
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
- Release metadata only. This tag contains no source changes relative to `v0.5.0`. The Wasm work intended for it was not merged and shipped in `v0.6.1` instead.

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
- **Bento Integration**: Native support for the `warpstreamlabs/bento` ecosystem, enabling the Bento connector ecosystem. The standard binary ships the Pure Logic and Local I/O sets. Institutional connectors (Kafka, SQL, AWS) require a custom build.
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

[Unreleased]: https://github.com/jaab-tech/fluxrig/compare/v0.16.0...HEAD
[v0.16.0]: https://github.com/jaab-tech/fluxrig/releases/tag/v0.16.0
[v0.15.1]: https://github.com/jaab-tech/fluxrig/releases/tag/v0.15.1
[v0.15.0]: https://github.com/jaab-tech/fluxrig/releases/tag/v0.15.0
[v0.14.0]: https://github.com/jaab-tech/fluxrig/releases/tag/v0.14.0
[v0.13.1]: https://github.com/jaab-tech/fluxrig/releases/tag/v0.13.1
[v0.13.0]: https://github.com/jaab-tech/fluxrig/releases/tag/v0.13.0
[v0.12.0]: https://github.com/jaab-tech/fluxrig/releases/tag/v0.12.0
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
