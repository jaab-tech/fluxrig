// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0
//
// probe connects to NATS as one Rack and tries what that Rack's permissions
// must refuse: another Rack's messages, events, durable readers, inbox and
// notifications, the registry, changes to the streams, and the forwarded
// control writes between the Mixers. It also tries
// what the Rack must still be able to do. It needs the certificate of the
// Rack and one of an unrestricted user, which prepares a durable reader of
// the other Rack.
//
//	go run ./test/ha/mixers/tools/probe -url tls://127.0.0.1:4223 -ca ca.crt \
//	    -cert rack-r1.crt -key rack-r1.key -admin-cert admin.crt -admin-key admin.key
//
// A refused publish or subscribe arrives apart from the call, and a refused
// request only times out, so the probe waits a moment after each one. It exits
// 3 when any check does not give what it should.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/jaab-tech/fluxrig/pkg/bus"
)

var wrong int

func check(name string, wantBlocked bool, err error) {
	got := err != nil
	verdict := "ok"
	if got != wantBlocked {
		verdict = "WRONG"
		wrong++
	}
	state := "allowed"
	if got {
		state = "blocked"
	}
	fmt.Printf("%-5s %-7s %s\n", verdict, state, name)
}

func main() {
	url := flag.String("url", "tls://127.0.0.1:4223", "one NATS server")
	ca := flag.String("ca", "", "CA of the servers")
	cert := flag.String("cert", "", "certificate of the Rack")
	key := flag.String("key", "", "key of the Rack")
	adminCert := flag.String("admin-cert", "", "certificate of an unrestricted user")
	adminKey := flag.String("admin-key", "", "key of the unrestricted user")
	rack := flag.String("rack", "rack-r1", "name of the Rack the certificate belongs to")
	other := flag.String("other", "rack-r2", "name of another Rack")
	stream := flag.String("stream", "flux-msg", "message stream")
	flag.Parse()

	admin, err := connect(*url, bus.Security{RootCA: *ca, ClientCert: *adminCert, ClientKey: *adminKey})
	if err != nil {
		fmt.Fprintln(os.Stderr, "admin:", err)
		os.Exit(2)
	}
	defer admin.Close()
	ajs, _ := jetstream.New(admin)
	durable := *other + "_probe_reader"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	_, err = ajs.CreateOrUpdateConsumer(ctx, "flux-events", jetstream.ConsumerConfig{
		Durable: durable, FilterSubject: "flux.evt.conn." + *other + ".probe", AckPolicy: jetstream.AckExplicitPolicy,
		InactiveThreshold: time.Minute,
	})
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "admin: durable reader of the other Rack:", err)
		os.Exit(2)
	}

	refusals := make(chan error, 16)
	nc, err := connect(*url, bus.Security{RootCA: *ca, ClientCert: *cert, ClientKey: *key, InboxPrefix: bus.RackInboxPrefix(*rack)},
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, e error) { refusals <- e }))
	if err != nil {
		fmt.Fprintln(os.Stderr, "rack:", err)
		os.Exit(2)
	}
	defer nc.Close()
	js, _ := jetstream.New(nc)
	// refused returns the refusal of a publish or subscribe on subject, if one
	// arrives. A refusal names its subject, and one of an earlier check can
	// arrive late, so only one that names subject counts.
	refused := func(subject string) error {
		_ = nc.Flush()
		deadline := time.After(300 * time.Millisecond)
		for {
			select {
			case e := <-refusals:
				if strings.Contains(e.Error(), `"`+subject+`"`) {
					return e
				}
			case <-deadline:
				return nil
			}
		}
	}
	api := func(subject string, body any) error {
		b, _ := json.Marshal(body)
		m, errReq := nc.Request(subject, b, 2*time.Second)
		if errReq != nil {
			return errReq
		}
		var r struct {
			Error *struct {
				Description string `json:"description"`
			} `json:"error"`
		}
		_ = json.Unmarshal(m.Data, &r)
		if r.Error != nil {
			return fmt.Errorf("%s", r.Error.Description)
		}
		return nil
	}
	pub := func(subject string) error { _ = nc.Publish(subject, []byte("x")); return refused(subject) }
	sub := func(subject string) error {
		s, errSub := nc.Subscribe(subject, func(*nats.Msg) {})
		if errSub != nil {
			return errSub
		}
		defer func() { _ = s.Unsubscribe() }()
		return refused(subject)
	}

	fmt.Println("what belongs to another Rack, or to the platform")
	check("publish on another Rack's messages", true, pub("flux.msg."+*other+".gear.out"))
	check("publish an event of another Rack", true, pub("flux.evt.conn."+*other+".probe"))
	check("publish a control command", true, pub("flux.ctrl.link.another_gear"))
	check("publish a forwarded control write", true, pub("flux.mixer.control.approve"))
	check("publish a notification", true, pub("flux.agent.notify."+*other+".x"))
	check("write a registry key", true, pub("$KV.fluxrig_registry.x"))
	check("subscribe to another Rack's messages", true, sub("flux.msg."+*other+".>"))
	check("subscribe to every event", true, sub("flux.evt.conn.>"))
	check("subscribe to every control command", true, sub("flux.ctrl.>"))
	check("subscribe to forwarded control writes", true, sub("flux.mixer.control.>"))
	check("subscribe to every inbox", true, sub("_INBOX.>"))
	cctx, ccancel := context.WithTimeout(context.Background(), 2*time.Second)
	_, err = js.CreateConsumer(cctx, *stream, jetstream.ConsumerConfig{FilterSubject: "flux.msg." + *other + ".>", AckPolicy: jetstream.AckNonePolicy})
	ccancel()
	check("consumer on another Rack's messages", true, err)
	check("consumer, filter only in the body", true, api("$JS.API.CONSUMER.CREATE."+*stream, map[string]any{"stream_name": *stream, "config": map[string]any{"filter_subject": "flux.msg." + *other + ".>", "ack_policy": "none"}}))
	check("consumer on another Rack's notifications", true, api("$JS.API.CONSUMER.CREATE."+*stream+".n.flux.agent.notify."+*other+".*", map[string]any{"stream_name": *stream, "config": map[string]any{"name": "n", "filter_subject": "flux.agent.notify." + *other + ".*", "ack_policy": "none"}}))
	check("pull from another Rack's durable reader", true, api("$JS.API.CONSUMER.MSG.NEXT.flux-events."+durable, map[string]any{"batch": 1, "no_wait": true}))
	check("delete another Rack's durable reader", true, api("$JS.API.CONSUMER.DELETE.flux-events."+durable, nil))
	check("list the consumers", true, api("$JS.API.CONSUMER.NAMES."+*stream, map[string]any{}))
	check("list the streams", true, api("$JS.API.STREAM.NAMES", map[string]any{}))
	check("read the registry", true, api("$JS.API.DIRECT.GET.KV_fluxrig_registry", map[string]any{"seq": 1}))
	check("create a stream", true, api("$JS.API.STREAM.CREATE.probe", map[string]any{"name": "probe", "subjects": []string{"probe.>"}}))
	check("purge the message stream", true, api("$JS.API.STREAM.PURGE."+*stream, map[string]any{}))
	check("delete the event stream", true, api("$JS.API.STREAM.DELETE.flux-events", nil))

	fmt.Println("what the Rack does")
	check("publish its own messages", false, pub("flux.msg."+*rack+".gear.out"))
	check("publish its own events", false, pub("flux.evt.conn."+*rack+".probe"))
	check("read its notifications", false, api("$JS.API.CONSUMER.CREATE."+*stream+".own.flux.agent.notify."+*rack+".*", map[string]any{"stream_name": *stream, "config": map[string]any{"name": "own", "filter_subject": "flux.agent.notify." + *rack + ".*", "ack_policy": "none", "inactive_threshold": 5e9}}))

	fmt.Println("known to be open")
	fmt.Printf("note  %-7s %s\n", state(pub("flux.telemetry."+*other+".logs")), "publish telemetry under another Rack's name")

	_ = ajs.DeleteConsumer(context.Background(), "flux-events", durable)
	if wrong > 0 {
		fmt.Printf("%d checks did not give what they should\n", wrong)
		os.Exit(3)
	}
	fmt.Println("every check gave what it should")
}

func state(err error) string {
	if err != nil {
		return "blocked"
	}
	return "allowed"
}

func connect(url string, sec bus.Security, extra ...nats.Option) (*nats.Conn, error) {
	opts, err := sec.Options()
	if err != nil {
		return nil, err
	}
	opts = append(opts, nats.Timeout(5*time.Second), nats.MaxReconnects(0))
	return nats.Connect(url, append(opts, extra...)...)
}
