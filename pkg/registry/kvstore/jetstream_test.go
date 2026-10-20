// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package kvstore

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/registry"
)

func startJetStream(t *testing.T) jetstream.JetStream {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Port: -1, JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true,
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(10*time.Second), "embedded server did not start")
	t.Cleanup(srv.Shutdown)

	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	return js
}

// The JetStream adapter has the semantics the store relies on and the
// memory bucket imitates: create on a present key conflicts, an update or
// delete on a moved revision conflicts, keys filter by prefix.
func TestJetStreamBucket_Semantics(t *testing.T) {
	ctx := context.Background()
	reg, live, err := EnsureBuckets(ctx, startJetStream(t), BucketConfig{
		Registry: "reg_test", Liveness: "live_test", MaxValueSize: 256, LivenessTTL: time.Second,
	})
	require.NoError(t, err)

	rev, err := reg.Create(ctx, "entity.4.a", []byte("1"))
	require.NoError(t, err)
	_, err = reg.Create(ctx, "entity.4.a", []byte("2"))
	require.ErrorIs(t, err, errConflict)

	_, err = reg.Update(ctx, "entity.4.a", []byte("2"), rev+100)
	require.ErrorIs(t, err, errConflict)
	rev2, err := reg.Update(ctx, "entity.4.a", []byte("2"), rev)
	require.NoError(t, err)

	require.ErrorIs(t, reg.Delete(ctx, "entity.4.a", rev), errConflict)
	_, _, err = reg.Get(ctx, "missing")
	require.ErrorIs(t, err, errKeyNotFound)

	_, err = reg.Put(ctx, "name.x", []byte("n"))
	require.NoError(t, err)
	keys, err := reg.Keys(ctx, "entity.4.")
	require.NoError(t, err)
	assert.Equal(t, []string{"entity.4.a"}, keys)
	none, err := reg.Keys(ctx, "scenario.")
	require.NoError(t, err)
	assert.Empty(t, none)

	require.NoError(t, reg.Delete(ctx, "entity.4.a", rev2))
	_, err = reg.Create(ctx, "entity.4.a", []byte("3"))
	require.NoError(t, err, "a deleted key can be created again")

	_, err = reg.Put(ctx, "big", make([]byte, 257))
	require.Error(t, err, "the registry bucket enforces its max value size")

	_, err = live.Put(ctx, "entity.4.a", []byte("p"))
	require.NoError(t, err)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, _, errGet := live.Get(ctx, "entity.4.a"); errGet != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	_, _, err = live.Get(ctx, "entity.4.a")
	require.ErrorIs(t, err, errKeyNotFound, "the liveness bucket expires a key by its max age")
}

// The whole store over real buckets: register, approve, heartbeat, list.
func TestStore_OverJetStream(t *testing.T) {
	ctx := context.Background()
	reg, live, err := EnsureBuckets(ctx, startJetStream(t), BucketConfig{
		Registry: "fluxrig_registry", Liveness: "fluxrig_liveness", MaxValueSize: 1 << 20, LivenessTTL: time.Minute,
	})
	require.NoError(t, err)
	s := New(slog.Default(), reg, live, nil, uuid.New(), Config{MaxValueSize: 1 << 20, WriteRetries: 3})
	s.SetBootstrapSecret(boot)

	r, err := s.Register(ctx, uuid.New(), "rack a", boot, "ip", 1, "v1", nil, uuid.New())
	require.NoError(t, err)
	_, err = s.Register(ctx, uuid.New(), "rack a", boot, "ip", 1, "v1", nil, uuid.New())
	require.ErrorIs(t, err, registry.ErrNameConflict)

	_, err = s.Approve(ctx, r.MachineID, "rack b")
	require.NoError(t, err)
	require.NoError(t, s.Heartbeat(ctx, r.MachineID, nil, nil))
	racks, err := s.List(ctx, "active")
	require.NoError(t, err)
	require.Len(t, racks, 1)
	assert.Equal(t, "rack b", racks[0].Name)
	assert.True(t, racks[0].Online)
}
