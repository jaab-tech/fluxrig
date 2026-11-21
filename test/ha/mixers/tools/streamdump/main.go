// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0
//
// streamdump prints what a stream holds on one subject, and the state of the
// consumers that read it. The testbed runs it when the traffic between the
// Racks loses a message, before the containers go: it tells a message that
// never reached the stream from one the stream holds and a consumer skipped.
//
//	go run ./test/ha/mixers/tools/streamdump -url nats://127.0.0.1:4223 -subject flux.msg.rack-r1.dp-sender.out
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/test/ha/mixers/tools/natsconn"
)

func main() {
	conn := natsconn.Flags()
	stream := flag.String("stream", "flux-msg", "stream to read")
	subject := flag.String("subject", "", "subject to read from the stream")
	field := flag.String("field", "seq", "field of the message data to print; * prints all of it")
	timeout := flag.Duration("timeout", 30*time.Second, "bound on the whole dump")
	flag.Parse()
	if *subject == "" {
		fmt.Fprintln(os.Stderr, "-subject is required")
		os.Exit(2)
	}

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
	s, err := js.Stream(ctx, *stream)
	if err != nil {
		fmt.Fprintln(os.Stderr, "stream:", err)
		os.Exit(2)
	}

	// The consumers first: reading the messages does not move them, but the
	// Racks go on while this runs.
	for info := range s.ListConsumers(ctx).Info() {
		c := info.Config
		if c.FilterSubject != *subject && !contains(c.FilterSubjects, *subject) {
			continue
		}
		fmt.Printf("consumer %s leader=%s delivered=%d ack_floor=%d pending=%d ack_pending=%d redelivered=%d ack=%s deliver=%s\n",
			info.Name, leader(info), info.Delivered.Stream, info.AckFloor.Stream, info.NumPending,
			info.NumAckPending, info.NumRedelivered, c.AckPolicy, c.DeliverPolicy)
	}

	// Then every message of the subject, oldest first, each asked of the
	// stream by sequence ("the next one with this subject from here"): no
	// consumer, so no consumer state to lose while a member is down. The
	// stream says how many there are.
	info, err := s.Info(ctx, jetstream.WithSubjectFilter(*subject))
	if err != nil {
		fmt.Fprintln(os.Stderr, "stream info:", err)
		os.Exit(2)
	}
	want := info.State.Subjects[*subject]
	fmt.Printf("stream %s holds %d messages on %s, last seq %d\n", *stream, want, *subject, info.State.LastSeq)
	next := info.State.FirstSeq
	for read := uint64(0); read < want; read++ {
		var msg *jetstream.RawStreamMsg
		for attempt := 1; ; attempt++ {
			gctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			msg, err = s.GetMsg(gctx, next, jetstream.WithGetMsgSubject(*subject))
			cancel()
			if err == nil || attempt == 3 || ctx.Err() != nil {
				break
			}
		}
		if err != nil {
			fmt.Printf("read %d of %d, at seq %d: %v\n", read, want, next, err)
			break
		}
		next = msg.Sequence + 1
		var fm fluxmsg.FluxMsg
		value := "?"
		if cbor.Unmarshal(msg.Data, &fm) == nil {
			if *field == "*" {
				b, _ := json.Marshal(jsonSafe(fm.Data))
				value = string(b)
			} else if v, ok := fm.Data[*field]; ok {
				b, _ := json.Marshal(v)
				value = string(b)
			}
		}
		fmt.Printf("msg stream_seq=%d %s=%s at=%s msg_id=%s\n", msg.Sequence, *field, value,
			msg.Time.UTC().Format(time.RFC3339Nano), msg.Header.Get("Nats-Msg-Id"))
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func leader(info *jetstream.ConsumerInfo) string {
	if info.Cluster == nil {
		return ""
	}
	return info.Cluster.Leader
}

// jsonSafe turns the map[interface{}]interface{} that CBOR gives a nested map
// into something encoding/json takes.
func jsonSafe(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = jsonSafe(val)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[fmt.Sprint(k)] = jsonSafe(val)
		}
		return out
	case []any:
		for i := range t {
			t[i] = jsonSafe(t[i])
		}
		return t
	case []byte:
		return string(t)
	}
	return v
}
