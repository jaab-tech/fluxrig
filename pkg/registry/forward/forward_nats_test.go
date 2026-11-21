// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package forward

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/manager"
	"github.com/jaab-tech/fluxrig/pkg/registry"
)

// startNATS runs an embedded NATS server for the test and returns its URL.
func startNATS(t *testing.T) string {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{Port: -1, NoLog: true, NoSigs: true})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(10*time.Second), "embedded server did not start")
	t.Cleanup(srv.Shutdown)
	return srv.ClientURL()
}

// mixer is one Mixer of a test cluster: its own connection, its own local
// registry, and its own holder-side server.
type mixer struct {
	reg  *memRegistry
	held *staticHolder
	nc   *nats.Conn
}

// startMixers starts one Server per Mixer over real NATS. holderIndex names
// the Mixer that holds the lease, or -1 for none. A non-holder's local store
// refuses writes the way kvstore's requireLease does.
func startMixers(t *testing.T, url string, n, holderIndex int) []*mixer {
	t.Helper()
	ms := make([]*mixer, n)
	for i := range ms {
		nc, err := nats.Connect(url)
		require.NoError(t, err)
		t.Cleanup(nc.Close)
		m := &mixer{reg: &memRegistry{}, held: &staticHolder{held: i == holderIndex}, nc: nc}
		if !m.held.held {
			m.reg.failWith = fmt.Errorf("kvstore: register: %w", registry.ErrNotLeaseHolder)
		}
		srv := NewServer(m.reg, nil, m.held, slog.Default())
		require.NoError(t, srv.Start(nc))
		t.Cleanup(srv.Stop)
		ms[i] = m
	}
	// Subscriptions are registered on the server asynchronously; a flush on
	// each connection makes them visible before the first request.
	for _, m := range ms {
		require.NoError(t, m.nc.Flush())
	}
	return ms
}

// Every Mixer subscribes to the control subjects. Only the holder may
// answer: request/reply returns the first answer, and a non-holder that
// answered "not the holder" would win the race.
func TestForwardOverNATS_OnlyTheHolderAnswers(t *testing.T) {
	url := startNATS(t)
	ms := startMixers(t, url, 3, 0)
	follower := ms[1]

	fwd := NewForwarder(NewNATSTransport(follower.nc), 2*time.Second, slog.Default())
	r := NewRegistry(follower.reg, fwd, follower.held)

	const writes = 50
	for i := 0; i < writes; i++ {
		_, err := r.Register(context.Background(), uuid.New(),
			fmt.Sprintf("rack-%d", i), "", "10.0.0.1", 0, "test", nil, uuid.Nil)
		require.NoError(t, err, "write %d", i)
	}
	assert.Equal(t, writes, ms[0].reg.registers, "every write ran on the holder")
	assert.Zero(t, ms[1].reg.registers, "nothing ran on the forwarding Mixer")
	assert.Zero(t, ms[2].reg.registers, "nothing ran on the other follower")
}

// With no holder nobody answers, and the caller gets ErrForbiddenNoHolder
// once its timeout ends.
func TestForwardOverNATS_NoHolderIsForbidden(t *testing.T) {
	url := startNATS(t)
	ms := startMixers(t, url, 3, -1)
	follower := ms[1]

	fwd := NewForwarder(NewNATSTransport(follower.nc), 200*time.Millisecond, slog.Default())
	r := NewRegistry(follower.reg, fwd, follower.held)

	_, err := r.Register(context.Background(), uuid.New(),
		"rack", "", "10.0.0.1", 0, "test", nil, uuid.Nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrForbiddenNoHolder), "got %v", err)
}

// The store's lease error keeps its identity across the hop, wrapped or not,
// so the API can map it to 503.
func TestCodeOf_NotLeaseHolder(t *testing.T) {
	assert.Equal(t, CodeNotLeaseHolder, CodeOf(registry.ErrNotLeaseHolder))
	assert.Equal(t, CodeNotLeaseHolder, CodeOf(fmt.Errorf("kvstore: approve: %w", registry.ErrNotLeaseHolder)))
	assert.True(t, errors.Is(ErrorOf(CodeNotLeaseHolder, "x"), ErrForbiddenNoHolder))
}

// A Rack's Hello is CBOR, which decodes nested maps as map[any]any. JSON
// refuses those, so a Hello that reached a Mixer without the lease failed to
// forward ("json: unsupported type: map[interface {}]interface {}") and the
// Rack never registered.
func TestForwardOverNATS_RegisterWithAConfigDecodedFromCBOR(t *testing.T) {
	url := startNATS(t)
	ms := startMixers(t, url, 2, 0)
	follower := ms[1]
	r := NewRegistry(follower.reg, NewForwarder(NewNATSTransport(follower.nc), 2*time.Second, slog.Default()), follower.held)

	config := map[string]any{"rack": map[any]any{"outbox_order": "subject", 3: []any{map[any]any{"k": 1}}}}
	_, err := r.Register(context.Background(), uuid.New(), "rack-cbor", "", "10.0.0.1", 0, "test", config, uuid.Nil)
	require.NoError(t, err)
	assert.Equal(t, 1, ms[0].reg.registers, "the write ran on the holder")
}

// A fresh registration issues the Rack's secret, the one time the plaintext
// leaves the registry. Rack.Secret never serializes, so a registration that a
// Mixer without the lease forwarded came back without it: that Mixer issued a
// Passport with no secret, the Rack kept its bootstrap secret, and every
// heartbeat it sent was rejected.
func TestForwardOverNATS_RegisterReturnsTheIssuedSecret(t *testing.T) {
	url := startNATS(t)
	ms := startMixers(t, url, 2, 0)
	follower := ms[1]
	r := NewRegistry(follower.reg, NewForwarder(NewNATSTransport(follower.nc), 2*time.Second, slog.Default()), follower.held)

	rack, err := r.Register(context.Background(), uuid.Nil, "rack-fresh", "bootstrap", "10.0.0.1", 0, "test", nil, uuid.Nil)
	require.NoError(t, err)
	assert.Equal(t, 1, ms[0].reg.registers, "the write ran on the holder")
	assert.Equal(t, "issued-rack-fresh", rack.Secret, "the secret the holder issued reaches the forwarding Mixer")
}

// fakeSpecs records imports and refuses a second version with other bytes.
type fakeSpecs struct {
	manager.Manager
	imports int
	byTag   map[string]string
}

func (f *fakeSpecs) ImportContent(_ context.Context, content []byte, name, tag string) (string, string, string, error) {
	if f.byTag == nil {
		f.byTag = map[string]string{}
	}
	if prev, ok := f.byTag[name+":"+tag]; ok && prev != string(content) {
		return "", "", "", fmt.Errorf("%w: %s:%s", manager.ErrVersionExists, name, tag)
	}
	f.byTag[name+":"+tag] = string(content)
	f.imports++
	return "hash-" + name, name, tag, nil
}

// A spec import on a Mixer without the lease runs on the holder, and the
// holder's answer, a refusal included, comes back unchanged.
func TestForwardOverNATS_ImportSpec(t *testing.T) {
	url := startNATS(t)
	holderSpecs, followerSpecs := &fakeSpecs{}, &fakeSpecs{}
	var conns []*nats.Conn
	for i, specs := range []*fakeSpecs{holderSpecs, followerSpecs} {
		nc, err := nats.Connect(url)
		require.NoError(t, err)
		t.Cleanup(nc.Close)
		srv := NewServer(&memRegistry{}, nil, &staticHolder{held: i == 0}, slog.Default()).WithSpecs(specs)
		require.NoError(t, srv.Start(nc))
		t.Cleanup(srv.Stop)
		require.NoError(t, nc.Flush())
		conns = append(conns, nc)
	}
	s := NewSpecs(followerSpecs, NewForwarder(NewNATSTransport(conns[1]), 2*time.Second, slog.Default()), &staticHolder{held: false})

	hash, name, tag, err := s.ImportContent(context.Background(), []byte("a"), "visa", "v1.0.0")
	require.NoError(t, err)
	assert.Equal(t, []string{"hash-visa", "visa", "v1.0.0"}, []string{hash, name, tag})
	assert.Equal(t, 1, holderSpecs.imports, "the import ran on the holder")
	assert.Zero(t, followerSpecs.imports, "nothing ran on the forwarding Mixer")

	_, _, _, err = s.ImportContent(context.Background(), []byte("b"), "visa", "v1.0.0")
	require.ErrorIs(t, err, manager.ErrVersionExists)
}

// A control write the holder could not apply because JetStream could not
// answer keeps that meaning across the hop, so the API can answer 503.
func TestCodeOf_Unavailable(t *testing.T) {
	electing := &jetstream.APIError{Code: 503, ErrorCode: 10008, Description: "JetStream system temporarily unavailable"}
	assert.Equal(t, CodeUnavailable, CodeOf(fmt.Errorf("kvstore: put: %w", electing)))
	assert.Equal(t, CodeUnavailable, CodeOf(fmt.Errorf("kvstore: put: %w", jetstream.ErrNoStreamResponse)))
	assert.True(t, errors.Is(ErrorOf(CodeUnavailable, "nats: no response from stream"), ErrUnavailable))
	assert.Equal(t, CodeInternal, CodeOf(errors.New("disk full")))
}
