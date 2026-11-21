// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0
//
// leaseholder prints the mixer_id currently holding the write lease, read
// from the fluxrig_lease KV bucket on the NATS cluster. It is how run.sh
// finds the holder: the Mixer logs go through a switched handler at runtime,
// so the acquire line is not reliable, and the KV is the authority.
//
//	go run ./test/ha/mixers/tools/leaseholder -url nats://127.0.0.1:4223
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/jaab-tech/fluxrig/test/ha/mixers/tools/natsconn"
)

func main() {
	conn := natsconn.Flags()
	bucket := flag.String("bucket", "fluxrig_lease", "lease bucket name")
	flag.Parse()

	nc, err := conn.Connect()
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		os.Exit(2)
	}
	defer nc.Close()

	js, err := jetstream.New(nc)
	if err != nil {
		fmt.Fprintln(os.Stderr, "jetstream:", err)
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	kv, err := js.KeyValue(ctx, *bucket)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bucket:", err)
		os.Exit(3)
	}
	entry, err := kv.Get(ctx, "holder")
	if err != nil {
		// No holder is a valid outcome after a fault, not an error.
		fmt.Print("")
		return
	}
	// The value is {"mixer_id":"...","acquired":"..."}; print just the id so
	// the caller can compare it with a Mixer's own machine_id.
	var payload struct {
		MixerID string `json:"mixer_id"`
	}
	if jsonErr := json.Unmarshal(entry.Value(), &payload); jsonErr == nil && payload.MixerID != "" {
		fmt.Print(payload.MixerID)
		return
	}
	fmt.Print(string(entry.Value()))
}
