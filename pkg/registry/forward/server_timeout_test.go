// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package forward

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/registry"
)

// blockingRegistry hangs every write until the context ends, as a holder
// whose store stalls does: the context is the only way out.
type blockingRegistry struct {
	registry.Registry
	sawCancel chan error
}

func (b *blockingRegistry) Approve(ctx context.Context, id uuid.UUID, newName string) (*registry.Rack, error) {
	<-ctx.Done()
	b.sawCancel <- ctx.Err()
	return nil, ctx.Err()
}

// A write must not outlive the caller: the holder applies the smaller of the
// caller's deadline and its own, so a caller that already gave up (the
// forwarder default is 5s) never gets the write applied anyway.
func TestServer_HonorsTheCallerDeadline(t *testing.T) {
	reg := &blockingRegistry{sawCancel: make(chan error, 1)}
	srv := NewServer(reg, nil, nil, slog.Default())

	req, err := json.Marshal(Request{Op: OpApprove, ID: uuid.New(), NewName: "x", TTLNanos: (200 * time.Millisecond).Nanoseconds()})
	require.NoError(t, err)

	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		// No reply subject: Respond fails silently and handle still runs the
		// write under the deadline, which is what this asserts.
		srv.handle(&nats.Msg{Subject: SubjectPrefix + OpApprove, Data: req})
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("handle did not return: the caller's deadline was ignored")
	}
	assert.Less(t, time.Since(start), 5*time.Second, "the holder must stop at the caller's deadline, not its own 10s")
	select {
	case err := <-reg.sawCancel:
		assert.Error(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("the blocked write never saw the deadline")
	}
}

// Without a caller TTL the holder falls back to its own bound.
func TestServer_DefaultsWithoutACallerDeadline(t *testing.T) {
	reg := &blockingRegistry{sawCancel: make(chan error, 1)}
	srv := NewServer(reg, nil, nil, slog.Default())

	req, err := json.Marshal(Request{Op: OpApprove, ID: uuid.New(), NewName: "x"})
	require.NoError(t, err)

	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.handle(&nats.Msg{Subject: SubjectPrefix + OpApprove, Data: req})
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("handle did not return under its own bound")
	}
}
