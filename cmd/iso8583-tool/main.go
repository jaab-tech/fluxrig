// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"flag"
	"log"
	"os"
)

var (
	mode = flag.String("mode", "load", "Operation mode: load | echo | auth (terminal) | scheme (host fixture)")
)

func main() {
	flag.Parse()

	switch *mode {
	case "load":
		runLoadMode()
	case "echo":
		runEchoMode()
	case "auth":
		runAuthMode()
	case "scheme":
		runSchemeMode()
	default:
		log.SetOutput(os.Stderr)
		log.Fatalf("Unknown mode: %s. Use one of: load, echo, auth, scheme", *mode)
	}
}
