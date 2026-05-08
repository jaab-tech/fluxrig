// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

// Package netutil provides small networking helpers for IP address discovery.
package netutil

import "net"

// LocalIPv4 returns the first non-loopback IPv4 address found on any active
// network interface. If no suitable interface is found, "127.0.0.1" is
// returned as a safe fallback. Callers should prefer an explicit address from
// configuration over this helper where possible.
func LocalIPv4() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String()
			}
		}
	}
	return "127.0.0.1"
}
