// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package bus

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/snake"
)

// newDLQFailingStream starts an embedded server with a business stream but no
// dead letter stream, and a bus with the DLQ enabled: every dead letter write
// fails.
func newDLQFailingStream(t *testing.T) (*NatsBus, jetstream.JetStream, *liveConsumer) {
	t.Helper()
	s, err := snake.NewServer(context.Background(), snake.Config{
		Port:        -1,
		ClusterName: t.Name(),
		StoreDir:    t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(s.Shutdown)

	nb := NewNatsBus("flux")
	require.NoError(t, nb.Connect(s.ClientURL(), ConnectOptions{
		Name:           t.Name(),
		ConnectTimeout: 5 * time.Second,
		ReconnectWait:  1 * time.Second,
		// Long enough that only an explicit Nak redelivers inside the
		// test: an AckWait expiry would redeliver too, and hide whether
		// the poison message was Nak'd or Term'd.
		AckWait:    10 * time.Second,
		DLQEnabled: true,
		DLQSubject: "flux.dlq",
	}))
	t.Cleanup(nb.Close)

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	_, err = js.CreateStream(context.Background(), jetstream.StreamConfig{
		Name:     "flux",
		Subjects: []string{"flux.msg.>"},
	})
	require.NoError(t, err)
	return nb, js, nil
}

// A poison message whose dead letter write fails must be Nak'd for
// redelivery, not Term'd with only a log: the failure is counted
// (DLQFailures) and the message comes back instead of vanishing.
func TestNatsBus_DLQFailureNaksInsteadOfDropping(t *testing.T) {
	nb, js, _ := newDLQFailingStream(t)

	sub, err := nb.Subscribe("flux.msg.poison", func(_ context.Context, _ *fluxmsg.FluxMsg) error {
		t.Error("handler must not run for an undecodable payload")
		return nil
	})
	require.NoError(t, err)
	defer func() { _ = sub.Unsubscribe() }()

	garbage := []byte{0xff, 0xff, 0xff, 0xff}
	require.NoError(t, nb.PublishRaw(context.Background(), "flux.msg.poison", garbage, uuid.New()))

	live := sub.(*liveConsumer)
	consumerName := func() string {
		live.mu.Lock()
		defer live.mu.Unlock()
		return live.name
	}
	redelivered := func() bool {
		cons, err := js.Consumer(context.Background(), "flux", consumerName())
		if err != nil {
			return false
		}
		return cons.CachedInfo().NumRedelivered >= 1
	}
	require.Eventually(t, redelivered, 15*time.Second, 100*time.Millisecond,
		"a poison message whose dead letter write failed must be redelivered, not dropped")
	require.GreaterOrEqual(t, nb.DLQFailures(), uint64(1), "every failed dead letter write is counted")
}
