// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0
//
// jsleader prints the JetStream leader of each stream on the NATS cluster. It
// is how the testbed finds the server to kill, and, with -stepdown, how
// leader-cost moves the leaders of chosen streams to one site.
//
//	go run ./test/ha/mixers/tools/jsleader -url nats://127.0.0.1:4223
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/jaab-tech/fluxrig/test/ha/mixers/tools/natsconn"
)

func main() {
	conn := natsconn.Flags()
	stepdown := flag.String("stepdown", "", "comma-separated streams to force toward the -tag site")
	tag := flag.String("tag", "site:b", "tag to place a forced leader toward")
	preferred := flag.String("preferred", "", "server to move a forced leader to, instead of -tag")
	created := flag.Bool("created", false, "print when each stream was created, oldest first, instead of the leaders")
	flag.Parse()

	nc, err := conn.Connect()
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		os.Exit(2)
	}
	defer nc.Close()

	// When asked, move the named leaders toward a tag first. This is a
	// testbed action, not part of the product.
	if *stepdown != "" {
		placement := map[string]any{"tags": []string{*tag}}
		if *preferred != "" {
			placement = map[string]any{"preferred": *preferred}
		}
		body, _ := json.Marshal(map[string]any{"placement": placement})
		for _, name := range splitComma(*stepdown) {
			resp, reqErr := nc.Request("$JS.API.STREAM.LEADER.STEPDOWN."+name, body, 5*time.Second)
			if reqErr != nil {
				fmt.Fprintf(os.Stderr, "%s -> %v\n", name, reqErr)
				continue
			}
			fmt.Fprintf(os.Stderr, "%s -> %s\n", name, string(resp.Data))
		}
	}

	js, err := jetstream.New(nc)
	if err != nil {
		fmt.Fprintln(os.Stderr, "jetstream:", err)
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	names := []string{}
	for name := range js.StreamNames(ctx).Name() {
		names = append(names, name)
	}
	sort.Strings(names)

	out := map[string]string{}
	type stream struct {
		name string
		at   time.Time
	}
	var born []stream
	for _, name := range names {
		resp, reqErr := nc.Request(fmt.Sprintf("$JS.API.STREAM.INFO.%s", name), nil, 5*time.Second)
		if reqErr != nil {
			out[name] = "unknown"
			continue
		}
		var info struct {
			Created time.Time `json:"created"`
			Cluster struct {
				Leader string `json:"leader"`
			} `json:"cluster"`
		}
		if jsonErr := json.Unmarshal(resp.Data, &info); jsonErr != nil {
			out[name] = "unknown"
			continue
		}
		out[name] = info.Cluster.Leader
		born = append(born, stream{name, info.Created})
	}
	if *created {
		// The order in which a first start provisions the streams, and how
		// long each took after the one before.
		sort.Slice(born, func(i, j int) bool { return born[i].at.Before(born[j].at) })
		for i, st := range born {
			step := time.Duration(0)
			if i > 0 {
				step = st.at.Sub(born[i-1].at)
			}
			fmt.Printf("%s +%.1fs %s\n", st.at.UTC().Format(time.RFC3339Nano), step.Seconds(), st.name)
		}
		return
	}
	b, _ := json.Marshal(out)
	fmt.Println(string(b))
}

func splitComma(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ',' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
