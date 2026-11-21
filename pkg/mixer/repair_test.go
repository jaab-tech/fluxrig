// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package mixer

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/config"
	"github.com/jaab-tech/fluxrig/pkg/pki"
)

// Repair on an external NATS store is refused before anything is touched:
// the store belongs to its operator. The missing directory below proves no
// disk is even looked at: the guard runs first.
func TestRepair_ExternalStoreIsRefused(t *testing.T) {
	cfg := &config.MixerConfig{}
	cfg.Snake.External = true
	cfg.Store.Dir = filepath.Join(t.TempDir(), "does-not-exist")

	out := captureStdout(t, func() {
		if got := Repair(context.Background(), cfg, slog.Default(), false); got != RepairFailed {
			t.Errorf("Repair on an external store: %d, want RepairFailed (%d)", got, RepairFailed)
		}
	})
	assert.Contains(t, out, "external", "the refusal must say whose store it is")
	assert.NoDirExists(t, cfg.Store.Dir, "the refusal must not touch the store")
}

func TestRepair_TLSEnabledEmbeddedStoreUsesInProcessConnection(t *testing.T) {
	dir := t.TempDir()
	cluster, err := pki.GenerateClusterKey()
	require.NoError(t, err)
	require.NoError(t, cluster.Save(filepath.Join(dir, "cluster.key")))

	certPath, keyPath := writeRepairTLSFiles(t, dir)
	cfg := &config.MixerConfig{}
	cfg.Store.Dir = dir
	cfg.Store.ClusterKeyFile = "cluster.key"
	cfg.Snake.Port = -1
	cfg.Snake.StoreDir = filepath.Join(dir, "snake")
	cfg.Snake.StoreEncryption = false
	cfg.Snake.TLSCertFile = certPath
	cfg.Snake.TLSKeyFile = keyPath
	cfg.Snake.EventsMaxAge = "1h"

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	require.Equal(t, RepairOK, Repair(context.Background(), cfg, log, false))
}

func writeRepairTLSFiles(t *testing.T, dir string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	certPath := filepath.Join(dir, "snake.crt")
	keyPath := filepath.Join(dir, "snake.key")
	require.NoError(t, os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	require.NoError(t, os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600))
	return certPath, keyPath
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	defer func() { os.Stdout = old }()
	fn()
	require.NoError(t, w.Close())
	os.Stdout = old
	var buf bytes.Buffer
	_, err = io.Copy(&buf, r)
	require.NoError(t, err)
	return buf.String()
}
