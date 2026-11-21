// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package snake

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/internal/natstest"
)

// syncBuffer is a bytes.Buffer safe for the server's goroutines to write
// while the test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureSlog sends slog.Default to a buffer for the rest of the test.
func captureSlog(t *testing.T, level slog.Level) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: level})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// The embedded server's own reports reach slog. Without a logger it reports
// nothing, and a client refused for its certificate leaves no trace.
func TestServer_NATSLogReachesSlog(t *testing.T) {
	buf := captureSlog(t, slog.LevelInfo)
	srv, err := NewServer(context.Background(), Config{
		Port:           natstest.FreePort(t),
		StoreDir:       t.TempDir(),
		StreamName:     "flux-msg",
		StreamSubjects: []string{"flux.msg.>"},
	})
	require.NoError(t, err)
	defer srv.Shutdown()

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(buf.String(), "Server is ready") {
		require.True(t, time.Now().Before(deadline), "no NATS notice reached slog; got:\n%s", buf.String())
		time.Sleep(20 * time.Millisecond) // poll interval
	}
	assert.Contains(t, buf.String(), "flux.component=snake.nats")
}

// Each NATS level lands on the slog level an operator filters on, and a
// fatal report is logged without ending the process.
func TestNATSLogger_Levels(t *testing.T) {
	buf := captureSlog(t, slog.LevelDebug)
	l := newNATSLogger()
	l.Noticef("route %s connected", "a")
	l.Warnf("slow consumer %d", 1)
	l.Errorf("TLS route handshake error: %s", "bad certificate")
	l.Fatalf("cannot listen")
	l.Debugf("debug line")
	l.Tracef("trace line")
	out := buf.String()
	assert.Contains(t, out, `level=INFO msg="route a connected"`)
	assert.Contains(t, out, `level=WARN msg="slow consumer 1"`)
	assert.Contains(t, out, `level=ERROR msg="TLS route handshake error: bad certificate"`)
	assert.Contains(t, out, `level=ERROR msg="cannot listen"`)
	assert.Contains(t, out, `level=DEBUG msg="debug line"`)
	assert.Contains(t, out, `level=DEBUG msg="trace line"`)
}
