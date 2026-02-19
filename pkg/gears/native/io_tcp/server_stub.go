// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !darwin && !dragonfly && !freebsd && !netbsd && !openbsd

package io_tcp

import (
	"syscall"
)

func reusePortControl(network, address string, c syscall.RawConn) error {
	// No-op for non-unix systems (e.g. Windows)
	return nil
}
