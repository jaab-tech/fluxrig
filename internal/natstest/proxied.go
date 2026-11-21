// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package natstest

import (
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
)

// RouteProxy forwards one cluster route. Holed, it drops every byte and
// refuses new connections, which is what a firewall DROP rule, or a server
// that died without closing its routes, looks like to the other members:
// the route stays open and goes silent.
type RouteProxy struct {
	ln    net.Listener
	holed atomic.Bool
}

// SetHoled holes the route, or opens it again.
func (p *RouteProxy) SetHoled(holed bool) { p.holed.Store(holed) }

func newRouteProxy(t testing.TB, target string) *RouteProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("natstest: route proxy: %v", err)
	}
	p := &RouteProxy{ln: ln}
	go func() {
		for {
			c, errAcc := ln.Accept()
			if errAcc != nil {
				return
			}
			go p.serve(c, target)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return p
}

func (p *RouteProxy) serve(c net.Conn, target string) {
	if p.holed.Load() {
		_ = c.Close()
		return
	}
	d, err := net.Dial("tcp", target)
	if err != nil {
		_ = c.Close()
		return
	}
	pipe := func(dst, src net.Conn) {
		defer func() { _ = dst.Close() }()
		buf := make([]byte, 32*1024)
		for {
			n, errRead := src.Read(buf)
			if errRead != nil {
				return
			}
			if p.holed.Load() {
				continue
			}
			if _, errWrite := dst.Write(buf[:n]); errWrite != nil {
				return
			}
		}
	}
	go pipe(d, c)
	go pipe(c, d)
}

// ProxiedCluster starts n JetStream servers in one cluster whose routes run
// through RouteProxy, so a test can cut a member off in silence. proxies[i][j]
// carries member i's route to member j; proxies[i][i] is nil.
func ProxiedCluster(t testing.TB, n int) (servers []*server.Server, proxies [][]*RouteProxy) {
	t.Helper()
	routePorts := make([]int, n)
	for i := range routePorts {
		routePorts[i] = FreePort(t)
	}
	proxies = make([][]*RouteProxy, n)
	servers = make([]*server.Server, n)
	for i := 0; i < n; i++ {
		proxies[i] = make([]*RouteProxy, n)
		opts := &server.Options{
			ServerName: fmt.Sprintf("m%d", i+1), Host: "127.0.0.1", Port: -1,
			JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true,
			Cluster: server.ClusterOpts{Name: "proxied", Host: "127.0.0.1", Port: routePorts[i]},
		}
		for j := 0; j < n; j++ {
			if i == j {
				continue
			}
			proxies[i][j] = newRouteProxy(t, fmt.Sprintf("127.0.0.1:%d", routePorts[j]))
			opts.Routes = append(opts.Routes, server.RoutesFromStr("nats://"+proxies[i][j].ln.Addr().String())...)
		}
		srv, err := server.NewServer(opts)
		if err != nil {
			t.Fatalf("natstest: member %d: %v", i+1, err)
		}
		go srv.Start()
		if !srv.ReadyForConnections(10 * time.Second) {
			t.Fatalf("natstest: member %d did not start", i+1)
		}
		t.Cleanup(srv.Shutdown)
		servers[i] = srv
	}
	return servers, proxies
}

// Isolate holes every route to and from member victim, or opens them again.
func Isolate(proxies [][]*RouteProxy, victim int, holed bool) {
	for i := range proxies {
		for j, p := range proxies[i] {
			if p != nil && (i == victim || j == victim) {
				p.SetHoled(holed)
			}
		}
	}
}
