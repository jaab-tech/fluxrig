// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package bus

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/snake"
)

// closedNatsURL returns a NATS URL on a port where nothing listens.
func closedNatsURL(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())
	return fmt.Sprintf("nats://127.0.0.1:%d", port)
}

// A bound on the retry time ends the wait long before the attempt count would.
func TestConnect_RetryTimeoutBoundsTheWait(t *testing.T) {
	nb := NewNatsBus("retry")
	start := time.Now()

	err := nb.Connect(closedNatsURL(t), ConnectOptions{
		Name:                 "retry-bounded",
		ConnectTimeout:       time.Second,
		InitialRetryWait:     10 * time.Millisecond,
		InitialRetryAttempts: 1000,
		InitialRetryTimeout:  300 * time.Millisecond,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "within 300ms")
	// The unbounded schedule would run for minutes; the bound plus one wait is
	// well under this ceiling even on a loaded machine.
	assert.Less(t, time.Since(start), 5*time.Second)
}

// Without a time bound the attempt count still ends the retry.
func TestConnect_RetryStopsAtAttemptLimit(t *testing.T) {
	nb := NewNatsBus("retry")

	err := nb.Connect(closedNatsURL(t), ConnectOptions{
		Name:                 "retry-attempts",
		ConnectTimeout:       time.Second,
		InitialRetryWait:     time.Millisecond,
		InitialRetryAttempts: 3,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "after 3 attempts")
	assert.NotContains(t, err.Error(), "within")
}

// A caller's shutdown signal must be observed between retry attempts, not
// only after the whole retry budget elapses: before this fix, Ctx did not
// exist and a SIGTERM during a long retry schedule (the ~37s default, or
// longer with a configured InitialRetryTimeout) went unnoticed until then.
func TestConnect_CtxCancelEndsTheWaitBetweenAttempts(t *testing.T) {
	nb := NewNatsBus("retry")
	ctx, cancel := context.WithCancel(context.Background())
	start := time.Now()
	time.AfterFunc(30*time.Millisecond, cancel)

	err := nb.Connect(closedNatsURL(t), ConnectOptions{
		Name:                 "retry-ctx-cancel",
		ConnectTimeout:       time.Second,
		InitialRetryWait:     10 * time.Second, // would otherwise wait far past the test
		InitialRetryAttempts: 1000,
		Ctx:                  ctx,
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Less(t, time.Since(start), 2*time.Second, "the cancellation must end the wait, not the 10s retry schedule")
}

// The retry exists for a server that is not accepting connections yet: one that
// appears while the client is retrying must be reached, bound or no bound.
func TestConnect_RetryReachesAServerThatComesUpLate(t *testing.T) {
	ctx := context.Background()

	// Reserve a port, then bring the server up on it after the first attempts fail.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())

	started := make(chan *snake.Server, 1)
	startErr := make(chan error, 1)
	timer := time.AfterFunc(200*time.Millisecond, func() {
		s, errStart := snake.NewServer(ctx, snake.Config{
			Port:        port,
			ClusterName: "retry-late",
			StoreDir:    t.TempDir(),
		})
		if errStart != nil {
			startErr <- errStart
			return
		}
		started <- s
	})
	defer timer.Stop()

	nb := NewNatsBus("flux")
	err = nb.Connect(fmt.Sprintf("nats://127.0.0.1:%d", port), ConnectOptions{
		Name:                 "retry-late",
		ConnectTimeout:       time.Second,
		InitialRetryWait:     50 * time.Millisecond,
		InitialRetryAttempts: 30,
		InitialRetryTimeout:  20 * time.Second,
	})
	require.NoError(t, err)
	defer nb.Close()

	select {
	case s := <-started:
		defer s.Shutdown()
	case errStart := <-startErr:
		t.Fatalf("server did not start: %v", errStart)
	case <-time.After(10 * time.Second):
		t.Fatal("server never started")
	}
}
