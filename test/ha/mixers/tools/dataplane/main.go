// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0
//
// dataplane reads the log of the receiving Rack (docker logs --timestamps)
// and reports how the probe flow between the two Racks behaved: how many
// messages arrived, which sequence numbers are missing, and the longest gap
// between two arrivals. The longest gap is the data-plane interruption.
//
// The sender emits one JSON message per interval with a "seq" field; the
// receiver prints each message on its own line. Lines without "seq" are
// ignored.
//
//	docker logs --timestamps fluxrig-mx-r2 2>&1 | go run ./test/ha/mixers/tools/dataplane -after 2026-10-06T10:00:00Z
//
// It exits 1 when fewer than -min-after messages arrived after -after, the
// moment the fault ended: then the flow did not recover. One message is not
// enough to tell, since it may have been sent just before the flow stopped. It exits 3 when a sequence number
// between the first and the last is missing: the lane lost a message.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

type arrival struct {
	at  time.Time
	seq int64
}

func main() {
	afterRaw := flag.String("after", "", "RFC 3339 time: messages must arrive after it")
	minAfter := flag.Int("min-after", 1, "how many messages must arrive after -after")
	flag.Parse()
	var after time.Time
	if *afterRaw != "" {
		t, err := time.Parse(time.RFC3339, *afterRaw)
		if err != nil {
			fmt.Fprintln(os.Stderr, "after:", err)
			os.Exit(2)
		}
		after = t
	}

	var got []arrival
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		// "2026-10-06T10:00:00.123456789Z {...}": the receipt time from
		// docker, then the message.
		ts, rest, ok := strings.Cut(line, " ")
		if !ok || !strings.Contains(rest, `"seq"`) {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			continue
		}
		var msg struct {
			Seq int64 `json:"seq"`
		}
		// The first JSON value of the line: another write to the Rack's
		// stdout, a debug line, can follow it on the same line.
		if i := strings.Index(rest, "{"); i < 0 || json.NewDecoder(strings.NewReader(rest[i:])).Decode(&msg) != nil {
			continue
		}
		got = append(got, arrival{at: at, seq: msg.Seq})
	}
	if len(got) == 0 {
		fmt.Println("dataplane: no probe message arrived")
		os.Exit(1)
	}

	seen := map[int64]bool{}
	minSeq, maxSeq := got[0].seq, got[0].seq
	for _, a := range got {
		seen[a.seq] = true
		minSeq, maxSeq = min(minSeq, a.seq), max(maxSeq, a.seq)
	}
	missing := 0
	for s := minSeq; s <= maxSeq; s++ {
		if !seen[s] {
			missing++
		}
	}
	sort.SliceStable(got, func(i, j int) bool { return got[i].at.Before(got[j].at) })
	var gap time.Duration
	var gapAt time.Time
	for i := 1; i < len(got); i++ {
		if d := got[i].at.Sub(got[i-1].at); d > gap {
			gap, gapAt = d, got[i-1].at
		}
	}
	last := got[len(got)-1].at
	fmt.Printf("dataplane: %d messages, seq %d..%d, %d missing, %d duplicates, longest gap %.1fs from %s, last arrival %s\n",
		len(got), minSeq, maxSeq, missing, len(got)-len(seen), gap.Seconds(), gapAt.Format(time.RFC3339), last.Format(time.RFC3339))
	if !after.IsZero() {
		n := 0
		for _, a := range got {
			if a.at.After(after) {
				n++
			}
		}
		if n < *minAfter {
			fmt.Printf("dataplane: %d messages arrived after %s, want %d\n", n, after.Format(time.RFC3339Nano), *minAfter)
			os.Exit(1)
		}
	}
	// The guaranteed lane must not lose a message: a missing sequence
	// number is a failure of its own, whatever the flow did afterwards.
	if missing > 0 {
		os.Exit(3)
	}
}
