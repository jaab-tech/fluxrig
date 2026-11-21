// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// muteServer speaks just enough NATS to complete the handshake, then never
// answers: JetStream API requests stall until the caller's context ends, the
// way they do on a dead link that holds TCP. A test that only shuts a real
// server down cannot stall Open: the client notices the closed connection
// and Open returns ErrNotConnected before any network wait.
func muteServer(t *testing.T) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	done := make(chan struct{})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				_, _ = fmt.Fprintf(c, "INFO {\"server_id\":\"mute\",\"version\":\"2.11.0\",\"proto\":1,\"go\":\"go1.24\",\"host\":\"127.0.0.1\",\"headers\":true,\"max_payload\":1048576,\"jetstream\":true}\r\n")
				rd := bufio.NewReader(c)
				for {
					line, err := rd.ReadString('\n')
					if err != nil {
						return
					}
					if strings.HasPrefix(line, "PING") {
						_, _ = fmt.Fprintf(c, "PONG\r\n")
					}
					select {
					case <-done:
						return
					default:
					}
				}
			}()
		}
	}()
	return ln.Addr().String(), func() { close(done); _ = ln.Close() }
}

func muteConn(t *testing.T, addr string) *nats.Conn {
	t.Helper()
	nc, err := nats.Connect(addr, nats.Timeout(5*time.Second))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	require.True(t, nc.IsConnected())
	return nc
}

// An Open against an unreachable cluster must end with OpenTimeout, not with
// the caller's context: readers open with Background, which never ends.
func TestReader_OpenIsBoundedByOpenTimeout(t *testing.T) {
	addr, stop := muteServer(t)
	defer stop()
	nc := muteConn(t, addr)
	r := NewReader(ReaderConfig{
		Conn:        func() *nats.Conn { return nc },
		Subject:     ConnSubject("rack1", "server_io"),
		Durable:     "reader",
		OpenTimeout: 500 * time.Millisecond,
		Logger:      discard(),
	})

	donec := make(chan error, 1)
	go func() { donec <- r.Open(context.Background()) }()
	select {
	case err := <-donec:
		require.Error(t, err, "Open against a mute server must fail, not hang")
	// The client library gives up an API request after its own 5s default,
	// so only a bound well below that proves OpenTimeout did it.
	case <-time.After(3 * time.Second):
		t.Fatal("Open did not return within OpenTimeout: it waits past it")
	}
}

// A Close racing a stalled Open must not block on it: Open runs the consumer
// create without holding the lock, and drops its consumer when Close ran
// meanwhile.
func TestReader_CloseDoesNotWaitForAStalledOpen(t *testing.T) {
	addr, stop := muteServer(t)
	defer stop()
	nc := muteConn(t, addr)
	r := NewReader(ReaderConfig{
		Conn:        func() *nats.Conn { return nc },
		Subject:     ConnSubject("rack1", "server_io"),
		Durable:     "reader",
		OpenTimeout: 10 * time.Second,
		Logger:      discard(),
	})

	donec := make(chan error, 1)
	go func() { donec <- r.Open(context.Background()) }()
	time.Sleep(200 * time.Millisecond) // let Open reach the stalled create

	start := time.Now()
	r.Close()
	// A stalled Open holds its answer for the client's 5s API default, so a
	// Close that waits on it takes seconds; without the lock it is instant.
	require.Less(t, time.Since(start), 3*time.Second, "Close must not wait for the stalled Open")

	select {
	case err := <-donec:
		require.Error(t, err, "an Open raced by Close must fail, not open behind it")
	case <-time.After(15 * time.Second):
		t.Fatal("Open did not return after Close")
	}
}

// Two Opens at once install one consumer and both report success.
func TestReader_ConcurrentOpensInstallOneConsumer(t *testing.T) {
	nc, js := eventStream(t)
	subject := ConnSubject("rack1", "server_io")
	r := NewReader(ReaderConfig{Subject: subject, Durable: "reader", Logger: discard(), Conn: func() *nats.Conn { return nc }})
	t.Cleanup(r.Close)

	errc := make(chan error, 2)
	for range 2 {
		go func() { errc <- r.Open(context.Background()) }()
	}
	for range 2 {
		require.NoError(t, <-errc)
	}
	publish(t, js, subject, fresh(Opened, "c1"))
	d := next(t, r)
	require.Equal(t, "c1", d.Event.ConnID)
	require.NoError(t, d.Ack(nil))
}
