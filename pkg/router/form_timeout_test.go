// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/stretchr/testify/require"
)

// Stream creation keeps retrying for the whole form timeout. The router used
// to bound it with a fixed 10s context, which the first 10s attempt used up,
// so three Mixers starting together all exited on "failed to create/update
// stream flux-telemetry: context deadline exceeded" while the cluster was
// still forming. Here JetStream only becomes reachable after 11s.
func TestConfigureJetStream_RetriesForTheFormTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for JetStream to appear after 11s")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())
	start := func(jetStream bool) *natsserver.Server {
		opts := &natsserver.Options{Host: "127.0.0.1", Port: port, NoLog: true, NoSigs: true}
		if jetStream {
			opts.JetStream, opts.StoreDir = true, t.TempDir()
		}
		srv, errSrv := natsserver.NewServer(opts)
		require.NoError(t, errSrv)
		go srv.Start()
		require.True(t, srv.ReadyForConnections(10*time.Second), "server did not start")
		return srv
	}

	// A server without JetStream: every stream request finds no responder.
	plain := start(false)
	swapped := make(chan *natsserver.Server, 1)
	go func() {
		time.Sleep(11 * time.Second) // the fault this test replays, not a wait for a condition
		plain.Shutdown()
		plain.WaitForShutdown()
		swapped <- start(true)
	}()
	t.Cleanup(func() {
		select {
		case srv := <-swapped:
			srv.Shutdown()
		default:
			plain.Shutdown()
		}
	})

	r, err := NewRouter(watermill.NopLogger{})
	require.NoError(t, err)
	began := time.Now()
	err = r.ConfigureJetStream(JetStreamSetup{
		URL:               "nats://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		BusinessStream:    "form-msg",
		BusinessSubjects:  []string{"form.msg.>"},
		TelemetryStream:   "form-tel",
		TelemetrySubjects: []string{"form.tel.>"},
		FormTimeout:       40 * time.Second,
	}, watermill.NopLogger{})
	require.NoError(t, err, "stream creation must outlast a 10s wait for JetStream (took %s)", time.Since(began).Round(time.Second))
}
