// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

// Package natstest starts stock NATS servers inside a test process: a cluster
// of them, the way an operator runs one for the external mode.
package natstest

import (
	"fmt"
	"math/rand/v2"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
)

// The ports a test hands to a server it starts later come from below the
// kernel's ephemeral range (32768 and up on Linux). A port taken from that
// range and released is free for anything that binds port 0 or dials out,
// such as a server of a parallel test, before this test's server binds it.
// Each port is handed out once per process, and checked free when chosen.
const (
	freePortLow  = 20000
	freePortHigh = 32768
)

var (
	freePortMu   sync.Mutex
	freePortUsed = map[int]bool{}
)

// FreePort returns a port that is free now and that no other caller in this
// process received.
func FreePort(t testing.TB) int {
	t.Helper()
	freePortMu.Lock()
	defer freePortMu.Unlock()
	for range freePortHigh - freePortLow {
		p := freePortLow + rand.IntN(freePortHigh-freePortLow)
		if freePortUsed[p] {
			continue
		}
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err != nil {
			continue
		}
		_ = l.Close()
		freePortUsed[p] = true
		return p
	}
	t.Fatal("natstest: no free port below the ephemeral range")
	return 0
}

// formTimeout bounds the wait for a new cluster to elect its metadata leader.
const formTimeout = 30 * time.Second

// Cluster starts n clustered NATS servers with JetStream on loopback, and
// returns once every one of them knows the metadata leader. The servers stop
// when the test ends; a test may stop one before, to take it out.
func Cluster(t testing.TB, n int) []*server.Server {
	t.Helper()
	routePorts := make([]int, n)
	for i := range routePorts {
		routePorts[i] = FreePort(t)
	}
	servers := make([]*server.Server, n)
	for i := 0; i < n; i++ {
		opts := &server.Options{
			ServerName: fmt.Sprintf("nats-%d", i+1),
			Host:       "127.0.0.1",
			Port:       FreePort(t),
			JetStream:  true,
			StoreDir:   t.TempDir(),
			NoLog:      true,
			NoSigs:     true,
			Cluster:    server.ClusterOpts{Name: "natstest", Host: "127.0.0.1", Port: routePorts[i]},
		}
		for j, p := range routePorts {
			if j != i {
				opts.Routes = append(opts.Routes, server.RoutesFromStr(fmt.Sprintf("nats://127.0.0.1:%d", p))...)
			}
		}
		srv, err := server.NewServer(opts)
		if err != nil {
			t.Fatalf("natstest: server %d: %v", i+1, err)
		}
		go srv.Start()
		if !srv.ReadyForConnections(10 * time.Second) {
			t.Fatalf("natstest: server %d did not start", i+1)
		}
		t.Cleanup(srv.Shutdown)
		servers[i] = srv
	}
	deadline := time.Now().Add(formTimeout)
	for _, srv := range servers {
		for !srv.JetStreamIsCurrent() {
			if time.Now().After(deadline) {
				t.Fatalf("natstest: the cluster has no metadata leader after %s", formTimeout)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	return servers
}

// URLs returns the client URLs of servers, comma-separated, as snake.url names
// a NATS cluster.
func URLs(servers []*server.Server) string {
	out := ""
	for i, srv := range servers {
		if i > 0 {
			out += ","
		}
		out += srv.ClientURL()
	}
	return out
}
