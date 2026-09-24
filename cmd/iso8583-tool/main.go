// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"flag"
	"log"
	"os"
)

var (
	mode          = flag.String("mode", "load", "Operation mode: load | echo | auth (terminal) | scheme (host fixture) | decode")
	decodeMessage = flag.String("message", "", "Hex-encoded ISO8583 message to decode (for decode mode)")
	decodeSpec    = flag.String("spec", "", "Path to SDL spec file (required for decode mode)")
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
	case "decode":
		runDecodeMode()
	default:
		log.SetOutput(os.Stderr)
		log.Fatalf("Unknown mode: %s. Use one of: load, echo, auth, scheme, decode", *mode)
	}
}
