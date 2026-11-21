// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0
//
// scenariocheck reads scenario names, one per line on stdin, and checks that
// each one is stored in the registry bucket on the NATS cluster. run.sh
// feeds it every scenario import the API acknowledged, so a missing name is
// an acknowledged control write the cluster lost. It prints the missing
// names and exits 1 when there is any.
//
//	printf 'ha_probe_1\n' | go run ./test/ha/mixers/tools/scenariocheck -url nats://127.0.0.1:4223
package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/jaab-tech/fluxrig/test/ha/mixers/tools/natsconn"
)

func main() {
	conn := natsconn.Flags()
	bucket := flag.String("bucket", "fluxrig_registry", "registry bucket name")
	timeout := flag.Duration("timeout", 30*time.Second, "deadline for the whole check")
	replicas := flag.Int("replicas", 0, "when set, the replica count the registry bucket must have")
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
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	kv, err := js.KeyValue(ctx, *bucket)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bucket:", err)
		os.Exit(2)
	}

	if *replicas > 0 {
		st, errSt := js.Stream(ctx, "KV_"+*bucket)
		if errSt != nil {
			fmt.Fprintln(os.Stderr, "stream:", errSt)
			os.Exit(2)
		}
		if got := st.CachedInfo().Config.Replicas; got != *replicas {
			fmt.Printf("the registry bucket has %d replicas, want %d\n", got, *replicas)
			os.Exit(1)
		}
	}
	checked, missing := 0, 0
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		name := strings.TrimSpace(sc.Text())
		if name == "" {
			continue
		}
		checked++
		// The registry stores a scenario under its base64url-encoded name.
		_, errGet := kv.Get(ctx, "scenario."+base64.RawURLEncoding.EncodeToString([]byte(name)))
		switch {
		case errGet == nil:
		case errors.Is(errGet, jetstream.ErrKeyNotFound):
			fmt.Println("missing:", name)
			missing++
		default:
			fmt.Fprintln(os.Stderr, "get", name+":", errGet)
			os.Exit(2)
		}
	}
	fmt.Printf("checked %d acknowledged writes, %d missing\n", checked, missing)
	if missing > 0 {
		os.Exit(1)
	}
}
