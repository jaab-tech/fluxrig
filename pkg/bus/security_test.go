// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package bus

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/snake"
)

// testPKI writes a CA, a server certificate for 127.0.0.1 and a client
// certificate, and returns their paths.
type testPKI struct{ ca, serverCert, serverKey, clientCert, clientKey string }

func writePEM(t *testing.T, path, kind string, der []byte) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der}), 0o600))
}

func newTestPKI(t *testing.T) testPKI {
	t.Helper()
	dir := t.TempDir()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	p := testPKI{ca: filepath.Join(dir, "ca.pem")}
	writePEM(t, p.ca, "CERTIFICATE", caDER)
	caCert, err := x509.ParseCertificate(caDER)
	require.NoError(t, err)

	issue := func(name string, serial int64, usage x509.ExtKeyUsage) (string, string) {
		key, errKey := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, errKey)
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: name},
			NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
			ExtKeyUsage: []x509.ExtKeyUsage{usage},
			IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("0.0.0.0")},
			DNSNames:    []string{"localhost"},
		}
		der, errCert := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
		require.NoError(t, errCert)
		keyDER, errKeyDER := x509.MarshalECPrivateKey(key)
		require.NoError(t, errKeyDER)
		cert, keyPath := filepath.Join(dir, name+".pem"), filepath.Join(dir, name+".key")
		writePEM(t, cert, "CERTIFICATE", der)
		writePEM(t, keyPath, "EC PRIVATE KEY", keyDER)
		return cert, keyPath
	}
	p.serverCert, p.serverKey = issue("server", 2, x509.ExtKeyUsageServerAuth)
	p.clientCert, p.clientKey = issue("client", 3, x509.ExtKeyUsageClientAuth)
	return p
}

func mtlsServer(t *testing.T, p testPKI) string {
	t.Helper()
	s, err := snake.NewServer(context.Background(), snake.Config{
		Port: -1, ClusterName: "flux", StoreDir: t.TempDir(),
		TLSCert: p.serverCert, TLSKey: p.serverKey, TLSCA: p.ca, TLSVerify: true,
	})
	require.NoError(t, err)
	t.Cleanup(s.Shutdown)
	return s.ClientURL()
}

func dial(url string, opts ConnectOptions) error {
	opts.Name, opts.ConnectTimeout, opts.InitialRetryAttempts = "test", 2*time.Second, 1
	b := NewNatsBus("flux")
	err := b.Connect(url, opts)
	if err == nil {
		b.Close()
	}
	return err
}

// tls_verify asks a client for a certificate, so a client must be able to give
// one. Before, a Rack had no way to present it, and could not connect at all.
func TestMutualTLS_ClientCertificateIsPresented(t *testing.T) {
	p := newTestPKI(t)
	url := mtlsServer(t, p)

	require.NoError(t, dial(url, ConnectOptions{
		RootCA: p.ca, ClientCert: p.clientCert, ClientKey: p.clientKey, External: true,
	}))
	require.Error(t, dial(url, ConnectOptions{RootCA: p.ca, External: true}),
		"a client with no certificate must be refused")
}

func TestSecurity_RefusesWhatItCannotUse(t *testing.T) {
	p := newTestPKI(t)
	for name, s := range map[string]Security{
		"certificate without key": {ClientCert: p.clientCert},
		"key without certificate": {ClientKey: p.clientKey},
		"missing credentials":     {Credentials: filepath.Join(t.TempDir(), "none.creds")},
		"missing ca":              {RootCA: filepath.Join(t.TempDir(), "none.pem")},
	} {
		_, err := s.Options()
		assert.Error(t, err, name)
	}
}

func TestSecurity_PlainNeedsNoOptions(t *testing.T) {
	opts, err := Security{}.Options()
	require.NoError(t, err)
	assert.Empty(t, opts)
}

func TestSecurity_InboxPrefix(t *testing.T) {
	opts, err := Security{InboxPrefix: "_INBOX_rack-1"}.Options()
	require.NoError(t, err)
	var o nats.Options
	for _, opt := range opts {
		require.NoError(t, opt(&o))
	}
	assert.Equal(t, "_INBOX_rack-1", o.InboxPrefix)
}

func TestRackInboxPrefix(t *testing.T) {
	for in, want := range map[string]string{"rack-1": "_INBOX_rack-1", "rack.1": "_INBOX_rack_1", "r*>": "_INBOX_r"} {
		assert.Equal(t, want, RackInboxPrefix(in), in)
	}
}

// A stream that does not exist yet reads as not provisioned through every
// wrap the subscription path adds.
func TestNotProvisioned(t *testing.T) {
	err := fmt.Errorf("failed to create ephemeral consumer: %w", fmt.Errorf("bus: read stream flux-msg: %w", jetstream.ErrStreamNotFound))
	assert.True(t, NotProvisioned(err))
	assert.False(t, NotProvisioned(errors.New("boom")))
}

func TestSecurity_Pings(t *testing.T) {
	opts, err := Security{PingInterval: 5 * time.Second, MaxPingsOut: 3}.Options()
	require.NoError(t, err)
	var o nats.Options
	for _, opt := range opts {
		require.NoError(t, opt(&o))
	}
	assert.Equal(t, 5*time.Second, o.PingInterval)
	assert.Equal(t, 3, o.MaxPingsOut)
}
