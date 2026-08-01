// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package io

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
)

// genSelfSigned writes a self-signed cert/key pair valid for 127.0.0.1 into
// dir and returns their paths.
func genSelfSigned(t *testing.T, dir string) (certFile, keyFile string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "iso8583-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}

	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certFile, keyFile
}

func TestTLSBuildValidation(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := genSelfSigned(t, dir)

	// Server without an identity must fail.
	if _, err := (&TLSConfig{Enabled: true}).buildServer(); err == nil {
		t.Fatalf("server without cert/key accepted")
	}
	// mTLS without a trust root must fail.
	bad := &TLSConfig{Enabled: true, CertFile: certFile, KeyFile: keyFile, ClientAuth: true}
	if _, err := bad.buildServer(); err == nil {
		t.Fatalf("client_auth without ca_file accepted")
	}
	// Missing files must fail loudly.
	missing := &TLSConfig{Enabled: true, CAFile: filepath.Join(dir, "nope.pem")}
	if _, err := missing.buildClient("host:1"); err == nil {
		t.Fatalf("missing ca_file accepted")
	}

	// A complete server + client pair builds.
	srv := &TLSConfig{Enabled: true, CertFile: certFile, KeyFile: keyFile, CAFile: certFile, ClientAuth: true}
	sc, err := srv.buildServer()
	if err != nil {
		t.Fatalf("buildServer: %v", err)
	}
	if sc.ClientAuth != tls.RequireAndVerifyClientCert || sc.MinVersion != tls.VersionTLS12 {
		t.Fatalf("server config wrong: auth=%v min=%x", sc.ClientAuth, sc.MinVersion)
	}
	cli := &TLSConfig{Enabled: true, CAFile: certFile, CertFile: certFile, KeyFile: keyFile}
	cc, err := cli.buildClient("127.0.0.1:9999")
	if err != nil {
		t.Fatalf("buildClient: %v", err)
	}
	if cc.ServerName != "127.0.0.1" || len(cc.Certificates) != 1 {
		t.Fatalf("client config wrong: sn=%q certs=%d", cc.ServerName, len(cc.Certificates))
	}
}

// End-to-end: the client dials a TLS endpoint, link-state reports up, a
// framed message crosses the encrypted socket, and a peer-side close reports
// link-state down.
func TestClientTLSLoopbackAndLinkState(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := genSelfSigned(t, dir)

	serverCert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		t.Fatalf("load pair: %v", err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{serverCert},
	})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	// Test endpoint: accept one connection, read one length-prefixed frame,
	// then close the socket (driving the client's down transition).
	gotFrame := make(chan []byte, 1)
	go func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		hdr := make([]byte, 2)
		if _, rerr := io.ReadFull(conn, hdr); rerr != nil {
			_ = conn.Close()
			return
		}
		payload := make([]byte, binary.BigEndian.Uint16(hdr))
		if _, rerr := io.ReadFull(conn, payload); rerr != nil {
			_ = conn.Close()
			return
		}
		gotFrame <- payload
		_ = conn.Close()
	}()

	gen, err := idgen.New(uuid.New())
	if err != nil {
		t.Fatalf("idgen: %v", err)
	}
	// Build through ParseConfig, the production path, which also proves the
	// nested tls block decodes from scenario configuration.
	cfg, err := ParseConfig(map[string]any{
		"mode":    ModeClient,
		"connect": ln.Addr().String(),
		"tls": map[string]any{
			"enabled": true,
			"ca_file": certFile,
		},
	})
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	client := NewClient(cfg, slog.Default(), func(*fluxmsg.FluxMsg) {}, gen)

	linkCh := make(chan bool, 4)
	client.OnLinkState(func(up bool, connID string) {
		if connID == "" {
			t.Errorf("link state without conn id")
		}
		linkCh <- up
	})
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = client.Stop() }()

	select {
	case up := <-linkCh:
		if !up {
			t.Fatalf("first link-state transition must be up")
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("no conn.up within deadline")
	}

	msg := fluxmsg.New()
	msg.FluxID, _ = gen.NextFluxID()
	msg.RawPayload = []byte("0200-tls-probe")
	if _, err := client.Process(context.Background(), msg); err != nil {
		t.Fatalf("process: %v", err)
	}

	select {
	case frame := <-gotFrame:
		if string(frame) != "0200-tls-probe" {
			t.Fatalf("frame = %q, want the probe payload", frame)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("endpoint did not receive the framed message")
	}

	select {
	case up := <-linkCh:
		if up {
			t.Fatalf("expected conn.down after peer close")
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("no conn.down after peer close")
	}
}
