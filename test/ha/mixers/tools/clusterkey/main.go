// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0
//
// clusterkey writes one cluster signing key for the testbed. Every Mixer of
// a cluster must sign with the same key: a Rack pins the key of its first
// passport and refuses any other, and a Mixer with the lease on refuses to
// start with a key the cluster does not use.
//
//	go run ./test/ha/mixers/tools/clusterkey -out test/ha/mixers/.build/cluster.key
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/jaab-tech/fluxrig/pkg/pki"
)

func main() {
	out := flag.String("out", "", "path of the key file to write")
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "clusterkey: -out is required")
		os.Exit(2)
	}
	key, err := pki.GenerateClusterKey()
	if err != nil {
		fmt.Fprintf(os.Stderr, "clusterkey: generate: %v\n", err)
		os.Exit(1)
	}
	if err := key.Save(*out); err != nil {
		fmt.Fprintf(os.Stderr, "clusterkey: save: %v\n", err)
		os.Exit(1)
	}
}
