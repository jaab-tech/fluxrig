// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/moov-io/iso8583"

	"github.com/jaab-tech/fluxrig/pkg/gears/native/iso8583/codec/sdl"
)

// runAuthMode is a POS terminal for payment-switch validation. It sends one
// 0200 authorization with a configurable PAN (the BIN drives the switch's
// routing), reads the response on the same connection, and verifies the MTI
// and DE39. It exits non-zero if the expectation is not met, so a suite can
// assert each routing outcome. The wire format comes from the same SDL spec
// the codec gears load.
var (
	authTarget   = flag.String("auth-target", "127.0.0.1:8583", "Switch ingress address")
	authSpec     = flag.String("auth-spec", "", "Path to the SDL spec (required)")
	authPAN      = flag.String("auth-pan", "4111111111111111", "PAN (its BIN drives routing)")
	authSTAN     = flag.String("auth-stan", "000001", "System trace audit number")
	authExpMTI   = flag.String("auth-expect-mti", "0210", "Expected response MTI")
	authExpDE39  = flag.String("auth-expect-de39", "", "Expected DE39 (empty to skip)")
	authTimeout  = flag.Duration("auth-timeout", 5*time.Second, "Response timeout")
	authConns    = flag.Int("auth-conns", 1, "Concurrent terminals (distinct connections)")
	authCount    = flag.Int("auth-count", 1, "Transactions per terminal")
	authRate     = flag.Float64("auth-rate", 0, "Transactions per second per terminal (0 = as fast as possible)")
	authStanBase = flag.Int("auth-stan-base", 100000, "Base STAN for concurrent mode; give each terminal a distinct range so keys never collide")
	// A reversal reuses the trace number of the authorization it reverses, which
	// is normal in several dialects and is the reason the correlation key carries
	// the message class. Sending both classes over one trace range is what proves
	// the class is doing that work.
	authMTI    = flag.String("auth-mti", "0200", "Request MTI (0200 financial, 0400 reversal)")
	authDE43   = flag.String("auth-de43", "", "Acceptor location (DE 43); last two chars are the merchant country. Empty omits the field.")
	authMix    = flag.String("auth-mix", "", "Multi-scheme terminal: comma list of BIN:expected-de39 (e.g. 4:00,5:05). Each txn picks one at random; the reply's DE39 must match that BIN's expectation.")
	authAccept = flag.String("auth-accept-de39", "", "Chaos mode: comma list of ACCEPTABLE DE39 values (e.g. 00,91). A reply passes if its DE39 is any of these, instead of an exact match. STAN-echo (cross-wiring) is still enforced strictly.")
	authReport = flag.String("auth-report", "", "Write a JSON summary report to this path")
	authReconn = flag.Bool("auth-reconnect", false, "Terminal-side chaos: on a connection error, reconnect and continue (dropped txns are counted, not failed). Models a POS that survives an ingress blip.")
	authTermID = flag.String("auth-terminal-id", "", "DE41 terminal id. Empty = TERM0001 in single mode, or a per-terminal unique id in concurrent mode so the correlation key is globally unique across terminals (not just per-terminal STAN).")
)

// binExpect is one scheme a mixed-source terminal drives: a BIN prefix and the
// DE39 the switch is expected to return for it.
type binExpect struct{ prefix, de39 string }

// authRep is the JSON summary a terminal (or terminal fleet) writes with
// -auth-report, so a Robot suite can assert on structured results rather than
// scraping stdout.
type authRep struct {
	Terminals  int            `json:"terminals"`
	Count      int            `json:"count"`
	Total      int            `json:"total"`
	OK         int            `json:"ok"`
	CrossWired int            `json:"cross_wired"`
	Failed     int            `json:"failed"`
	Dropped    int            `json:"dropped"`
	Reconnects int            `json:"reconnects"`
	ByDE39     map[string]int `json:"by_de39"`
	// DE 42 carries whatever the host reported back, which is where a scheme
	// that moves a private field lands it. Aggregating it lets a suite assert
	// that a named outcome actually occurred, not merely that a reply arrived.
	ByDE42       map[string]int `json:"by_de42"`
	ElapsedSec   float64        `json:"elapsed_sec"`
	AchievedTPS  float64        `json:"achieved_tps"`
	LatencyP50Ms float64        `json:"latency_p50_ms"`
	LatencyP99Ms float64        `json:"latency_p99_ms"`
}

// aggregator collects per-transaction outcomes across all terminal goroutines.
type aggregator struct {
	mu        sync.Mutex
	byDE39    map[string]int
	byDE42    map[string]int
	latencies []float64 // milliseconds
}

func newAggregator() *aggregator {
	return &aggregator{byDE39: make(map[string]int), byDE42: make(map[string]int)}
}

func (a *aggregator) record(de39, de42 string, latencyMs float64) {
	a.mu.Lock()
	a.byDE39[de39]++
	if de42 != "" {
		a.byDE42[de42]++
	}
	a.latencies = append(a.latencies, latencyMs)
	a.mu.Unlock()
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p / 100 * float64(len(sorted)-1))
	return sorted[idx]
}

func runAuthMode() {
	if *authSpec == "" {
		log.Fatal("auth: -auth-spec is required")
	}
	spec, _, err := sdl.LoadSpec(*authSpec)
	if err != nil {
		log.Fatalf("auth: load spec: %v", err)
	}

	if *authConns <= 1 && *authCount <= 1 {
		runAuthSingle(spec)
		return
	}
	runAuthConcurrent(spec)
}

// runAuthSingle sends one transaction and checks MTI/DE39 (suite assertions).
func runAuthSingle(spec *iso8583.MessageSpec) {
	termID := *authTermID
	if termID == "" {
		termID = "TERM0001"
	}
	r, err := sendAuthNewConn(spec, *authTarget, *authPAN, *authSTAN, termID, *authTimeout)
	if err != nil {
		log.Fatalf("auth: %v", err)
	}
	fmt.Printf("pan=%s -> mti=%s de39=%s stan=%s\n", *authPAN, r.mti, r.de39, r.stan)
	if r.mti != *authExpMTI {
		fmt.Fprintf(os.Stderr, "FAIL: expected mti %s, got %s\n", *authExpMTI, r.mti)
		os.Exit(1)
	}
	if *authExpDE39 != "" && r.de39 != *authExpDE39 {
		fmt.Fprintf(os.Stderr, "FAIL: expected de39 %s, got %s\n", *authExpDE39, r.de39)
		os.Exit(1)
	}
}

// runAuthConcurrent runs N terminals in parallel, each sending count
// transactions with globally-unique STANs, and verifies that every response
// carries the STAN its own request sent. A STAN that does not match is a
// cross-wired reply: the switch returned one terminal's response to another.
func runAuthConcurrent(spec *iso8583.MessageSpec) {
	mix := parseMix(*authMix)
	accept := parseAccept(*authAccept) // nil unless chaos mode
	// STAN is DE11 (n6): guard against a base+range that would overflow 6 digits
	// and silently corrupt the correlation key.
	if hi := *authStanBase + *authConns**authCount; hi > 999999 {
		log.Fatalf("auth: stan range overflows n6 (base %d + conns*count %d = %d > 999999); lower -auth-stan-base/-auth-conns/-auth-count",
			*authStanBase, *authConns**authCount, hi)
	}
	agg := newAggregator()
	var wg sync.WaitGroup
	var ok, crossed, failed, dropped, reconnects atomic.Int64

	start := time.Now()
	for c := 0; c < *authConns; c++ {
		wg.Add(1)
		go func(connID int) {
			defer wg.Done()
			// One open connection per terminal, reused for its transactions.
			conn, err := net.DialTimeout("tcp", *authTarget, *authTimeout)
			if err != nil {
				if !*authReconn {
					failed.Add(int64(*authCount))
					return
				}
				conn = nil // reconnect mode: the loop will redial
			}
			defer func() {
				if conn != nil {
					_ = conn.Close()
				}
			}()

			// Per-terminal RNG for realistic, uncorrelated traffic.
			rng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(connID)*7919))

			// DE41 terminal id. Unless overridden, derive a value unique across
			// terminals AND processes (via the distinct STAN base), so the
			// correlation key is globally unique even when two terminals reuse
			// the same STAN, matching real acquirer behaviour.
			termID := *authTermID
			if termID == "" {
				termID = fmt.Sprintf("T%07d", *authStanBase+connID)
			}

			for j := 0; j < *authCount; j++ {
				// Poisson arrivals: exponential inter-arrival at the target
				// rate, so transactions interleave unpredictably across
				// terminals instead of marching in lockstep.
				if *authRate > 0 {
					gap := rng.ExpFloat64() / *authRate
					time.Sleep(time.Duration(gap * float64(time.Second)))
				}
				// Terminal-side chaos: if the connection was lost, try to come
				// back before sending. A round we can't reconnect drops its txn.
				if conn == nil {
					conn = redialWithBackoff(*authTarget, *authTimeout)
					if conn == nil {
						dropped.Add(1)
						continue
					}
					reconnects.Add(1)
				}
				// Each txn's scheme: a fixed PAN, or a random pick from the mix
				// so a single terminal fans out across schemes.
				prefix, expDE39 := *authPAN, *authExpDE39
				if len(mix) > 0 {
					m := mix[rng.Intn(len(mix))]
					prefix, expDE39 = m.prefix, m.de39
				}
				// Unique 6-digit STAN, >= base so no leading zero is stripped
				// and terminals never collide on the correlation key.
				stan := fmt.Sprintf("%06d", *authStanBase+connID*(*authCount)+j)
				pan := randomizePAN(rng, prefix)
				amount := fmt.Sprintf("%012d", rng.Intn(500000)+100) // 1.00 .. 5000.00
				sent := time.Now()
				r, err := sendAuthOnConn(conn, spec, pan, stan, amount, termID, *authTimeout)
				switch {
				case err != nil && *authReconn:
					// Connection blip: drop this txn's reply and reconnect on
					// the next round. NOT a correctness failure.
					dropped.Add(1)
					_ = conn.Close()
					conn = nil
				case err != nil:
					failed.Add(1)
					fmt.Fprintf(os.Stderr, "ERROR: stan=%s pan=%s: %v\n", stan, pan, err)
				case r.stan != stan:
					// The reply came back with someone else's trace number.
					crossed.Add(1)
					fmt.Fprintf(os.Stderr, "CROSS-WIRED: sent stan=%s got stan=%s (conn %d)\n", stan, r.stan, connID)
				case r.mti != *authExpMTI || !de39OK(r.de39, expDE39, accept):
					failed.Add(1)
					fmt.Fprintf(os.Stderr, "BAD RESPONSE: pan=%s stan=%s mti=%s de39=%s (want de39=%s)\n", pan, r.stan, r.mti, r.de39, expDE39)
					agg.record(r.de39, r.de42, time.Since(sent).Seconds()*1000)
				default:
					ok.Add(1)
					agg.record(r.de39, r.de42, time.Since(sent).Seconds()*1000)
				}
			}
		}(c)
	}
	wg.Wait()
	elapsed := time.Since(start).Seconds()

	total := *authConns * *authCount
	fmt.Printf("terminals=%d count=%d total=%d ok=%d cross_wired=%d failed=%d dropped=%d reconnects=%d elapsed=%.1fs tps=%.0f\n",
		*authConns, *authCount, total, ok.Load(), crossed.Load(), failed.Load(),
		dropped.Load(), reconnects.Load(), elapsed, float64(total)/elapsed)

	if *authReport != "" {
		writeAuthReport(agg, elapsed, total,
			int(ok.Load()), int(crossed.Load()), int(failed.Load()),
			int(dropped.Load()), int(reconnects.Load()))
	}
	// The hard invariants: no reply ever went to the wrong terminal, and every
	// reply we DID get was well-formed. Dropped txns (terminal-side chaos) are
	// expected and do not fail the process.
	if crossed.Load() > 0 || failed.Load() > 0 {
		os.Exit(1)
	}
}

// redialWithBackoff retries a TCP dial for a bounded time so a terminal can
// come back after an ingress blip. Returns nil if the target stays unreachable.
func redialWithBackoff(target string, timeout time.Duration) net.Conn {
	for i := 0; i < 20; i++ {
		if c, err := net.DialTimeout("tcp", target, timeout); err == nil {
			return c
		}
		time.Sleep(300 * time.Millisecond)
	}
	return nil
}

// de39OK decides whether a reply's DE39 is acceptable. In chaos mode (accept
// set non-empty) any member passes; otherwise an exact expDE39 match is required
// (empty expDE39 skips the check).
func de39OK(got, exp string, accept map[string]bool) bool {
	if len(accept) > 0 {
		return accept[got]
	}
	return exp == "" || got == exp
}

func parseAccept(s string) map[string]bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	m := make(map[string]bool)
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			m[p] = true
		}
	}
	return m
}

func writeAuthReport(agg *aggregator, elapsed float64, total, ok, crossed, failed, dropped, reconnects int) {
	agg.mu.Lock()
	lat := append([]float64(nil), agg.latencies...)
	byDE39 := make(map[string]int, len(agg.byDE39))
	for k, v := range agg.byDE39 {
		byDE39[k] = v
	}
	byDE42 := make(map[string]int, len(agg.byDE42))
	for k, v := range agg.byDE42 {
		byDE42[k] = v
	}
	agg.mu.Unlock()
	sort.Float64s(lat)

	rep := authRep{
		Terminals:    *authConns,
		Count:        *authCount,
		Total:        total,
		OK:           ok,
		CrossWired:   crossed,
		Failed:       failed,
		Dropped:      dropped,
		Reconnects:   reconnects,
		ByDE39:       byDE39,
		ByDE42:       byDE42,
		ElapsedSec:   elapsed,
		AchievedTPS:  float64(total) / elapsed,
		LatencyP50Ms: percentile(lat, 50),
		LatencyP99Ms: percentile(lat, 99),
	}
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		log.Printf("auth: report marshal: %v", err)
		return
	}
	if err := os.WriteFile(*authReport, data, 0o644); err != nil {
		log.Printf("auth: report write: %v", err)
	}
}

// parseMix parses "4:00,5:05" into scheme expectations. It fatals on a
// malformed entry so a suite never silently under-tests.
func parseMix(s string) []binExpect {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var out []binExpect
	for _, part := range strings.Split(s, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), ":", 2)
		if len(kv) != 2 || kv[0] == "" {
			log.Fatalf("auth: bad -auth-mix entry %q (want BIN:de39)", part)
		}
		out = append(out, binExpect{prefix: kv[0], de39: kv[1]})
	}
	return out
}

// randomizePAN keeps the routing prefix and fills the rest with random digits
// out to 16, so every card is distinct while the BIN still steers routing.
func randomizePAN(rng *rand.Rand, prefix string) string {
	const panLen = 16
	if len(prefix) >= panLen {
		return prefix[:panLen]
	}
	b := []byte(prefix)
	for len(b) < panLen {
		b = append(b, byte('0'+rng.Intn(10)))
	}
	return string(b)
}

type authResult struct{ mti, de39, stan, de42 string }

func sendAuthNewConn(spec *iso8583.MessageSpec, target, pan, stan, termID string, timeout time.Duration) (authResult, error) {
	conn, err := net.DialTimeout("tcp", target, timeout)
	if err != nil {
		return authResult{}, fmt.Errorf("dial %s: %w", target, err)
	}
	defer func() { _ = conn.Close() }()
	return sendAuthOnConn(conn, spec, pan, stan, "000000010000", termID, timeout)
}

// sendAuthOnConn sends one 0200 on an existing connection and reads its reply.
func sendAuthOnConn(conn net.Conn, spec *iso8583.MessageSpec, pan, stan, amount, termID string, timeout time.Duration) (authResult, error) {
	req := iso8583.NewMessage(spec)
	set(req, 0, *authMTI)
	set(req, 2, pan)
	set(req, 3, "000000")
	set(req, 4, amount)
	set(req, 7, time.Now().UTC().Format("0102150405"))
	set(req, 11, stan)
	set(req, 41, termID)
	if *authDE43 != "" {
		set(req, 43, fitField(spec, 43, *authDE43))
	}
	set(req, 49, "858") // ISO 4217 UYU, matching the load generator and the UY scenario
	packed, err := req.Pack()
	if err != nil {
		return authResult{}, fmt.Errorf("pack: %w", err)
	}
	if _, werr := conn.Write(frameFor(packed)); werr != nil {
		return authResult{}, fmt.Errorf("write: %w", werr)
	}
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	respBytes, err := readFramed(conn)
	if err != nil {
		return authResult{}, fmt.Errorf("no response (pan=%s stan=%s): %w", pan, stan, err)
	}
	resp := iso8583.NewMessage(spec)
	if err := resp.Unpack(respBytes); err != nil {
		return authResult{}, fmt.Errorf("unpack: %w", err)
	}
	return authResult{mti: fieldStr(resp, 0), de39: fieldStr(resp, 39), stan: fieldStr(resp, 11), de42: fieldStr(resp, 42)}, nil
}

func set(m *iso8583.Message, id int, v string) {
	if err := m.Field(id, v); err != nil {
		log.Fatalf("auth: set field %d: %v", id, err)
	}
}

func fieldStr(m *iso8583.Message, id int) string {
	f := m.GetField(id)
	if f == nil {
		return ""
	}
	s, _ := f.String()
	return s
}
