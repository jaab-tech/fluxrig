// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0
//
// logons opens a connection to an ISO 8583 server every -interval for
// -duration, and waits on each for the network management message the
// scenario sends when a connection opens. The message reaches the terminal
// through a durable connection event, so a logon measures the event path:
// the Rack's outbox, the event stream and the reader's consumer.
//
//	go run ./test/ha/mixers/tools/logons -addr 127.0.0.1:8888 -duration 2m
//
// It prints one line per connection that got no logon, then a summary. It
// exits 3 when a connection got no logon within -timeout: an event was lost,
// or took longer than the timeout to arrive.
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"sync"
	"time"
)

type attempt struct {
	at      time.Time
	latency time.Duration
	err     error
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8888", "address of the ISO 8583 server")
	interval := flag.Duration("interval", 500*time.Millisecond, "time between two connections")
	duration := flag.Duration("duration", time.Minute, "how long to keep opening connections")
	timeout := flag.Duration("timeout", 30*time.Second, "how long a connection waits for its logon")
	mti := flag.String("mti", "0800", "the message type of the logon")
	flag.Parse()

	var (
		mu       sync.Mutex
		attempts []attempt
		wg       sync.WaitGroup
	)
	end := time.Now().Add(*duration)
	for t := time.Now(); t.Before(end); t = time.Now() {
		wg.Add(1)
		go func(at time.Time) {
			defer wg.Done()
			lat, err := logon(*addr, *mti, *timeout)
			mu.Lock()
			attempts = append(attempts, attempt{at: at, latency: lat, err: err})
			mu.Unlock()
		}(t)
		time.Sleep(*interval)
	}
	wg.Wait()

	sort.Slice(attempts, func(i, j int) bool { return attempts[i].at.Before(attempts[j].at) })
	var ok []time.Duration
	failed := 0
	for _, a := range attempts {
		if a.err != nil {
			failed++
			fmt.Printf("%s no logon: %v\n", a.at.UTC().Format(time.RFC3339Nano), a.err)
			continue
		}
		ok = append(ok, a.latency)
	}
	sort.Slice(ok, func(i, j int) bool { return ok[i] < ok[j] })
	fmt.Printf("logons %d of %d", len(ok), len(attempts))
	if len(ok) > 0 {
		var sum time.Duration
		for _, l := range ok {
			sum += l
		}
		fmt.Printf(", mean %s, p99 %s, max %s", (sum / time.Duration(len(ok))).Round(time.Millisecond),
			ok[(len(ok)*99)/100].Round(time.Millisecond), ok[len(ok)-1].Round(time.Millisecond))
	}
	fmt.Println()
	if failed > 0 || len(attempts) == 0 {
		os.Exit(3)
	}
}

// logon connects, reads one message framed by a two-byte big-endian length,
// and checks its message type. It returns how long the message took.
func logon(addr, mti string, timeout time.Duration) (time.Duration, error) {
	start := time.Now()
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return 0, err
	}
	defer func() { _ = c.Close() }()
	if err = c.SetReadDeadline(start.Add(timeout)); err != nil {
		return 0, err
	}
	var size [2]byte
	if _, err = io.ReadFull(c, size[:]); err != nil {
		return 0, fmt.Errorf("after %s: %w", time.Since(start).Round(time.Millisecond), err)
	}
	body := make([]byte, binary.BigEndian.Uint16(size[:]))
	if _, err = io.ReadFull(c, body); err != nil {
		return 0, err
	}
	if len(body) < 4 || string(body[:4]) != mti {
		return 0, fmt.Errorf("got message type %q, want %s", body[:min(4, len(body))], mti)
	}
	return time.Since(start), nil
}
