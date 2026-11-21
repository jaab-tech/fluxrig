// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package snake

import (
	"context"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func stockServer(t *testing.T, jetStream bool) string {
	t.Helper()
	opts := &natsserver.Options{Port: -1, Host: "127.0.0.1", NoLog: true, NoSigs: true, JetStream: jetStream, StoreDir: t.TempDir()}
	s, err := natsserver.NewServer(opts)
	require.NoError(t, err)
	go s.Start()
	require.True(t, s.ReadyForConnections(5*time.Second))
	t.Cleanup(s.Shutdown)
	return s.ClientURL()
}

func TestNewExternal_ProvisionsOnAServerItDoesNotRun(t *testing.T) {
	url := stockServer(t, true)
	s, err := NewExternal(context.Background(), Config{StreamName: "flux-msg", StreamSubjects: []string{"flux.msg.>"}}, url)
	require.NoError(t, err)
	assert.True(t, s.External())
	assert.Equal(t, url, s.ClientURL())
	require.NoError(t, s.ProvisionKV(context.Background(), "wasm_catalog"))
	s.Shutdown() // nothing to stop, and the server stays up

	nc, err := nats.Connect(url)
	require.NoError(t, err)
	defer nc.Close()
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	_, err = js.Stream(context.Background(), "flux-msg")
	require.NoError(t, err)
}

func TestNewExternal_RefusesWithTheReason(t *testing.T) {
	short := time.Second
	_, err := NewExternal(context.Background(), Config{FormTimeout: short}, stockServer(t, false))
	require.ErrorContains(t, err, "no JetStream")

	_, err = NewExternal(context.Background(), Config{FormTimeout: short, Replicas: 3}, stockServer(t, true))
	require.ErrorContains(t, err, "not a cluster")

	_, err = NewExternal(context.Background(), Config{FormTimeout: short}, "nats://127.0.0.1:1")
	require.ErrorContains(t, err, "does not answer")
}

func TestVersionAtLeast(t *testing.T) {
	for _, tt := range []struct {
		version, minimum string
		want             bool
	}{
		{"2.15.1", "2.15.0", true},
		{"2.15.0", "2.15.0", true},
		{"2.14.9", "2.15.0", false},
		{"3.0.0", "2.15.0", true},
		{"2.9.25", "2.15.0", false},
		{"2.15.0-RC.1", "2.15.0", true},
		{"v2.15.1", "2.15.0", true},
	} {
		got, err := versionAtLeast(tt.version, tt.minimum)
		require.NoError(t, err, tt.version)
		assert.Equal(t, tt.want, got, tt.version)
	}
	_, err := versionAtLeast("two", "2.15.0")
	require.Error(t, err)
}

// The embedded server runs alone, so more than one copy of a stream needs an
// external NATS cluster.
func TestNewServer_RunsAlone(t *testing.T) {
	_, err := NewServer(context.Background(), Config{Port: -1, StoreDir: t.TempDir(), Replicas: 3})
	require.ErrorContains(t, err, "external NATS")
}
