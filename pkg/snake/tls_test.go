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

// caAndClientCert issues a CA, a server certificate signed by it (for
// localhost/127.0.0.1) and a client certificate signed by the same CA, so an
// mTLS test has a realistic chain instead of one cert standing in for
// everything. The CA is also returned as a pool a client or server can trust.
func caAndClientCert(t *testing.T) (caCertPath, serverCertPath, serverKeyPath, clientCertPath, clientKeyPath string, pool *x509.CertPool) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	caCert, err := x509.ParseCertificate(caDER)
	require.NoError(t, err)

	pool = x509.NewCertPool()
	pool.AddCert(caCert)

	caDir := t.TempDir()
	caCertPath = filepath.Join(caDir, "ca-cert.pem")
	require.NoError(t, os.WriteFile(caCertPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o600))

	issue := func(name string, ekus []x509.ExtKeyUsage) (certPath, keyPath string) {
		key, errKey := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, errKey)
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(2),
			Subject:      pkix.Name{CommonName: name},
			DNSNames:     []string{"localhost"},
			IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  ekus,
		}
		der, errCert := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
		require.NoError(t, errCert)
		keyDER, errMarshal := x509.MarshalECPrivateKey(key)
		require.NoError(t, errMarshal)

		dir := t.TempDir()
		certPath = filepath.Join(dir, name+"-cert.pem")
		keyPath = filepath.Join(dir, name+"-key.pem")
		require.NoError(t, os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
		require.NoError(t, os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600))
		return certPath, keyPath
	}

	serverCertPath, serverKeyPath = issue("server", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
	clientCertPath, clientKeyPath = issue("client", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
	return caCertPath, serverCertPath, serverKeyPath, clientCertPath, clientKeyPath, pool
}

// Resolved (ADR 0054, question 6): configuring TLS makes it required.
// AllowNonTLS defaults to false, so a plain connection is refused once a
// certificate is given, the way the Mixer gives it one. TLS with no client
// certificate is still accepted: TLSVerify is a separate, still-off-by-default
// opt-in (see TestServer_TLSVerifyRequiresAClientCertificate).
func TestServer_RefusesPlainConnectionsByDefaultOnceTLSIsConfigured(t *testing.T) {
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
	if err == nil {
		plain.Close()
	}
	require.Error(t, err, "a plain connection must be refused once TLS is configured and AllowNonTLS is not set")

	secure, err := nats.Connect("tls://127.0.0.1:"+port, nats.Secure(&tls.Config{RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS12}))
	require.NoError(t, err, "a TLS connection with no client certificate is still accepted: TLSVerify is a separate opt-in")
	assert.True(t, secure.IsConnected())
	secure.Close()
}

// The Mixer's own connection to its embedded Snake (provisioning, its own
// bus) is not a network client the TLS-required-by-default policy is about,
// so it must keep working even when a plain network client is refused.
func TestServer_InProcessConnBypassesAllowNonTLS(t *testing.T) {
	certPath, keyPath, _ := selfSigned(t)
	s, err := NewServer(context.Background(), Config{
		Port: -1, ClusterName: "tls-test", StoreDir: t.TempDir(),
		StreamName: "flux-msg", StreamSubjects: []string{"flux.msg.>"},
		TLSCert: certPath, TLSKey: keyPath, // AllowNonTLS left false
	})
	require.NoError(t, err)
	defer s.Shutdown()

	nc, err := s.InProcessConn()
	require.NoError(t, err, "the Mixer's own in-process connection must not be subject to AllowNonTLS")
	defer nc.Close()
	assert.True(t, nc.IsConnected())
}

// AllowNonTLS is the explicit opt-in an operator can still reach for.
func TestServer_AllowNonTLSAcceptsPlainConnectionsAlongsideTLS(t *testing.T) {
	certPath, keyPath, _ := selfSigned(t)
	s, err := NewServer(context.Background(), Config{
		Port: -1, ClusterName: "tls-test", StoreDir: t.TempDir(),
		StreamName: "flux-msg", StreamSubjects: []string{"flux.msg.>"},
		TLSCert: certPath, TLSKey: keyPath, AllowNonTLS: true,
	})
	require.NoError(t, err)
	defer s.Shutdown()

	_, port, err := net.SplitHostPort(strings.TrimPrefix(s.ClientURL(), "tls://"))
	require.NoError(t, err)

	plain, err := nats.Connect("nats://127.0.0.1:" + port)
	require.NoError(t, err, "AllowNonTLS must still let a plain connection through")
	assert.True(t, plain.IsConnected())
	plain.Close()
}

// TLSVerify (with TLSCA set) is the mTLS opt-in: a client that presents no
// certificate is refused once it is on, the way TLSCA/TLSVerify are meant to
// be reachable from snake.tls_ca_file / snake.tls_verify.
func TestServer_TLSVerifyRequiresAClientCertificate(t *testing.T) {
	caCertPath, serverCertPath, serverKeyPath, clientCertPath, clientKeyPath, pool := caAndClientCert(t)
	s, err := NewServer(context.Background(), Config{
		Port: -1, ClusterName: "tls-test", StoreDir: t.TempDir(),
		StreamName: "flux-msg", StreamSubjects: []string{"flux.msg.>"},
		TLSCert: serverCertPath, TLSKey: serverKeyPath, TLSCA: caCertPath, TLSVerify: true,
	})
	require.NoError(t, err)
	defer s.Shutdown()

	_, port, err := net.SplitHostPort(strings.TrimPrefix(s.ClientURL(), "tls://"))
	require.NoError(t, err)

	noCert, err := nats.Connect("tls://127.0.0.1:"+port, nats.Secure(&tls.Config{RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS12}))
	if err == nil {
		noCert.Close()
	}
	require.Error(t, err, "TLSVerify must refuse a client that presents no certificate")

	withCert, err := nats.Connect("tls://127.0.0.1:"+port, nats.ClientCert(clientCertPath, clientKeyPath), nats.Secure(&tls.Config{RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS12}))
	require.NoError(t, err, "a client presenting the CA-signed certificate must be accepted")
	assert.True(t, withCert.IsConnected())
	withCert.Close()
}
