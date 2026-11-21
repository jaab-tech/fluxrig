// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0
//
// s3server is an S3-compatible object store in memory, for the testbed's s3
// telemetry store: the host cannot pull an image of a real one. It checks no
// credentials, and loses everything when it stops.
//
//	go run ./test/ha/mixers/tools/s3server -addr :9000
package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
)

func main() {
	addr := flag.String("addr", ":9000", "address to listen on")
	flag.Parse()
	srv := &http.Server{Addr: *addr, Handler: gofakes3.New(s3mem.New()).Server(), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
