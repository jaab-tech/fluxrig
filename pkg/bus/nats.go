// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package bus

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/google/uuid"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

// asyncErrorHandler logs the errors NATS reports apart from any call. A refused
// permission is one: the publish or subscribe that caused it returned nil, and
// a request only times out, so this log is where a missing permission shows.
func asyncErrorHandler(name string) nats.ErrHandler {
	return func(_ *nats.Conn, sub *nats.Subscription, err error) {
		if errors.Is(err, nats.ErrPermissionViolation) {
			slog.Error("NATS Bus: the server refused an operation this connection has no permission for",
				"connection", name, "error", err)
			return
		}
		subject := ""
		if sub != nil {
			subject = sub.Subject
		}
		slog.Warn("NATS Bus: asynchronous error", "connection", name, "subject", subject, "error", err)
	}
}

// connectionHandlers log the life of a connection: when it drops, each
// attempt to reconnect that fails, and the server it reconnects to. Without
// them a client that took a minute to reconnect across a slow link, trying a
// server that had died on every round, left no line in its log.
func connectionHandlers(name string) []nats.Option {
	return []nats.Option{
		nats.DisconnectErrHandler(func(nc *nats.Conn, err error) {
			slog.Warn("NATS Bus: disconnected", "connection", name, "server", nc.ConnectedUrlRedacted(), "error", err)
		}),
		nats.ReconnectErrHandler(func(nc *nats.Conn, err error) {
			slog.Info("NATS Bus: a reconnect attempt failed", "connection", name, "error", err)
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			slog.Info("NATS Bus: reconnected", "connection", name, "server", nc.ConnectedUrlRedacted())
		}),
	}
}

// NatsBus is the concrete implementation of the Bus interface using NATS JetStream.
type NatsBus struct {
	conn              *nats.Conn
	js                jetstream.JetStream
	streamName        string
	retryWait         time.Duration
	retryAttempts     int
	inactiveThreshold time.Duration
	keepConsumers     bool
	opTimeout         time.Duration
	ackWait           time.Duration
	consumerReplicas  int
	missedHeartbeats  int
	// streamReplicas caches the stream's replica count for consumers that
	// take it (consumerReplicas 0); 0 until read.
	streamReplicas    atomic.Int32
	dlqEnabled        bool
	dlqSubject        string
	dlqPublishTimeout time.Duration
	// dlqFailures counts messages the bus could not route to the dead letter
	// queue. Read it with DLQFailures.
	dlqFailures atomic.Uint64
	// failoverAfter and pubMisses implement ConnectOptions.FailoverAfter:
	// pubMisses counts publishes in a row that found no stream.
	failoverAfter int32
	pubMisses     atomic.Int32
	// probing is set while a failover checks whether the server still
	// reaches JetStream, so a burst of misses runs one check.
	probing atomic.Bool
	// closed ends the retries of a subscription that is still starting: it
	// closes with Close, or when ConnectOptions.Ctx is done.
	closed    chan struct{}
	closeOnce sync.Once
}

// NewNatsBus creates a new instance.
func NewNatsBus(streamName string) *NatsBus {
	return &NatsBus{
		streamName: streamName,
		closed:     make(chan struct{}),
	}
}

// Connect establishes the connection to the NATS server and initializes JetStream.
// Implements retry with exponential backoff for initial connection to handle
// race conditions where the NATS server reports ready but isn't accepting connections yet.
func (n *NatsBus) Connect(url string, opts ConnectOptions) error {
	// 1. Build Options
	natsOpts := []nats.Option{
		nats.Name(opts.Name),
		nats.Timeout(opts.ConnectTimeout),
		nats.ReconnectWait(opts.ReconnectWait),
		nats.MaxReconnects(-1), // Infinite reconnects
		nats.ErrorHandler(asyncErrorHandler(opts.Name)),
	}
	natsOpts = append(natsOpts, connectionHandlers(opts.Name)...)

	// Bound a write that cannot be flushed. The client default is one minute,
	// and a request or a force-reconnect to a black-holed server then sits in
	// the socket write for that long: measured in the latam testbed, a Rack
	// cut off from its server moved 73 s after the cut, past the 60 s that
	// gate 1 allows. The operation timeout is the contract for one exchange,
	// so a write that outlasts it is already too late.
	if opts.OperationTimeout > 0 {
		natsOpts = append(natsOpts, nats.FlusherTimeout(opts.OperationTimeout))
	}

	// --- In-Process Optimization ---
	if opts.InProcessServer != nil {
		url = "nats://localhost:4222" // Dummy URL required by NATS client even for in-process
		// Use special InProcess provider if we can cast it
		if srv, ok := opts.InProcessServer.(interface {
			InProcessConn(opts ...nats.Option) (*nats.Conn, error)
		}); ok {
			slog.Info("Establishing In-Process NATS connection (Bypassing network stack)")
			nc, err := srv.InProcessConn(natsOpts...)
			if err != nil {
				return err
			}
			n.conn = nc
			return n.finalizeConnect(opts)
		}
	}

	// --- Security ---
	sec := Security{
		RootCA:             opts.RootCA,
		ClientCert:         opts.ClientCert,
		ClientKey:          opts.ClientKey,
		Credentials:        opts.Credentials,
		InboxPrefix:        opts.InboxPrefix,
		PingInterval:       opts.PingInterval,
		MaxPingsOut:        opts.MaxPingsOut,
		InsecureSkipVerify: opts.InsecureSkipVerify,
	}
	if !opts.External {
		// The embedded server's certificate names the domain.
		sec.ServerName = opts.Domain
	}
	secOpts, err := sec.Options()
	if err != nil {
		return err
	}
	natsOpts = append(natsOpts, secOpts...)

	// 2. Connect to NATS Core with retry logic for initial connection
	nc, err := dialWithRetry(url, natsOpts, opts)
	if err != nil {
		return err
	}
	n.conn = nc

	return n.finalizeConnect(opts)
}

// Defaults for the initial connection retry, used when ConnectOptions leaves
// them unset. Rack and Mixer configuration set them explicitly
// (snake.initial_retry_wait and snake.initial_retry_attempts).
const (
	defaultInitialRetryWait     = 500 * time.Millisecond
	defaultInitialRetryAttempts = 10
	initialRetryBackoff         = 1.5
)

// maxGuaranteedRedeliveries bounds how many times JetStream redelivers a
// message on either guaranteed-lane consumer (Subscribe and SubscribeDurable)
// after a Nak, so a handler that fails deterministically on one message does
// not retry it forever. Past this the server simply stops redelivering it; a
// var, not a config field yet, the same trade-off lane.go's unsubscribeWait
// already documents for the same reason: making it operator-configurable is a
// reasonable follow-up, not done here.
var maxGuaranteedRedeliveries = 5

// dialWithRetry connects to url and retries a failed first connection with
// exponential backoff. It gives up after InitialRetryAttempts tries or, when
// InitialRetryTimeout is set, as soon as the next wait would carry it past that
// bound. A single attempt is also clamped to the time left, so a host that never
// answers cannot hold the caller beyond the bound.
func dialWithRetry(url string, natsOpts []nats.Option, opts ConnectOptions) (*nats.Conn, error) {
	wait := opts.InitialRetryWait
	if wait == 0 {
		wait = defaultInitialRetryWait
	}
	attempts := opts.InitialRetryAttempts
	if attempts == 0 {
		attempts = defaultInitialRetryAttempts
	}
	var deadline time.Time
	if opts.InitialRetryTimeout > 0 {
		deadline = time.Now().Add(opts.InitialRetryTimeout)
	}

	for attempt := 1; ; attempt++ {
		attemptOpts := natsOpts
		if !deadline.IsZero() {
			remaining := time.Until(deadline)
			if remaining < opts.ConnectTimeout || opts.ConnectTimeout == 0 {
				attemptOpts = append(append([]nats.Option{}, natsOpts...), nats.Timeout(max(remaining, time.Millisecond)))
			}
		}

		nc, err := nats.Connect(url, attemptOpts...)
		if err == nil {
			slog.Info("NATS connection established", "attempt", attempt)
			return nc, nil
		}

		if attempt >= attempts {
			return nil, fmt.Errorf("failed to connect to NATS after %d attempts: %w", attempt, err)
		}
		if !deadline.IsZero() && time.Now().Add(wait).After(deadline) {
			return nil, fmt.Errorf("failed to connect to NATS after %d attempts within %s: %w", attempt, opts.InitialRetryTimeout, err)
		}

		slog.Info("NATS connection failed, retrying...", "attempt", attempt, "max_attempts", attempts, "error", err, "next_retry_in", wait)
		if opts.Ctx != nil {
			select {
			case <-opts.Ctx.Done():
				return nil, fmt.Errorf("failed to connect to NATS after %d attempts: %w", attempt, opts.Ctx.Err())
			case <-time.After(wait):
			}
		} else {
			time.Sleep(wait)
		}
		wait = time.Duration(float64(wait) * initialRetryBackoff)
	}
}

func (n *NatsBus) finalizeConnect(opts ConnectOptions) error {
	n.retryWait = opts.SubscriptionRetryWait
	n.retryAttempts = opts.SubscriptionRetryAttempts
	if n.retryAttempts <= 0 {
		n.retryAttempts = 1 // At least one attempt
	}
	n.inactiveThreshold = opts.InactiveThreshold
	n.keepConsumers = opts.KeepConsumers
	n.opTimeout = opts.OperationTimeout
	n.ackWait = opts.AckWait
	n.consumerReplicas = opts.ConsumerReplicas
	n.missedHeartbeats = opts.ConsumerMissedHeartbeats
	if opts.Ctx != nil {
		go func() {
			select {
			case <-opts.Ctx.Done():
				n.closeOnce.Do(func() { close(n.closed) })
			case <-n.closed:
			}
		}()
	}
	n.dlqEnabled = opts.DLQEnabled
	n.failoverAfter = int32(max(opts.FailoverAfter, 0)) //nolint:gosec // a small count from config
	n.dlqSubject = opts.DLQSubject
	if n.dlqSubject == "" {
		n.dlqSubject = dlqDefaultSubject
	}
	n.dlqPublishTimeout = opts.DLQPublishTimeout
	if n.dlqPublishTimeout <= 0 {
		n.dlqPublishTimeout = defaultDLQPublishTimeout
	}

	// 3. Initialize JetStream
	js, err := jetstream.New(n.conn)
	if err != nil {
		return fmt.Errorf("failed to initialize jetstream: %w", err)
	}
	n.js = js

	return nil
}

// noStream reports whether a publish error means no stream answered through
// this server: the request timed out or found no responders. A caller's own
// cancellation, a refused message or a payload limit does not count.
func noStream(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, nats.ErrTimeout) ||
		errors.Is(err, nats.ErrNoResponders) ||
		errors.Is(err, jetstream.ErrNoStreamResponse)
}

// Unavailable reports whether a JetStream request failed because the cluster
// cannot answer it yet: a 503 from the API, which a server returns while it
// elects a metadata leader, or no stream that answers. Such a failure passes
// once the cluster settles; a caller can try again instead of giving up.
func Unavailable(err error) bool {
	var apiErr *jetstream.APIError
	if errors.As(err, &apiErr) && apiErr.Code == http.StatusServiceUnavailable {
		return true
	}
	return noStream(err)
}

// NotProvisioned reports whether a request failed because its stream does not
// exist yet. On an external NATS a Rack can start before any Mixer has
// provisioned the streams, and the stream appears once one does.
func NotProvisioned(err error) bool {
	return errors.Is(err, jetstream.ErrStreamNotFound)
}

// Disconnected reports whether a request failed because its connection
// dropped, was reconnecting, or was closed under it. A failover reconnect
// closes the connection on purpose, and a request in flight then fails with
// "connection closed": a Rack that is starting tries again on a new session,
// as it does when the cluster cannot answer yet.
func Disconnected(err error) bool {
	return errors.Is(err, nats.ErrConnectionClosed) ||
		errors.Is(err, nats.ErrConnectionReconnecting) ||
		errors.Is(err, nats.ErrDisconnected)
}

// notePublish counts publishes in a row that found no stream. When the count
// reaches failoverAfter it forces a reconnect, which moves the connection to
// another server in the URL list: the current one goes to the end of the
// pool. Any publish that reaches a stream resets the count.
//
// A cancelled publish is skipped: the caller went away, and that says nothing
// about the server. Every timeout counts, including one where the caller's own
// deadline expired. A server that is black-holed keeps accepting writes until
// the socket buffer fills, and the client's ping queues behind that data, so
// the publish timeout is the only signal that arrives. Measured in the latam
// testbed: without that count a cut-off Rack moved 73 s later, past the 60 s
// that gate 1 allows.
func (n *NatsBus) notePublish(subject string, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	n.noteMiss(subject, noStream(err))
}

// noteRequest counts a JetStream API request (a consumer create or lookup)
// toward the same failover: a server cut off from its cluster answers every
// one with 503, and an idle Rack publishes too seldom to notice it soon.
//
// Only an answer that says the server has no JetStream counts: a 503, or no
// responders. A timeout does not: while the whole cluster elects after a
// member dies every server times out, and moving the connection then gains
// nothing and costs a reconnect each time.
func (n *NatsBus) noteRequest(subject string, err error) {
	n.noteMiss(subject, err != nil && noJetStream(err))
}

// noJetStream reports an answer from a server that has no JetStream to
// offer: a 503 from the API, or no responders.
func noJetStream(err error) bool {
	var apiErr *jetstream.APIError
	if errors.As(err, &apiErr) && apiErr.Code == http.StatusServiceUnavailable {
		return true
	}
	return errors.Is(err, nats.ErrNoResponders)
}

// noteMiss counts a request in a row that found no stream or no JetStream,
// and resets the count on one that did.
func (n *NatsBus) noteMiss(subject string, miss bool) {
	if n.failoverAfter <= 0 {
		return
	}
	if !miss {
		n.pubMisses.Store(0)
		return
	}
	if n.pubMisses.Add(1) < n.failoverAfter {
		return
	}
	n.pubMisses.Store(0)
	if !n.probing.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer n.probing.Store(false)
		n.failoverIfCutOff(subject)
	}()
}

// defaultFailoverProbeTimeout bounds the JetStream check of a failover on a
// bus with no operation timeout (snake.operation_timeout sets it otherwise).
const defaultFailoverProbeTimeout = 5 * time.Second

// failoverProbeInterval is the pause between two JetStream checks of a
// failover, while it waits out a possible election.
const failoverProbeInterval = time.Second

// failoverIfCutOff moves the connection to another server unless this one
// still reaches JetStream. A stream that elects a leader, after a NATS
// server died, answers no publish for some seconds through every server:
// moving then gains nothing, and costs a reconnect, across a slow link a
// long one. A server cut off from its cluster cannot answer the JetStream
// account info either, and the connection moves.
//
// While the cluster elects its metadata leader, a healthy server cannot
// answer the account info for some seconds either. So the check is asked
// again for a while, twice the operation timeout, which outlasts an
// election (the raft election timeout of NATS is 4 to 9 seconds): one answer
// keeps the connection, and only a server that never answers is left.
func (n *NatsBus) failoverIfCutOff(subject string) {
	server := n.conn.ConnectedUrlRedacted()
	timeout := n.opTimeout
	if timeout <= 0 {
		timeout = defaultFailoverProbeTimeout
	}
	deadline := time.Now().Add(2 * timeout)
	for n.js != nil && n.conn.IsConnected() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		err := n.accountInfoBounded(ctx)
		cancel()
		if err == nil {
			slog.Info("NATS Bus: no stream answered, but this server reaches JetStream; staying (a stream may be electing its leader)",
				"server", server, "subject", subject)
			return
		}
		if !time.Now().Before(deadline) {
			break
		}
		time.Sleep(failoverProbeInterval) // the pause between two checks of an election
	}
	if !n.conn.IsConnected() {
		return // the client is reconnecting already
	}
	if errRe := n.conn.ForceReconnect(); errRe != nil {
		slog.Error("NATS Bus: failover reconnect failed", "server", server, "error", errRe)
		return
	}
	slog.Warn("NATS Bus: no stream answered through this server, reconnecting to another",
		"server", server, "subject", subject, "failed_publishes", n.failoverAfter)
}

// accountInfoBounded asks the server whether it still reaches JetStream, and
// gives up when ctx ends. The request cannot do that by itself: when the
// server is black-holed the request can sit in a blocked socket write, which
// nats.go bounds by its flusher timeout of one minute, not by the request
// context. Measured in the latam testbed: one probe iteration took 70 s and
// the Rack moved past the 60 s that gate 1 allows.
func (n *NatsBus) accountInfoBounded(ctx context.Context) error {
	done := make(chan error, 1)
	go func() {
		_, err := n.js.AccountInfo(ctx)
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// deadLetter routes data to the DLQ when enabled and returns true if the
// message was handed off. A DLQ failure is logged, counted (DLQFailures),
// and reported as false so the caller Nak's for redelivery instead of
// dropping the poison message with only a log. The publish is bounded by dlqPublishTimeout and by
// ctx, whichever ends first. A consumer callback has no parent context before
// it decodes the message, so its decode and validate paths pass
// context.Background() and rely on the timeout.
func (n *NatsBus) deadLetter(ctx context.Context, originSubject string, data []byte, reason string, deliveries uint64) bool {
	if !n.dlqEnabled {
		return false
	}
	timeout := n.dlqPublishTimeout
	if timeout <= 0 {
		timeout = defaultDLQPublishTimeout
	}
	if err := routeToDLQ(ctx, n.js, n.dlqSubject, originSubject, data, reason, deliveries, timeout); err != nil {
		n.dlqFailures.Add(1)
		slog.Error("NATS Bus: failed to route message to the dead letter queue",
			"origin_subject", originSubject,
			"reason", reason,
			"error", err,
		)
		return false
	}
	return true
}

// settlePoison settles a message no consumer could process (undecodable,
// invalid, or failed past redelivery). routed reports whether deadLetter
// handed it to the DLQ. A DLQ write that failed Naks for redelivery instead
// of dropping the message with only a log; anything else Terms it, including
// a disabled DLQ, which keeps its documented log-and-discard. It returns
// whether the message is settled, for the resume accounting of Subscribe.
func (n *NatsBus) settlePoison(msg jetstream.Msg, routed bool) bool {
	if n.dlqEnabled && !routed {
		_ = msg.Nak()
		return false
	}
	_ = msg.Term()
	return true
}

// DLQFailures counts messages the bus could not route to the dead letter
// queue since Connect: the poison message was Nak'd for redelivery instead of
// dropped, so a growing count says the DLQ stream itself is unhealthy.
func (n *NatsBus) DLQFailures() uint64 {
	return n.dlqFailures.Load()
}

// Publish serializes the FluxMsg to CBOR bytes and sends it via JetStream.
func (n *NatsBus) Publish(ctx context.Context, subject string, msg *fluxmsg.FluxMsg) error {
	if n.js == nil {
		return errors.New("nats bus not connected")
	}

	// 1. Technical Enforcement (Transparent UTF-8 Guard)
	if err := msg.Validate(); err != nil {
		return fmt.Errorf("%w: fluxmsg validation failed: %w", ErrInvalidMessage, err)
	}

	// 2. Inject Traces (OTel). Must happen before CBOR marshaling below: the
	// injected context lands in msg.Metadata, and a marshal taken before this
	// serializes the message without it, so the trace never reaches the wire.
	// Metadata is initialized defensively here too: every production
	// construction goes through fluxmsg.New(), which never leaves it nil, but
	// propagation.MapCarrier.Set panics on a nil map, and this is the one place
	// that would ever call it on a hand-built FluxMsg that skipped New().
	if ctx != nil {
		if msg.Metadata == nil {
			msg.Metadata = make(map[string]string)
		}
		otel.GetTextMapPropagator().Inject(ctx, propagation.MapCarrier(msg.Metadata))
	}

	// 3. Serialize to Binary (CBOR)
	data, err := cbor.Marshal(msg)
	if err != nil {
		return fmt.Errorf("%w: encode: %w", ErrInvalidMessage, err)
	}

	// 4. Technical Logging (Avoid recursion for telemetry)
	if !strings.Contains(subject, "telemetry") && !strings.Contains(subject, "logs") && !strings.Contains(subject, "metrics") {
		slog.Debug("NATS Bus: Publish",
			"subject", subject,
			"flux_id", msg.FluxID.String(),
		)
	}

	// 5. Send Bytes (Persistent) with Deduplication ID.
	// The dedup key must be unique per logical emission, not just per message:
	// a fan-out gear (e.g. the Conductor) emits the SAME FluxID at the SAME hop
	// count to more than one destination, and a parked-then-replayed request
	// (a timeout re-emission of the original) shares FluxID and Path length with
	// the outbound copy it was cloned from. Keying on FluxID+pathlen alone made
	// those collide, so the second publish fell inside JetStream's dedup window
	// and was silently dropped. Scoping by subject (as PublishRaw already does)
	// keeps genuine same-message-same-destination idempotency while letting the
	// same message reach distinct ports.
	msgID := fmt.Sprintf("%s-%d-%s", msg.FluxID.String(), len(msg.Path), subject)

	_, err = n.js.Publish(ctx, subject, data, jetstream.WithMsgID(msgID))
	n.notePublish(subject, err)
	if err != nil {
		slog.Error("NATS Bus Publish Failed", "subject", subject, "error", err)
		if errors.Is(err, nats.ErrMaxPayload) {
			return fmt.Errorf("%w: %w", ErrInvalidMessage, err)
		}
	}
	return err
}

// PublishRaw sends pre-serialized data (CBOR) with a specific deduplication ID.
func (n *NatsBus) PublishRaw(ctx context.Context, subject string, data []byte, fluxID uuid.UUID) error {
	if n.js == nil {
		return errors.New("nats bus not connected")
	}

	// Send Bytes (Persistent) with Deduplication ID
	msgID := fmt.Sprintf("%s-%s", fluxID.String(), subject)
	_, err := n.js.Publish(ctx, subject, data, jetstream.WithMsgID(msgID))
	n.notePublish(subject, err)
	if err != nil {
		slog.Error("NATS Bus PublishRaw Failed", "subject", subject, "error", err)
	}
	return err
}

// PublishRawBatch implements BatchRawPublisher. The messages leave in order on
// this one connection, so the stream stores them in order, and each carries the
// deduplication ID that PublishRaw gives it.
func (n *NatsBus) PublishRawBatch(ctx context.Context, subject string, msgs []RawMessage) (int, error) {
	if n.js == nil {
		return 0, errors.New("nats bus not connected")
	}
	acks := make([]jetstream.PubAckFuture, 0, len(msgs))
	for _, m := range msgs {
		f, err := n.js.PublishAsync(subject, m.Data, jetstream.WithMsgID(fmt.Sprintf("%s-%s", m.ID.String(), subject)))
		if err != nil {
			n.notePublish(subject, err)
			slog.Error("NATS Bus PublishRawBatch Failed", "subject", subject, "sent", len(acks), "of", len(msgs), "error", err)
			return n.awaitAcks(ctx, subject, acks, len(msgs), err)
		}
		acks = append(acks, f)
	}
	return n.awaitAcks(ctx, subject, acks, len(msgs), nil)
}

// awaitAcks waits for the acknowledgements in order, and returns how many of
// the first ones arrived. sendErr is the error that stopped the sending, when
// one did after the acknowledged ones. Each acknowledgement gets the full
// timeout: one timer shared by the whole batch would let a slow link spend
// the budget on the first messages and fail the rest, degrading healthy
// batches into partial sends with full resends.
func (n *NatsBus) awaitAcks(ctx context.Context, subject string, acks []jetstream.PubAckFuture, total int, sendErr error) (int, error) {
	timeout := n.opTimeout
	if timeout <= 0 {
		timeout = defaultFailoverProbeTimeout
	}
	for i, f := range acks {
		wait := time.NewTimer(timeout)
		var err error
		select {
		case <-f.Ok():
		case err = <-f.Err():
		case <-wait.C:
			err = context.DeadlineExceeded
		case <-ctx.Done():
			err = ctx.Err()
		}
		wait.Stop()
		n.notePublish(subject, err)
		if err != nil {
			slog.Error("NATS Bus PublishRawBatch Failed", "subject", subject, "acknowledged", i, "of", total, "error", err)
			return i, err
		}
	}
	if sendErr != nil {
		return len(acks), sendErr
	}
	return len(acks), nil
}

// Subscribe listens for messages using a JetStream Consumer.
// Basic Bus uses Ephemeral Ordered Consumer to mimic simple sub behavior.
func (n *NatsBus) Subscribe(subject string, handler Handler) (Subscription, error) {
	if n.js == nil {
		return nil, errors.New("nats bus not connected")
	}

	// An ephemeral pull consumer, created with a short retry loop to handle
	// propagation latency during promotion.
	slog.Info("NATS Subscribe Initiated", "stream", n.streamName, "subject", subject)

	// The consumer is replaced if it is lost with its server (liveConsumer).
	live := newLiveConsumer(n.js, n.noteRequest, n.streamName, "flux-sub", jetstream.ConsumerConfig{
		FilterSubject:     subject,
		AckPolicy:         jetstream.AckExplicitPolicy, // Reliable baseline
		MaxAckPending:     10000,                       // Prevent stalls during high-TPS
		AckWait:           n.ackWait,                   // A message delivered through a server that died comes again after it
		MaxDeliver:        maxGuaranteedRedeliveries,   // Bound retries on a handler that fails every time
		InactiveThreshold: n.inactiveThreshold,         // Autonomous server-side cleanup if client crashes
	}, n.retryWait, n.opTimeout, func(msg jetstream.Msg) bool {
		var fluxMsg fluxmsg.FluxMsg

		if errUnmarshal := cbor.Unmarshal(msg.Data(), &fluxMsg); errUnmarshal != nil {
			// A message that cannot be decoded used to vanish here without a
			// trace, which reads downstream as a gear that never received
			// anything rather than as a message that was thrown away. It goes
			// to the dead letter stream first when that is enabled, then Term
			// (not Ack): it will not decode on redelivery either, and Ack would
			// look identical to a message the handler actually processed.
			slog.Error("NATS Bus: Discarding undecodable message",
				"subject", subject,
				"bytes", len(msg.Data()),
				"error", errUnmarshal,
			)
			// A DLQ write that fails must not drop the message with only a
			// log: settlePoison Nak's it for redelivery instead of Term-ing.
			return n.settlePoison(msg, n.deadLetter(context.Background(), subject, msg.Data(), DLQReasonDecode, deliveryCount(msg)))
		}

		// Ingress validation: a message from another Rack has never been through
		// this process's own Publish, so its hop count and shape are only
		// checked here, not assumed from having passed Validate once already.
		if errValidate := fluxMsg.Validate(); errValidate != nil {
			slog.Error("NATS Bus: Discarding invalid message",
				"subject", subject,
				"flux_id", fluxMsg.FluxID.String(),
				"error", errValidate,
			)
			return n.settlePoison(msg, n.deadLetter(context.Background(), subject, msg.Data(), DLQReasonValidate, deliveryCount(msg)))
		}

		// Extract Traces (OTel)
		ctx := context.Background()
		ctx = otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier(fluxMsg.Metadata))

		// Acked only once the handler actually succeeds. Acking up front, as this
		// used to, meant any failure afterward (a panic recovered further down
		// the call chain, a downstream publish error) was unrecoverable: at-most-
		// once delivery disguised as the guaranteed lane it was supposed to be.
		if errHandle := handler(ctx, &fluxMsg); errHandle != nil {
			deliveries := deliveryCount(msg)
			// On the last redelivery the server would stop anyway, leaving the
			// message to age out unread. Route it to the dead letter stream and
			// settle it, so the operator can find it, instead of Nak-ing into
			// silence.
			if int(deliveries) >= maxGuaranteedRedeliveries {
				slog.Error("NATS Bus: Handler failed on the last redelivery, routing to dead letter queue",
					"subject", subject,
					"flux_id", fluxMsg.FluxID.String(),
					"deliveries", deliveries,
					"error", errHandle,
				)
				return n.settlePoison(msg, n.deadLetter(ctx, subject, msg.Data(), DLQReasonRedeliveryExhausted, deliveries))
			}
			slog.Error("NATS Bus: Handler failed, message will be redelivered",
				"subject", subject,
				"flux_id", fluxMsg.FluxID.String(),
				"error", errHandle,
			)
			_ = msg.Nak()
			return false
		}
		_ = msg.Ack()
		return true
	})
	// As many copies as the stream by default: a consumer with one lives on
	// one Mixer, and a partition of that Mixer can make it skip messages its
	// stale copy of the stream never had.
	live.replicas = n.subscriptionReplicas
	live.maxMissed = n.missedHeartbeats
	live.keep = n.keepConsumers
	live.busClosed = n.closed
	if err := live.start(n.retryAttempts); err != nil {
		return nil, fmt.Errorf("failed to create ephemeral consumer for %s: %w", subject, err)
	}
	return live, nil
}

// subscriptionReplicas is the replica count of an acknowledged
// subscription's consumer: ConsumerReplicas, or the stream's own count when
// that is 0. A stream that cannot be read is an error, never 0: the server
// makes an ephemeral consumer created with 0 replicas a single replica.
func (n *NatsBus) subscriptionReplicas(ctx context.Context) (int, error) {
	if n.consumerReplicas > 0 {
		return n.consumerReplicas, nil
	}
	if r := n.streamReplicas.Load(); r > 0 {
		return int(r), nil
	}
	s, err := n.js.Stream(ctx, n.streamName)
	if err != nil {
		return 0, fmt.Errorf("bus: read stream %s: %w", n.streamName, err)
	}
	r := s.CachedInfo().Config.Replicas
	if r <= 0 {
		return 0, fmt.Errorf("bus: stream %s reports %d replicas", n.streamName, r)
	}
	n.streamReplicas.Store(int32(r)) //nolint:gosec // a replica count
	return r, nil
}

// SubscribeRaw listens for raw messages using a JetStream Consumer bound to a specific stream.
func (n *NatsBus) SubscribeRaw(subject string, streamName string, handler RawHandler) (Subscription, error) {
	if n.js == nil {
		return nil, errors.New("nats bus not connected")
	}
	// An ephemeral pull consumer, replaced if it is lost with its server.
	live := newLiveConsumer(n.js, n.noteRequest, streamName, "flux-raw", jetstream.ConsumerConfig{
		FilterSubject:     subject,
		AckPolicy:         jetstream.AckNonePolicy,
		InactiveThreshold: n.inactiveThreshold, // Autonomous server-side cleanup
	}, n.retryWait, n.opTimeout, func(msg jetstream.Msg) bool {
		_ = msg.Ack()
		handler(context.Background(), msg.Subject(), msg.Data())
		return true
	})
	live.maxMissed = n.missedHeartbeats
	live.keep = n.keepConsumers
	live.busClosed = n.closed
	if err := live.start(1); err != nil {
		return nil, fmt.Errorf("failed to create ephemeral consumer (raw): %w", err)
	}
	return live, nil
}

// SubscribeDurable listens for messages using a persistent consumer (durableName).
func (n *NatsBus) SubscribeDurable(subject, durableName string, handler Handler) (Subscription, error) {
	if n.js == nil {
		return nil, errors.New("nats bus not connected")
	}

	// The create is bounded by the operation timeout, not the caller's
	// context: an unbounded wait here held the subscriber for as long as
	// the cluster was unreachable.
	timeout := n.opTimeout
	if timeout <= 0 {
		timeout = defaultFailoverProbeTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// A durable consumer keeps its replica count for life, like the
	// ephemeral ones do: one created without a count during an election
	// would stay a single replica on one server. An unreadable stream fails
	// the subscribe, as it does for Subscribe.
	replicas, err := n.subscriptionReplicas(ctx)
	n.noteRequest(subject, err)
	if err != nil {
		return nil, err
	}

	// 1. Create Durable Consumer
	// DeliverAllPolicy ensures full stream delivery (or resume).
	// MaxAckPending needs to be reasonable for flow control.
	cons, err := n.js.CreateOrUpdateConsumer(ctx, n.streamName, jetstream.ConsumerConfig{
		Durable:       durableName,
		FilterSubject: subject,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
		MaxDeliver:    maxGuaranteedRedeliveries, // Same bound as Subscribe: a handler that fails every time must not retry forever
		AckWait:       n.ackWait,
		Replicas:      replicas,
	})
	n.noteRequest(subject, err)
	if err != nil {
		return nil, err
	}

	// 2. Consume Messages. The lifecycle context is independent of the
	// bounded create above: cancelling that at return must not end the
	// subscription.
	_, stop := context.WithCancel(context.Background())

	cc, err := cons.Consume(func(msg jetstream.Msg) {
		// Deserialize
		var fluxMsg fluxmsg.FluxMsg
		if errUnmarshal := cbor.Unmarshal(msg.Data(), &fluxMsg); errUnmarshal != nil {
			// Term rather than left to redeliver: a message that cannot be
			// decoded will not decode on the next attempt either, and holding
			// it stalls the consumer. It goes to the dead letter stream first
			// when that is enabled, then Term (not Ack): Term makes it explicit
			// instead of looking identical to a message the handler processed.
			slog.Error("NATS Bus: Discarding undecodable message",
				"subject", subject,
				"bytes", len(msg.Data()),
				"error", errUnmarshal,
			)
			n.settlePoison(msg, n.deadLetter(context.Background(), subject, msg.Data(), DLQReasonDecode, deliveryCount(msg)))
			return
		}

		if errValidate := fluxMsg.Validate(); errValidate != nil {
			slog.Error("NATS Bus: Discarding invalid message",
				"subject", subject,
				"flux_id", fluxMsg.FluxID.String(),
				"error", errValidate,
			)
			n.settlePoison(msg, n.deadLetter(context.Background(), subject, msg.Data(), DLQReasonValidate, deliveryCount(msg)))
			return
		}

		// Extract Traces (OTel)
		ctx := context.Background()
		ctx = otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier(fluxMsg.Metadata))

		if errHandle := handler(ctx, &fluxMsg); errHandle != nil {
			deliveries := deliveryCount(msg)
			// On the last redelivery the server would stop anyway and the
			// message would age out unread. Route it to the dead letter stream
			// and settle it, so it stays findable.
			if int(deliveries) >= maxGuaranteedRedeliveries {
				slog.Error("NATS Bus: Handler failed on the last redelivery, routing to dead letter queue",
					"subject", subject,
					"flux_id", fluxMsg.FluxID.String(),
					"deliveries", deliveries,
					"error", errHandle,
				)
				n.settlePoison(msg, n.deadLetter(ctx, subject, msg.Data(), DLQReasonRedeliveryExhausted, deliveries))
				return
			}
			slog.Error("NATS Bus: Handler failed, message will be redelivered",
				"subject", subject,
				"flux_id", fluxMsg.FluxID.String(),
				"error", errHandle,
			)
			_ = msg.Nak()
			return
		}
		_ = msg.Ack()
	})

	if err != nil {
		stop()
		return nil, err
	}

	return &natsSubscription{
		cc:         cc,
		cancel:     stop,
		js:         n.js,
		streamName: n.streamName,
		consName:   cons.CachedInfo().Name,
		isDurable:  true,
		opTimeout:  n.opTimeout,
	}, nil
}

func (n *NatsBus) Core() any {
	return n.conn
}

// JetStream returns the bus's JetStream handle, or nil before Connect. The
// Mixer uses it to open the registry buckets, which need the typed API that
// KeyValue does not expose (create-only and revision-checked writes).
func (n *NatsBus) JetStream() jetstream.JetStream {
	return n.js
}

func (n *NatsBus) Close() {
	n.closeOnce.Do(func() { close(n.closed) })
	if n.conn != nil {
		// Drain ensures all buffered messages are sent before closing.
		_ = n.conn.Drain()
		n.conn.Close()
	}
}

// natsSubscription implements the Subscription interface (Wrapper)
type natsSubscription struct {
	cc         jetstream.ConsumeContext
	cancel     context.CancelFunc
	js         jetstream.JetStream
	streamName string
	consName   string
	isDurable  bool
	// opTimeout bounds the consumer delete on Unsubscribe; zero takes
	// defaultUnsubscribeTimeout.
	opTimeout time.Duration
}

// defaultUnsubscribeTimeout bounds the ephemeral consumer delete of an
// Unsubscribe when the bus has no operation timeout.
const defaultUnsubscribeTimeout = 2 * time.Second

func (s *natsSubscription) Unsubscribe() error {
	s.cc.Stop()
	s.cancel()

	// Best effort: delete the ephemeral consumer to avoid overlap with new subscriptions
	if !s.isDurable && s.js != nil && s.streamName != "" && s.consName != "" {
		slog.Debug("NATS Bus Unsubscribe: Deleting ephemeral consumer", "stream", s.streamName, "consumer", s.consName)
		timeout := s.opTimeout
		if timeout <= 0 {
			timeout = defaultUnsubscribeTimeout
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if err := s.js.DeleteConsumer(ctx, s.streamName, s.consName); err != nil {
			// Log as warning rather than error as it might be already gone (race condition with InactiveThreshold)
			slog.Warn("NATS Bus Unsubscribe: Failed to delete ephemeral consumer", "stream", s.streamName, "consumer", s.consName, "error", err)
		}
	}
	return nil
}

// KV returns the KeyValue interface for distributed state management.
func (n *NatsBus) KV() KeyValue {
	return &natsKV{
		js:  n.js,
		bus: n,
	}
}
