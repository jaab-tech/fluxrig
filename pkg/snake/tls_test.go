// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package snake

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// selfSigned writes a certificate and key for localhost and returns their paths and
// a pool that trusts the certificate.
func selfSigned(t *testing.T) (certPath, keyPath string, pool *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)

	dir := t.TempDir()
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	require.NoError(t, os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	require.NoError(t, os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600))

	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	pool = x509.NewCertPool()
	pool.AddCert(cert)
	return certPath, keyPath, pool
}

// This records what the Snake does when it is given a certificate, as the Mixer gives
// it one: it serves TLS, and it also accepts a plain connection and asks for no
// client certificate. It is a characterization of the current behaviour, so that
// changing it (ADR 0054, question 6) is a decision that changes this test.
func TestServer_TLSIsOptionalAndClientsAreNotAuthenticated(t *testing.T) {
	certPath, keyPath, pool := selfSigned(t)
	s, err := NewServer(context.Background(), Config{
		Port: -1, ClusterName: "tls-test", StoreDir: t.TempDir(),
		StreamName: "flux-msg", StreamSubjects: []string{"flux.msg.>"},
		TLSCert: certPath, TLSKey: keyPath,
	})
	require.NoError(t, err)
	defer s.Shutdown()

	// ClientURL is tls://0.0.0.0:<port> here; the port is what is needed.
	_, port, err := net.SplitHostPort(strings.TrimPrefix(s.ClientURL(), "tls://"))
	require.NoError(t, err)

	plain, err := nats.Connect("nats://127.0.0.1:" + port)
	require.NoError(t, err, "a plain connection is accepted although TLS is configured")
	assert.True(t, plain.IsConnected())
	assert.False(t, plain.TLSRequired(), "the server did not ask for TLS")
	plain.Close()

	secure, err := nats.Connect("tls://127.0.0.1:"+port, nats.Secure(&tls.Config{RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS12}))
	require.NoError(t, err, "a TLS connection with no client certificate is accepted")
	assert.True(t, secure.IsConnected())
	secure.Close()
}
