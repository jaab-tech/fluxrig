// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package kvstore

import (
	"context"
	"fmt"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/internal/natstest"
)

// Listing keys through a healthy member must not depend on the member that
// is cut off. The KV key lister creates an ephemeral consumer, which the
// cluster could place on the cut-off member, and then the listing hung until
// its context ended: the Mixer API answered GET /racks with a timeout on and
// off for as long as the partition lasted.
func TestKeys_DuringAPartition(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a three-member cluster")
	}
	const members = 3
	servers, proxies, connect := startProxiedCluster(t, members)

	ctx := context.Background()
	reg, _, _, err := EnsureBuckets(ctx, connect(0), BucketConfig{
		Registry: "reg_part", Liveness: "live_part", Replicas: members, LivenessTTL: time.Minute,
	})
	require.NoError(t, err)
	for k := 0; k < 3; k++ {
		_, err = reg.Put(ctx, fmt.Sprintf("entity.1.%d", k), []byte("x"))
		require.NoError(t, err)
	}

	// Cut off the member that leads the registry stream, and read through
	// another one.
	js0 := connect(0)
	stream, err := js0.Stream(ctx, "KV_reg_part")
	require.NoError(t, err)
	info, err := stream.Info(ctx)
	require.NoError(t, err)
	victim := map[string]int{"m1": 0, "m2": 1, "m3": 2}[info.Cluster.Leader]
	reader := (victim + 1) % members
	jsr := connect(reader)
	kvr, err := jsr.KeyValue(ctx, "reg_part")
	require.NoError(t, err)
	b := NewJetStreamBucket(jsr, kvr)
	natstest.Isolate(proxies, victim, true)

	// The majority elects a new stream leader and a new metadata leader
	// first. Until then nothing in the cluster can answer a read.
	deadline := time.Now().Add(30 * time.Second)
	for {
		c, cancel := context.WithTimeout(ctx, 2*time.Second)
		s, errS := jsr.Stream(c, "KV_reg_part")
		var leader string
		if errS == nil {
			if si, errI := s.Info(c); errI == nil {
				leader = si.Cluster.Leader
			}
		}
		cancel()
		var meta string
		if jsz, errJsz := servers[reader].Jsz(nil); errJsz == nil && jsz.Meta != nil {
			meta = jsz.Meta.Leader
		}
		victimName := fmt.Sprintf("m%d", victim+1)
		if leader != "" && leader != victimName && meta != "" && meta != victimName {
			break
		}
		require.True(t, time.Now().Before(deadline), "the majority did not elect a stream and a metadata leader")
		time.Sleep(200 * time.Millisecond) // poll interval
	}

	for attempt := 0; attempt < 15; attempt++ {
		c, cancel := context.WithTimeout(ctx, 3*time.Second)
		keys, errKeys := b.Keys(c, "entity.1.")
		cancel()
		require.NoError(t, errKeys, "listing %d through a healthy member", attempt)
		require.Len(t, keys, 3, "listing %d", attempt)
	}

	// Heal. The member that rejoins sees no metadata leader for a moment, and
	// in that moment it answers every API request at once with "cluster not
	// available". That answer must not win over the leader's.
	for i := range proxies {
		natstest.Isolate(proxies, i, false)
	}
	healed := time.Now()
	for attempt := 0; time.Since(healed) < 15*time.Second; attempt++ {
		c, cancel := context.WithTimeout(ctx, 3*time.Second)
		keys, errKeys := b.Keys(c, "entity.1.")
		cancel()
		require.NoError(t, errKeys, "listing %d after the heal (%s)", attempt, time.Since(healed).Round(time.Millisecond))
		require.Len(t, keys, 3, "listing %d after the heal", attempt)
		time.Sleep(100 * time.Millisecond) // listing pace, not a wait for a condition
	}
}

// startProxiedCluster starts a JetStream cluster whose routes run through
// natstest.RouteProxy, so a test can cut a member off. proxies[i][j] carries
// member i's route to member j. connect opens a JetStream handle on member i.
func startProxiedCluster(t *testing.T, members int) ([]*natsserver.Server, [][]*natstest.RouteProxy, func(int) jetstream.JetStream) {
	t.Helper()
	servers, proxies := natstest.ProxiedCluster(t, members)
	connect := func(i int) jetstream.JetStream {
		nc, err := nats.Connect(servers[i].ClientURL())
		require.NoError(t, err)
		t.Cleanup(nc.Close)
		js, err := jetstream.New(nc)
		require.NoError(t, err)
		return js
	}
	return servers, proxies, connect
}

// A read through any member sees a write made a moment before through another
// one. A direct get could be answered by a replica that had not received the
// write yet, and a Rack's registration, which writes its record and reads it
// back, then failed: the Rack never came online.
func TestGet_ReadsAWriteFromAnotherMember(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a three-member cluster")
	}
	_, _, connect := startProxiedCluster(t, 3)
	ctx := context.Background()
	writer, _, _, err := EnsureBuckets(ctx, connect(0), BucketConfig{
		Registry: "reg_raw", Liveness: "live_raw", Replicas: 3, LivenessTTL: time.Minute,
	})
	require.NoError(t, err)
	jsr := connect(1)
	kvr, err := jsr.KeyValue(ctx, "reg_raw")
	require.NoError(t, err)
	reader := NewJetStreamBucket(jsr, kvr)

	for i := 0; i < 200; i++ {
		key := fmt.Sprintf("entity.1.%d", i)
		rev, err := writer.Put(ctx, key, []byte("x"))
		require.NoError(t, err)
		_, got, err := reader.Get(ctx, key)
		require.NoError(t, err, "read %d right after its write", i)
		require.Equal(t, rev, got, "read %d returns the revision just written", i)
	}
}
