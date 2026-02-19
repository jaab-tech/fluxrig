// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package version

import (
	"fmt"
	"runtime"
)

// These variables are set via -ldflags at build time
var (
	Version   = "0.0.0-dev"
	Commit    = "none"
	BuildDate = "unknown"
	Dirty     = "" // "-dirty" if tree was dirty
)

// String returns the complete version string
func String() string {
	v := Version
	if Commit != "none" {
		v += fmt.Sprintf("+%s", Commit)
	}
	if Dirty != "" {
		v += Dirty
	}
	return v
}

// FullInfo returns detailed version info with Go version and OS/Arch
func FullInfo() string {
	c := Commit
	if Dirty != "" {
		c += Dirty
	}
	return fmt.Sprintf("fluxrig %s ( %s, %s/%s ) %s", Version, BuildDate, runtime.GOOS, runtime.GOARCH, c)
}
