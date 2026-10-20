// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

// registry_dump prints the Mixer's registry for the e2e segments. By default
// it prints one line per entity, tab-separated: type_id, machine_id, name,
// status, version, attributes (JSON). -scenarios prints the imported scenario
// names, one per line. -active prints the active scenario's name, or nothing.
// The registry lives in a JetStream key-value bucket, not in a file a segment
// can query, so this is how a segment reads it.
package main

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

type record struct {
	TypeID     uint8          `json:"type_id"`
	MachineID  string         `json:"machine_id"`
	Name       string         `json:"name"`
	Status     string         `json:"status"`
	Version    string         `json:"version"`
	Attributes map[string]any `json:"attributes"`
}

func main() {
	url := flag.String("url", "nats://localhost:4222", "Snake URL")
	bucket := flag.String("bucket", "fluxrig_registry", "registry bucket")
	insecure := flag.Bool("tls-insecure", false, "skip TLS verification (test certificates)")
	scenarios := flag.Bool("scenarios", false, "print the imported scenario names")
	active := flag.Bool("active", false, "print the active scenario name")
	flag.Parse()

	opts := []nats.Option{nats.Timeout(5 * time.Second)}
	if strings.HasPrefix(*url, "tls://") {
		opts = append(opts, nats.Secure(&tls.Config{InsecureSkipVerify: *insecure})) //nolint:gosec // test tool, flag-gated
	}
	nc, err := nats.Connect(*url, opts...)
	if err != nil {
		fail(err)
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		fail(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	kv, err := js.KeyValue(ctx, *bucket)
	if err != nil {
		fail(err)
	}
	if *active {
		e, errGet := kv.Get(ctx, "active")
		if errGet != nil {
			if errors.Is(errGet, jetstream.ErrKeyNotFound) {
				return
			}
			fail(errGet)
		}
		var p struct {
			Name string `json:"name"`
		}
		if errDec := json.Unmarshal(e.Value(), &p); errDec != nil {
			fail(errDec)
		}
		fmt.Println(p.Name)
		return
	}
	if *scenarios {
		lister, errList := kv.ListKeysFiltered(ctx, "scenario.>")
		if errList != nil {
			if errors.Is(errList, jetstream.ErrNoKeysFound) {
				return
			}
			fail(errList)
		}
		var out []string
		for k := range lister.Keys() {
			if b, errDec := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(k, "scenario.")); errDec == nil {
				out = append(out, string(b))
			}
		}
		sort.Strings(out)
		for _, n := range out {
			fmt.Println(n)
		}
		return
	}
	keys, err := kv.ListKeysFiltered(ctx, "entity.>")
	if err != nil {
		fail(err)
	}
	var names []string
	for k := range keys.Keys() {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		e, errGet := kv.Get(ctx, k)
		if errGet != nil {
			if errors.Is(errGet, jetstream.ErrKeyNotFound) || errors.Is(errGet, jetstream.ErrKeyDeleted) {
				continue
			}
			fail(errGet)
		}
		var r record
		if errDec := json.Unmarshal(e.Value(), &r); errDec != nil {
			fail(fmt.Errorf("%s: %w", k, errDec))
		}
		attrs, _ := json.Marshal(r.Attributes)
		fmt.Printf("%d\t%s\t%s\t%s\t%s\t%s\n", r.TypeID, r.MachineID, r.Name, r.Status, r.Version, attrs)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "registry_dump:", err)
	os.Exit(1)
}
