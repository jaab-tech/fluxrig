// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package bus

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

// Security is how a client proves itself to NATS and checks NATS in return.
// Every connection of a Rack or a Mixer builds its options here, so none of them
// can miss a setting.
type Security struct {
	// RootCA is the CA file that verifies the server certificate.
	RootCA string
	// ClientCert and ClientKey are the certificate this client presents. Set
	// both or neither.
	ClientCert string
	ClientKey  string
	// Credentials is a NATS credentials file: the user of this client, with the
	// permissions that the operator gave it.
	Credentials string
	// InboxPrefix replaces the default _INBOX prefix of the replies this
	// client receives. A NATS that limits each client to its own inboxes needs
	// it: the replies of every client, JetStream deliveries included, go to an
	// inbox, so a client allowed _INBOX.> reads them all.
	InboxPrefix string
	// PingInterval and MaxPingsOut decide how fast this client notices a
	// server that went silent: after MaxPingsOut unanswered pings sent
	// PingInterval apart it reconnects to another one. A server that dies
	// without closing the connection (a crash, a cut link) is noticed only so,
	// and until then every request on the connection times out. Zero keeps
	// the client defaults (2m, 2).
	PingInterval time.Duration
	MaxPingsOut  int
	// InsecureSkipVerify skips the check of the server certificate. Development only.
	InsecureSkipVerify bool
	// ServerName is the name to verify in the server certificate. Empty takes
	// the host of the URL in use, which is right for a cluster with one name
	// for each server.
	ServerName string
}

// Options returns the connection options. It fails on a file it cannot read, so
// a wrong path stops the start with its own error, not with a failed handshake.
func (s Security) Options() ([]nats.Option, error) {
	var opts []nats.Option
	if s.InboxPrefix != "" {
		opts = append(opts, nats.CustomInboxPrefix(s.InboxPrefix))
	}
	if s.PingInterval > 0 {
		opts = append(opts, nats.PingInterval(s.PingInterval))
	}
	if s.MaxPingsOut > 0 {
		opts = append(opts, nats.MaxPingsOutstanding(s.MaxPingsOut))
	}
	if s.Credentials != "" {
		if _, err := os.Stat(s.Credentials); err != nil {
			return nil, fmt.Errorf("bus: credentials file: %w", err)
		}
		opts = append(opts, nats.UserCredentials(s.Credentials))
	}
	if !s.usesTLS() {
		return opts, nil
	}
	conf := &tls.Config{
		InsecureSkipVerify: s.InsecureSkipVerify, //nolint:gosec // allowed for dev/test environments
		MinVersion:         tls.VersionTLS12,
		ServerName:         s.ServerName,
	}
	if s.RootCA != "" {
		pem, err := os.ReadFile(s.RootCA)
		if err != nil {
			return nil, fmt.Errorf("failed to read root ca: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("failed to append root ca (no PEM blocks found)")
		}
		conf.RootCAs = pool
	}
	if (s.ClientCert == "") != (s.ClientKey == "") {
		return nil, errors.New("bus: a client certificate needs both its certificate and its key")
	}
	if s.ClientCert != "" {
		cert, err := tls.LoadX509KeyPair(s.ClientCert, s.ClientKey)
		if err != nil {
			return nil, fmt.Errorf("bus: client certificate: %w", err)
		}
		conf.Certificates = []tls.Certificate{cert}
	}
	return append(opts, nats.Secure(conf)), nil
}

// KeyFiles returns the credential and key files this client reads, skipping
// the ones left unset. A caller that starts with these warns when one is
// readable by other users on the machine (see WorldReadable): a client
// certificate, key or credentials file the whole machine can read lets any
// local user speak to NATS as this client.
func (s Security) KeyFiles() []string {
	var out []string
	for _, p := range []string{s.RootCA, s.ClientCert, s.ClientKey, s.Credentials} {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// WorldReadable reports whether path exists and grants read access beyond its
// owner: group or other read bits. A missing or unreadable path is not world
// readable — it fails closed elsewhere, in Options, with its own error.
func WorldReadable(path string) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	return fi.Mode().Perm()&0o044 != 0
}

// RackInboxPrefix is the prefix of the reply inboxes of a Rack. With its own
// prefix, a NATS that gives each Rack its own permissions can let a Rack read
// only its own replies ("_INBOX_<rack>.>") instead of everyone's ("_INBOX.>").
func RackInboxPrefix(rack string) string {
	return "_INBOX_" + fluxmsg.SubjectToken(rack)
}

func (s Security) usesTLS() bool {
	return s.InsecureSkipVerify || s.RootCA != "" || s.ClientCert != "" || s.ClientKey != ""
}
