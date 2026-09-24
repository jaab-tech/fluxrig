// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package netutil

import (
	"net"
	"testing"
)

func TestLocalIPv4_ReturnsLoopbackWhenNoInterface(t *testing.T) {
	// This test verifies the fallback behavior.
	// We can't easily mock net.InterfaceAddrs, so we just verify
	// it returns a valid IPv4 address (at least the fallback).
	ip := LocalIPv4()
	if ip == "" {
		t.Fatal("LocalIPv4 returned empty string")
	}
	// Validate it's a valid IPv4
	if ip := net.ParseIP(ip); ip == nil {
		t.Errorf("LocalIPv4 returned invalid IP: %s", ip)
	}
	// Should be IPv4 (not IPv6)
	if ip4 := net.ParseIP(ip); ip4.To4() == nil {
		t.Errorf("LocalIPv4 returned non-IPv4 address: %s", ip)
	}
}

func TestLocalIPv4_NotEmpty(t *testing.T) {
	ip := LocalIPv4()
	if ip == "" {
		t.Error("LocalIPv4 should never return empty string")
	}
}
