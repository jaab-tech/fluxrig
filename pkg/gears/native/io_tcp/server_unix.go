// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package io_tcp

import (
	"syscall"

	"golang.org/x/sys/unix"
)

func reusePortControl(network, address string, c syscall.RawConn) error {
	var err error
	if err2 := c.Control(func(fd uintptr) {
		// Use unix package for constants to ensure cross-platform availability
		// on supported unix systems.
		err = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1)
		if err != nil {
			return
		}
		err = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
	}); err2 != nil {
		return err2
	}
	return err
}
