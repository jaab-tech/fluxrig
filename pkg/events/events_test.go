// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/snake"
)

// fakeProvider records what a Publisher sends. A non-nil gate holds each publish.
type fakeProvider struct {
	mu   sync.Mutex
	got  []ConnEvent
	ids  []uuid.UUID
	subs []string
	gate chan struct{}
	err  error
}

func (f *fakeProvider) RackName() string         { return "rack1" }
func (f *fakeProvider) GearRack(g string) string { return "rack1" }
func (f *fakeProvider) PublishEvent(_ context.Context, subject string, data []byte, id uuid.UUID) error {
	if f.gate != nil {
		<-f.gate
	}
	var ev ConnEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, ev)
	f.ids = append(f.ids, id)
	f.subs = append(f.subs, subject)
	return f.err
}

func (f *fakeProvider) events() []ConnEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ConnEvent(nil), f.got...)
}

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

func TestPublisher_SendsInOrderWithItsIdentity(t *testing.T) {
	f := &fakeProvider{}
	p := NewPublisher(f, "server_io", 8, discard())
	p.Publish(ConnEvent{Event: Opened, ConnID: "c1", PeerIP: "10.0.0.1", PeerPort: "4000"})
	p.Publish(ConnEvent{Event: Closed, ConnID: "c1", Reason: ReasonPeerClosed})
	require.NoError(t, p.Close(context.Background()))

	got := f.events()
	require.Len(t, got, 2)
	assert.Equal(t, Opened, got[0].Event)
	assert.Equal(t, Closed, got[1].Event)
	assert.Equal(t, ReasonPeerClosed, got[1].Reason)
	assert.Equal(t, "server_io", got[0].Gear)
	assert.Equal(t, "rack1", got[0].Rack)
	assert.False(t, got[0].TS.IsZero())
	assert.Equal(t, "flux.evt.conn.rack1.server_io", f.subs[0])
	assert.NotEqual(t, f.ids[0], f.ids[1], "two events of one connection must not share a dedup id")
}

// Publish runs on the handler of a socket. The bus here never answers, so if
// Publish waited for it, this test would not finish.
func TestPublisher_FullQueueDoesNotHoldTheCaller(t *testing.T) {
	f := &fakeProvider{gate: make(chan struct{})}
	p := NewPublisher(f, "server_io", 2, discard())

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 100 {
			p.Publish(ConnEvent{Event: Opened, ConnID: "c"})
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish waited for the bus")
	}

	close(f.gate)
	require.NoError(t, p.Close(context.Background()))
	// One in the publisher's hand, and the two the queue holds.
	assert.LessOrEqual(t, len(f.events()), 3)
}

func TestPublisher_CloseIsBoundedAndSafeTwice(t *testing.T) {
	f := &fakeProvider{gate: make(chan struct{})}
	p := NewPublisher(f, "server_io", 2, discard())
	p.Publish(ConnEvent{Event: Opened, ConnID: "c"})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	require.Error(t, p.Close(ctx), "Close must end with its context while the bus does not answer")

	close(f.gate)
	require.NoError(t, p.Close(context.Background()))
}

func TestID_IsStableForOneEvent(t *testing.T) {
	a := ConnEvent{Event: Opened, ConnID: "c1", TS: time.Now()}
	b := ConnEvent{Event: Opened, ConnID: "c1", TS: time.Now().Add(time.Hour)}
	assert.Equal(t, a.id(), b.id(), "a retry of a publish must be recognised as the same event")
}

func TestDurableName_HasOnlyLegalCharacters(t *testing.T) {
	assert.Equal(t, "rack-1_on_connect_server_io", DurableName("rack-1", "on_connect", "server_io"))
	assert.Equal(t, "rack_1_a_b_c", DurableName("rack.1", "a b", "c"))
}

// eventStream starts an embedded server with the stream of durable events.
func eventStream(t *testing.T) (*nats.Conn, jetstream.JetStream) {
	t.Helper()
	s, err := snake.NewServer(context.Background(), snake.Config{Port: -1, ClusterName: t.Name(), StoreDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(s.Shutdown)
	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	_, err = js.CreateStream(context.Background(), jetstream.StreamConfig{Name: StreamName, Subjects: []string{SubjectRoot + ".>"}})
	require.NoError(t, err)
	return nc, js
}

func publish(t *testing.T, js jetstream.JetStream, subject string, ev ConnEvent) {
	t.Helper()
	data, err := json.Marshal(ev)
	require.NoError(t, err)
	_, err = js.Publish(context.Background(), subject, data)
	require.NoError(t, err)
}

func fresh(event, connID string) ConnEvent {
	return ConnEvent{Event: event, ConnID: connID, TS: time.Now().UTC()}
}

func openReader(t *testing.T, nc *nats.Conn, cfg ReaderConfig) *Reader {
	t.Helper()
	cfg.Conn = func() *nats.Conn { return nc }
	cfg.Logger = discard()
	r := NewReader(cfg)
	require.NoError(t, r.Open(context.Background()))
	t.Cleanup(r.Close)
	return r
}

func next(t *testing.T, r *Reader) *Delivery {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d, err := r.Next(ctx)
	require.NoError(t, err)
	return d
}

// quiet asserts that nothing arrives for wait.
func quiet(t *testing.T, r *Reader, wait time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	d, err := r.Next(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected nothing for %s, got %+v, err %v", wait, d, err)
	}
}

func TestReader_TwoReadersEachGetEveryEvent(t *testing.T) {
	nc, js := eventStream(t)
	subject := ConnSubject("rack1", "server_io")
	a := openReader(t, nc, ReaderConfig{Subject: subject, Durable: "a"})
	b := openReader(t, nc, ReaderConfig{Subject: subject, Durable: "b"})

	publish(t, js, subject, fresh(Opened, "c1"))
	publish(t, js, subject, fresh(Opened, "c2"))

	for _, r := range []*Reader{a, b} {
		for _, want := range []string{"c1", "c2"} {
			d := next(t, r)
			assert.Equal(t, want, d.Event.ConnID)
			require.NoError(t, d.Ack(nil))
		}
	}
}

// The reason the Rack is in the subject. Without it, the readers of a gear that
// runs on two Racks would share one consumer, and an event could reach the Rack
// that does not hold the connection.
func TestReader_SeesOnlyTheRackItAsksFor(t *testing.T) {
	nc, js := eventStream(t)
	one := ConnSubject("rack1", "server_io")
	two := ConnSubject("rack2", "server_io")
	r1 := openReader(t, nc, ReaderConfig{Subject: one, Durable: DurableName("rack1", "on_connect", "server_io")})
	r2 := openReader(t, nc, ReaderConfig{Subject: two, Durable: DurableName("rack2", "on_connect", "server_io")})

	publish(t, js, one, fresh(Opened, "on-rack1"))
	publish(t, js, two, fresh(Opened, "on-rack2"))

	d1 := next(t, r1)
	assert.Equal(t, "on-rack1", d1.Event.ConnID)
	require.NoError(t, d1.Ack(nil))
	d2 := next(t, r2)
	assert.Equal(t, "on-rack2", d2.Event.ConnID)
	require.NoError(t, d2.Ack(nil))
}

func TestReader_GetsWhatItMissedWhileDown(t *testing.T) {
	nc, js := eventStream(t)
	subject := ConnSubject("rack1", "server_io")
	first := openReader(t, nc, ReaderConfig{Subject: subject, Durable: "reader"})
	first.Close()

	publish(t, js, subject, fresh(Opened, "while-down"))

	second := openReader(t, nc, ReaderConfig{Subject: subject, Durable: "reader"})
	d := next(t, second)
	assert.Equal(t, "while-down", d.Event.ConnID)
	require.NoError(t, d.Ack(nil))
}

func TestReader_DropsAnEventThatIsTooOld(t *testing.T) {
	nc, js := eventStream(t)
	subject := ConnSubject("rack1", "server_io")
	r := openReader(t, nc, ReaderConfig{Subject: subject, Durable: "reader", MaxEventAge: time.Minute})

	old := fresh(Opened, "old")
	old.TS = time.Now().Add(-2 * time.Minute)
	publish(t, js, subject, old)
	publish(t, js, subject, fresh(Opened, "new"))

	d := next(t, r)
	assert.Equal(t, "new", d.Event.ConnID, "the reader must not return the old event")
	require.NoError(t, d.Ack(nil))
}

func TestReader_OnSelectsTheEvents(t *testing.T) {
	nc, js := eventStream(t)
	subject := ConnSubject("rack1", "server_io")
	r := openReader(t, nc, ReaderConfig{Subject: subject, Durable: "reader", On: []string{Opened}})

	publish(t, js, subject, fresh(Closed, "c1"))
	publish(t, js, subject, fresh(Opened, "c2"))

	d := next(t, r)
	assert.Equal(t, "c2", d.Event.ConnID)
	require.NoError(t, d.Ack(nil))
}

func TestReader_FailureIsDeliveredAgainUpToMaxDeliver(t *testing.T) {
	nc, js := eventStream(t)
	subject := ConnSubject("rack1", "server_io")
	r := openReader(t, nc, ReaderConfig{Subject: subject, Durable: "reader", MaxDeliver: 2})

	publish(t, js, subject, fresh(Opened, "c1"))

	first := next(t, r)
	require.NoError(t, first.Ack(errors.New("pipeline failed")))
	second := next(t, r)
	assert.Equal(t, "c1", second.Event.ConnID)
	require.NoError(t, second.Ack(errors.New("pipeline failed")))

	quiet(t, r, 2*redeliverDelay)
}

func TestReader_SkipsAnEventThatDoesNotDecode(t *testing.T) {
	nc, js := eventStream(t)
	subject := ConnSubject("rack1", "server_io")
	r := openReader(t, nc, ReaderConfig{Subject: subject, Durable: "reader"})

	_, err := js.Publish(context.Background(), subject, []byte("not json"))
	require.NoError(t, err)
	publish(t, js, subject, fresh(Opened, "c1"))

	d := next(t, r)
	assert.Equal(t, "c1", d.Event.ConnID)
	require.NoError(t, d.Ack(nil))
}

func TestReader_ReportsADeletedConsumerAndReopens(t *testing.T) {
	nc, js := eventStream(t)
	subject := ConnSubject("rack1", "server_io")
	r := openReader(t, nc, ReaderConfig{Subject: subject, Durable: "reader"})

	require.NoError(t, js.DeleteConsumer(context.Background(), StreamName, "reader"))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := r.Next(ctx)
	require.ErrorIs(t, err, ErrNotConnected)

	require.NoError(t, r.Open(context.Background()))
	publish(t, js, subject, fresh(Opened, "c1"))
	d := next(t, r)
	assert.Equal(t, "c1", d.Event.ConnID)
	require.NoError(t, d.Ack(nil))
}

func TestReader_ReportsAReplacedConnection(t *testing.T) {
	nc, js := eventStream(t)
	subject := ConnSubject("rack1", "server_io")
	var mu sync.Mutex
	current := nc
	r := NewReader(ReaderConfig{Subject: subject, Durable: "reader", Logger: discard(), Conn: func() *nats.Conn {
		mu.Lock()
		defer mu.Unlock()
		return current
	}})
	require.NoError(t, r.Open(context.Background()))
	t.Cleanup(r.Close)

	replacement, err := nats.Connect(nc.ConnectedUrl())
	require.NoError(t, err)
	t.Cleanup(replacement.Close)
	mu.Lock()
	current = replacement
	mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = r.Next(ctx)
	require.ErrorIs(t, err, ErrNotConnected)

	require.NoError(t, r.Open(context.Background()))
	publish(t, js, subject, fresh(Opened, "c1"))
	d := next(t, r)
	assert.Equal(t, "c1", d.Event.ConnID)
	require.NoError(t, d.Ack(nil))
}

func TestReader_WithoutAConnectionReportsIt(t *testing.T) {
	r := NewReader(ReaderConfig{Conn: func() *nats.Conn { return nil }, Logger: discard()})
	require.ErrorIs(t, r.Open(context.Background()), ErrNotConnected)
	_, err := r.Next(context.Background())
	require.ErrorIs(t, err, ErrNotConnected)
}
