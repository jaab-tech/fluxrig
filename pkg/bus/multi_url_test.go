// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package bus

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/internal/natstest"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/snake"
)

// A Rack passes snake.url straight to NatsBus.Connect, which passes it
// straight to the NATS client. These tests prove that nothing in fluxrig needs
// to change for a Rack to know several NATS servers, against a real cluster of
// stock servers rather than from reading the client's source.

// startBusCluster brings up three stock NATS servers with the message stream
// in three replicas, so it survives losing one, provisioned the way a Mixer in
// external mode does it.
func startBusCluster(t *testing.T) ([]*natsserver.Server, []string) {
	t.Helper()
	servers := natstest.Cluster(t, 3)
	_, err := snake.NewExternal(context.Background(), snake.Config{
		StreamName: "flux-msg", StreamSubjects: []string{"flux.msg.>"},
		Replicas: 3, FormTimeout: 60 * time.Second,
	}, natstest.URLs(servers))
	require.NoError(t, err)
	urls := make([]string, len(servers))
	for i, srv := range servers {
		urls[i] = srv.ClientURL()
	}
	return servers, urls
}

func testFluxMsg() *fluxmsg.FluxMsg {
	msg := fluxmsg.New()
	msg.Data = map[string]any{"v": "hello"}
	return msg
}

// A Rack's snake.url is a comma-separated list the NATS client splits and
// dials. Naming any one server of the cluster is enough: the client
// discovers the others from that member's own gossip (its CLUSTER_SHARD
// INFO), so even a single-URL config reaches the whole cluster once
// connected.
func TestRackBus_SingleURLDiscoversClusterPeers(t *testing.T) {
	_, urls := startBusCluster(t)

	nb := NewNatsBus("flux-msg")
	require.NoError(t, nb.Connect(urls[0], ConnectOptions{
		Name: "rack-test", ConnectTimeout: 5 * time.Second, ReconnectWait: 100 * time.Millisecond,
	}))
	defer nb.Close()

	nc, ok := nb.Core().(interface{ DiscoveredServers() []string })
	require.True(t, ok, "NatsBus.Core() must expose the underlying *nats.Conn")

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if len(nc.DiscoveredServers()) >= 2 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	assert.GreaterOrEqual(t, len(nc.DiscoveredServers()), 2,
		"the other two cluster members should be discovered from the one explicitly named")
}

// Killing the server a Rack is connected to must not lose the connection:
// the NATS client reconnects to another member of the pool (explicit or
// discovered) on its own, and a publish issued after the kill still
// succeeds once reconnection completes. No fluxrig code participates in
// this; it is the client's own MaxReconnects(-1) plus its server pool.
func TestRackBus_FailsOverWhenItsServerDies(t *testing.T) {
	servers, urls := startBusCluster(t)

	// Only the first two URLs are named explicitly. The Rack must still
	// survive losing whichever one it is actually connected to — including
	// falling back to the third, undiscovered-by-name member if gossip
	// delivered it before the kill.
	multiURL := urls[0] + "," + urls[1]

	nb := NewNatsBus("flux-msg")
	require.NoError(t, nb.Connect(multiURL, ConnectOptions{
		Name: "rack-test", ConnectTimeout: 5 * time.Second, ReconnectWait: 100 * time.Millisecond,
		InitialRetryAttempts: 20, InitialRetryWait: 100 * time.Millisecond,
	}))
	defer nb.Close()

	nc, ok := nb.Core().(interface{ ConnectedUrl() string })
	require.True(t, ok)
	connected := nc.ConnectedUrl()

	var victim *natsserver.Server
	for i, u := range urls {
		if "nats://"+u[len("nats://"):] == connected || u == connected {
			victim = servers[i]
			break
		}
	}
	require.NotNil(t, victim, "must identify which server the client actually picked (connected=%s urls=%v)", connected, urls)
	victim.Shutdown()

	// A publish right after the kill may fail while the client reconnects;
	// retry to a deadline. The claim is that it recovers, not that it never
	// blinks.
	ctx := context.Background()
	fid, err := uuid.NewV7()
	require.NoError(t, err)
	msg := testFluxMsg()
	msg.FluxID = fid

	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		lastErr = nb.Publish(context.Background(), "flux.msg.rack-test.out", msg)
		if lastErr == nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	require.NoError(t, lastErr, "the bus must become writable again after its server dies, with %d of 3 members still up", len(urls)-1)
	_ = ctx
}

// A URL naming no live server at all must still fail, with the normal
// errNoBus-shaped error from the existing single-URL path: phase 2 adds no
// new failure mode, it only widens url to carry more than one address.
func TestRackBus_AllServersDownStillErrors(t *testing.T) {
	nb := NewNatsBus("flux-msg")
	p1, p2 := natstest.FreePort(t), natstest.FreePort(t)
	url := fmt.Sprintf("nats://127.0.0.1:%d,nats://127.0.0.1:%d", p1, p2)
	err := nb.Connect(url, ConnectOptions{
		Name: "rack-test", ConnectTimeout: 300 * time.Millisecond,
		InitialRetryAttempts: 2, InitialRetryWait: 50 * time.Millisecond,
	})
	require.Error(t, err, "no server in the list is listening")
}
