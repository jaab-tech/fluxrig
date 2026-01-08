// Copyright 2025 JAAB Tech SAS, Uruguay
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
