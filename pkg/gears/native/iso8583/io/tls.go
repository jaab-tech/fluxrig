// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package io

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
)

// TLSConfig enables native TLS on the gear's external socket, standard
// library only, no sidecars or proxies. In client mode it secures the dial to
// the remote endpoint; in server mode it wraps the listener. Providing both
// cert_file and key_file supplies a local identity (required for servers,
// optional for clients); ca_file pins the peer's trust root. Setting
// client_auth on a server requires and verifies client certificates (mTLS).
type TLSConfig struct {
	// Enabled turns TLS on for the external socket.
	Enabled bool `json:"enabled" mapstructure:"enabled"`
	// CertFile / KeyFile: this endpoint's PEM identity.
	CertFile string `json:"cert_file" mapstructure:"cert_file"`
	KeyFile  string `json:"key_file" mapstructure:"key_file"`
	// CAFile: PEM trust root(s) used to verify the peer. When empty, a client
	// falls back to the system pool; a server with client_auth requires it.
	CAFile string `json:"ca_file" mapstructure:"ca_file"`
	// ServerName overrides the hostname verified by a client (defaults to the
	// host part of connect).
	ServerName string `json:"server_name" mapstructure:"server_name"`
	// ClientAuth (server mode): require and verify a client certificate.
	ClientAuth bool `json:"client_auth" mapstructure:"client_auth"`
}

// buildClient returns the tls.Config for dialing connectTarget.
func (t *TLSConfig) buildClient(connectTarget string) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}

	serverName := t.ServerName
	if serverName == "" {
		host, _, err := net.SplitHostPort(connectTarget)
		if err != nil {
			return nil, fmt.Errorf("tls: cannot derive server name from connect %q: %w", connectTarget, err)
		}
		serverName = host
	}
	cfg.ServerName = serverName

	if t.CAFile != "" {
		pool, err := loadCertPool(t.CAFile)
		if err != nil {
			return nil, err
		}
		cfg.RootCAs = pool
	}

	if t.CertFile != "" || t.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(t.CertFile, t.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("tls: loading client identity: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}

	return cfg, nil
}

// buildServer returns the tls.Config for wrapping the listener.
func (t *TLSConfig) buildServer() (*tls.Config, error) {
	if t.CertFile == "" || t.KeyFile == "" {
		return nil, fmt.Errorf("tls: server mode requires cert_file and key_file")
	}
	cert, err := tls.LoadX509KeyPair(t.CertFile, t.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("tls: loading server identity: %w", err)
	}
	cfg := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
	}

	if t.ClientAuth {
		if t.CAFile == "" {
			return nil, fmt.Errorf("tls: client_auth requires ca_file")
		}
		pool, err := loadCertPool(t.CAFile)
		if err != nil {
			return nil, err
		}
		cfg.ClientCAs = pool
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}

	return cfg, nil
}

func loadCertPool(caFile string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(caFile) //nolint:gosec // operator-provided path from gear config
	if err != nil {
		return nil, fmt.Errorf("tls: reading ca_file: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("tls: ca_file %q contains no usable certificates", caFile)
	}
	return pool, nil
}
