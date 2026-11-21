// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0
//
// otlpreceiver takes OTLP over HTTP with JSON bodies, as an OpenTelemetry
// collector does, and appends each request to a file, one JSON line each:
// {"path": "/v1/logs", "body": {...}}. It is the testbed's otlp telemetry
// store. With -count it reads such a file instead, and prints how many times
// each log body matching -match arrived.
//
//	go run ./test/ha/mixers/tools/otlpreceiver -addr :4318 -out /data/otlp.jsonl
//	go run ./test/ha/mixers/tools/otlpreceiver -count otlp.jsonl -match 'ha-seq '
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

func main() {
	addr := flag.String("addr", ":4318", "address to listen on")
	out := flag.String("out", "otlp.jsonl", "file to append the requests to")
	count := flag.String("count", "", "read this file and count the log bodies, instead of serving")
	match := flag.String("match", "", "with -count, the prefix of the bodies to count")
	flag.Parse()
	if *count != "" {
		countBodies(*count, *match)
		return
	}
	f, err := os.OpenFile(*out, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		log.Fatal(err)
	}
	var mu sync.Mutex
	handler := func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil || !json.Valid(body) {
			http.Error(w, "the body is not JSON", http.StatusBadRequest)
			return
		}
		line, _ := json.Marshal(map[string]any{"path": r.URL.Path, "body": json.RawMessage(body)})
		mu.Lock()
		_, errWrite := f.Write(append(line, '\n'))
		mu.Unlock()
		if errWrite != nil {
			http.Error(w, errWrite.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	}
	mux := http.NewServeMux()
	for _, p := range []string{"/v1/logs", "/v1/metrics", "/v1/traces"} {
		mux.HandleFunc("POST "+p, handler)
	}
	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

// countBodies prints "<count> <body>" for each matching log body, sorted.
func countBodies(path, prefix string) {
	f, err := os.Open(path)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	seen := map[string]int{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		var req struct {
			Path string `json:"path"`
			Body struct {
				ResourceLogs []struct {
					ScopeLogs []struct {
						LogRecords []struct {
							Body struct {
								StringValue string `json:"stringValue"`
							} `json:"body"`
						} `json:"logRecords"`
					} `json:"scopeLogs"`
				} `json:"resourceLogs"`
			} `json:"body"`
		}
		if json.Unmarshal(sc.Bytes(), &req) != nil || req.Path != "/v1/logs" {
			continue
		}
		for _, rl := range req.Body.ResourceLogs {
			for _, sl := range rl.ScopeLogs {
				for _, rec := range sl.LogRecords {
					if strings.HasPrefix(rec.Body.StringValue, prefix) {
						seen[rec.Body.StringValue]++
					}
				}
			}
		}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("%d %s\n", seen[k], k)
	}
}
