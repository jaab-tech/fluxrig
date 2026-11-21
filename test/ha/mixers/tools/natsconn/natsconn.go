// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

// Package natsconn gives the testbed tools one way to reach the NATS cluster:
// the URL, and the certificate of a client when the cluster requires TLS.
package natsconn

import (
	"flag"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/jaab-tech/fluxrig/pkg/bus"
)

// Options are the flags of a connection.
type Options struct {
	url, ca, cert, key, inbox *string
}

// Flags registers -url, -ca, -cert, -key and -inbox on the default flag set.
func Flags() *Options {
	return &Options{
		url:   flag.String("url", "nats://127.0.0.1:4223", "NATS URL, comma-separated"),
		ca:    flag.String("ca", "", "CA that verifies the NATS servers; empty for plaintext"),
		cert:  flag.String("cert", "", "client certificate"),
		key:   flag.String("key", "", "key of the client certificate"),
		inbox: flag.String("inbox", "", "inbox prefix; a Rack's user may read only its own"),
	}
}

// Connect opens a connection with the flags, and opts after them.
func (o *Options) Connect(opts ...nats.Option) (*nats.Conn, error) {
	sec, err := bus.Security{RootCA: *o.ca, ClientCert: *o.cert, ClientKey: *o.key, InboxPrefix: *o.inbox}.Options()
	if err != nil {
		return nil, err
	}
	all := append([]nats.Option{nats.Timeout(5 * time.Second)}, sec...)
	return nats.Connect(*o.url, append(all, opts...)...)
}
