// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"time"

	"github.com/HdrHistogram/hdrhistogram-go"
	"github.com/moov-io/iso8583"
	"github.com/moov-io/iso8583/encoding"
	"github.com/moov-io/iso8583/field"
	"github.com/moov-io/iso8583/padding"
	"github.com/moov-io/iso8583/prefix"
	"golang.org/x/time/rate"
)

var (
	targetAddr       = flag.String("target", "localhost:8583", "Target ISO8583 server address")
	concurrency      = flag.Int("concurrency", 10, "Number of parallel TCP connections")
	tps              = flag.Int("rate", 100, "Target total Transactions Per Second")
	duration         = flag.Duration("duration", 30*time.Second, "Test duration")
	encodingFlag     = flag.String("encoding", "bcd", "Encoding: ascii or bcd")
	endian           = flag.String("endian", "big", "Endianness: big or little")
	framingBytes     = flag.Int("framing-bytes", 2, "Length prefix size in bytes (2 or 4)")
	correlationBytes = flag.Int("header-len", 12, "Correlation header length in bytes (min 8 for timestamp, 0 to disable)")
	staticHeaderVal  = flag.String("header-val", "", "Static header hex value (overrides -header-len, disables RTT)")
	warmupDuration   = flag.Duration("warmup", 0, "Warmup duration before measurement (e.g. 2s)")
	reportFile       = flag.String("report", "report.json", "JSON report output file")
)

// MetricEvent represents a single metric data point for aggregation
type MetricEvent struct {
	Type      string // "sent", "recv"
	LatencyUs int64
}

// Collector manages time-series aggregation
type Collector struct {
	buckets map[int64]*TimeBucketWrapper
	mu      sync.Mutex
}

type TimeBucketWrapper struct {
	Bucket TimeBucket
	Hist   *hdrhistogram.Histogram
}

func NewCollector() *Collector {
	return &Collector{
		buckets: make(map[int64]*TimeBucketWrapper),
	}
}

func (c *Collector) Record(evt MetricEvent) {
	now := time.Now().Unix()
	c.mu.Lock()
	defer c.mu.Unlock()

	b, ok := c.buckets[now]
	if !ok {
		b = &TimeBucketWrapper{
			Bucket: TimeBucket{Timestamp: now},
			Hist:   hdrhistogram.New(1, 1000000, 2),
		}
		c.buckets[now] = b
	}

	if evt.Type == "sent" {
		b.Bucket.ReqSent++
	} else if evt.Type == "recv" {
		b.Bucket.RespRecv++
		if evt.LatencyUs > 0 {
			_ = b.Hist.RecordValue(evt.LatencyUs)
		}
	}
}

func (c *Collector) GetTimeSeries() []TimeBucket {
	c.mu.Lock()
	defer c.mu.Unlock()

	series := make([]TimeBucket, 0, len(c.buckets))
	for _, w := range c.buckets {
		// Finalize Latencies
		w.Bucket.LatencyP50Ms = float64(w.Hist.ValueAtQuantile(50)) / 1000.0
		w.Bucket.LatencyP99Ms = float64(w.Hist.ValueAtQuantile(99)) / 1000.0
		w.Bucket.LatencyMaxMs = float64(w.Hist.Max()) / 1000.0
		series = append(series, w.Bucket)
	}
	// Sort by timestamp? Not strictly necessary if renderer handles it, but good practice.
	// (Skipping sort for brevity, Robot/DuckDB can sort)
	return series
}

type Stats struct {
	ReqSent    int64 `json:"req_sent"`
	ReqFailed  int64 `json:"req_failed"`
	RespRecv   int64 `json:"resp_recv"`
	RespFailed int64 `json:"resp_failed"` // Actual read errors (connection closed, etc.)
	RTTSuccess int64 `json:"rtt_success"` // Responses with valid RTT measurement
	TotalBytes int64 `json:"total_bytes"`

	LatencyMinMs  float64 `json:"latency_min_ms"`
	LatencyMaxMs  float64 `json:"latency_max_ms"`
	LatencyP50Ms  float64 `json:"latency_p50_ms"`
	LatencyP90Ms  float64 `json:"latency_p90_ms"`
	LatencyP99Ms  float64 `json:"latency_p99_ms"`
	LatencyP999Ms float64 `json:"latency_p999_ms"`

	TPS         float64 `json:"actual_tps"` // Based on Sent
	RPS         float64 `json:"actual_rps"` // Based on Recv
	DurationSec float64 `json:"duration_sec"`

	ConfiguredTPS   int   `json:"configured_tps"`
	ConfiguredConns int   `json:"configured_conns"`
	ConnectedConns  int64 `json:"connected_conns"`

	TimeSeries  []TimeBucket      `json:"time_series"`
	Connections []ConnectionStats `json:"connections"` // Per-connection breakdown
}

// ConnectionStats tracks metrics for a single TCP connection/worker
type ConnectionStats struct {
	ConnID       int     `json:"conn_id"`
	ReqSent      int64   `json:"req_sent"`
	RespRecv     int64   `json:"resp_recv"`
	RespFailed   int64   `json:"resp_failed"`
	RTTSuccess   int64   `json:"rtt_success"`
	LatencyP50Ms float64 `json:"latency_p50_ms"`
	LatencyP99Ms float64 `json:"latency_p99_ms"`
	LatencyMaxMs float64 `json:"latency_max_ms"`
}

type TimeBucket struct {
	Timestamp    int64   `json:"timestamp"` // Unix epoch seconds
	ReqSent      int64   `json:"req_sent"`
	RespRecv     int64   `json:"resp_recv"`
	LatencyP50Ms float64 `json:"latency_p50_ms"`
	LatencyP99Ms float64 `json:"latency_p99_ms"`
	LatencyMaxMs float64 `json:"latency_max_ms"`
}

func runLoadMode() {
	// Log to stdout (not stderr) so Robot Framework doesn't show as ERROR
	log.SetOutput(os.Stdout)

	log.Printf("Starting ISO8583 Load Generator (Stateless)")
	log.Printf("Target: %s, Concurrency: %d, Rate: %d TPS, Duration: %s", *targetAddr, *concurrency, *tps, *duration)
	log.Printf("Configuration: Encoding=%s, Header=%d bytes (Timestamp), Framing=%d bytes", *encodingFlag, *correlationBytes, *framingBytes)

	// Configuration Logic
	var rttEnabled bool
	var staticHeader []byte

	if *staticHeaderVal != "" {
		var err error
		staticHeader, err = hex.DecodeString(*staticHeaderVal)
		if err != nil {
			log.Fatalf("Invalid -header-val hex: %v", err)
		}
		*correlationBytes = len(staticHeader)
		log.Printf("Using Static Header (%d bytes). RTT Disabled.", len(staticHeader))
	} else {
		if *correlationBytes >= 8 {
			rttEnabled = true
			log.Printf("Using Timestamp Header (%d bytes). RTT Enabled.", *correlationBytes)
		} else if *correlationBytes > 0 {
			log.Printf("Using Zero-Pad Header (%d bytes). RTT Disabled (too short).", *correlationBytes)
		} else {
			log.Printf("No Header. RTT Disabled.")
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), *duration+*warmupDuration)
	defer cancel()

	// Handle signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)
	go func() {
		<-sigChan
		cancel()
	}()

	// Initialize Metrics
	hist := hdrhistogram.New(1, 1000000, 3) // 1ms to 1000s, 3 sig figs
	var reqSent, reqFailed, respRecv, totalBytes, connectedCount atomic.Int64

	// Global Rate Limiter
	limiter := rate.NewLimiter(rate.Limit(*tps), 1)

	// Time Series Collector
	collector := NewCollector()

	// Build Template Spec (0800 Echo)
	spec := getEchoSpec()

	// Warmup Phase (if configured)
	if *warmupDuration > 0 {
		log.Printf("Starting Warmup Phase (%s)...", *warmupDuration)
		warmupCtx, warmupCancel := context.WithTimeout(ctx, *warmupDuration)

		var warmupWg sync.WaitGroup
		for i := 0; i < *concurrency; i++ {
			warmupWg.Add(1)
			go func(id int) {
				defer warmupWg.Done()
				// Use separate counters for warmup (discarded)
				var wuReqSent, wuReqFailed, wuRespRecv, wuBytes, wuConns atomic.Int64
				wuHist := hdrhistogram.New(1, 1000000, 3)
				wuCollector := NewCollector()
				runWorker(warmupCtx, id, limiter, spec, staticHeader, rttEnabled, wuHist, wuCollector, &wuReqSent, &wuReqFailed, &wuRespRecv, &wuBytes, &wuConns, nil)
			}(i)
		}
		warmupWg.Wait()
		warmupCancel()
		log.Printf("Warmup Complete. Starting Measurement...")
	}

	var wg sync.WaitGroup
	start := time.Now()

	// Results channel for per-connection stats
	resultsChan := make(chan ConnectionStats, *concurrency)

	for i := 0; i < *concurrency; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			runWorker(ctx, id, limiter, spec, staticHeader, rttEnabled, hist, collector, &reqSent, &reqFailed, &respRecv, &totalBytes, &connectedCount, resultsChan)
		}(i)
	}

	wg.Wait()
	close(resultsChan)
	elapsed := time.Since(start)

	// Collect per-connection stats
	var connections []ConnectionStats
	var totalRTTSuccess, totalRespFailed int64
	for cs := range resultsChan {
		connections = append(connections, cs)
		totalRTTSuccess += cs.RTTSuccess
		totalRespFailed += cs.RespFailed
	}

	// Save Report
	report := Stats{
		ReqSent:    reqSent.Load(),
		ReqFailed:  reqFailed.Load(),
		RespRecv:   respRecv.Load(),
		RespFailed: totalRespFailed,
		RTTSuccess: totalRTTSuccess,
		TotalBytes: totalBytes.Load(),

		LatencyMinMs:  float64(hist.Min()) / 1000.0,
		LatencyMaxMs:  float64(hist.Max()) / 1000.0,
		LatencyP50Ms:  float64(hist.ValueAtQuantile(50)) / 1000.0,
		LatencyP90Ms:  float64(hist.ValueAtQuantile(90)) / 1000.0,
		LatencyP99Ms:  float64(hist.ValueAtQuantile(99)) / 1000.0,
		LatencyP999Ms: float64(hist.ValueAtQuantile(99.9)) / 1000.0,
		TPS:           float64(reqSent.Load()) / elapsed.Seconds(),
		RPS:           float64(respRecv.Load()) / elapsed.Seconds(),
		DurationSec:   elapsed.Seconds(),

		ConfiguredTPS:   *tps,
		ConfiguredConns: *concurrency,
		ConnectedConns:  connectedCount.Load(),

		// Detailed data
		TimeSeries:  collector.GetTimeSeries(),
		Connections: connections,
	}

	jsonData, _ := json.MarshalIndent(report, "", "  ")
	//nolint:gosec // 0644 is intended for report file readability, but lowering to 0600 to satisfy linter
	_ = os.WriteFile(*reportFile, jsonData, 0600)

	fmt.Println("\n--- Test Results ---")
	fmt.Printf("Requests Sent:    %d\n", report.ReqSent)
	fmt.Printf("Requests Failed:  %d\n", report.ReqFailed)
	fmt.Printf("Responses Recv:   %d\n", report.RespRecv)
	fmt.Printf("Responses Failed: %d\n", report.RespFailed)
	fmt.Printf("Send TPS:         %.2f\n", report.TPS)
	fmt.Printf("Recv RPS:         %.2f\n", report.RPS)
	fmt.Printf("Connections:      %d/%d\n", report.ConnectedConns, report.ConfiguredConns)
	fmt.Printf("Latency (ms):     P50: %.2f, P99: %.2f, Max: %.2f\n", report.LatencyP50Ms, report.LatencyP99Ms, report.LatencyMaxMs)
	fmt.Printf("Report saved to: %s\n", *reportFile)
}

// Worker manages a single TCP connection with async Read/Write loops
type Worker struct {
	id      int
	conn    net.Conn
	limiter *rate.Limiter
	spec    *iso8583.MessageSpec

	// Config
	staticHeader []byte
	rttEnabled   bool

	// Per-connection stats (local to this worker)
	localHist       *hdrhistogram.Histogram
	localReqSent    atomic.Int64
	localReqFailed  atomic.Int64
	localRespRecv   atomic.Int64
	localRespFailed atomic.Int64
	localRTTSuccess atomic.Int64
	localBytes      atomic.Int64

	// Shared/global stats (for aggregation)
	globalHist      *hdrhistogram.Histogram
	collector       *Collector
	globalReqSent   *atomic.Int64
	globalReqFailed *atomic.Int64
	globalRespRecv  *atomic.Int64
	globalBytes     *atomic.Int64
	globalConnected *atomic.Int64
}

func runWorker(ctx context.Context, id int, limiter *rate.Limiter, spec *iso8583.MessageSpec, staticHeader []byte, rttEnabled bool, hist *hdrhistogram.Histogram, collector *Collector, reqSent, reqFailed, respRecv, bytes, connected *atomic.Int64, resultsChan chan<- ConnectionStats) {
	conn, err := net.DialTimeout("tcp", *targetAddr, 5*time.Second)
	if err != nil {
		log.Printf("Worker %d: Dial failed: %v", id, err)
		return
	}
	defer func() { _ = conn.Close() }()
	connected.Add(1)

	w := &Worker{
		id:              id,
		conn:            conn,
		limiter:         limiter,
		spec:            spec,
		staticHeader:    staticHeader,
		rttEnabled:      rttEnabled,
		localHist:       hdrhistogram.New(1, 1000000, 3), // Per-connection histogram
		globalHist:      hist,
		collector:       collector,
		globalReqSent:   reqSent,
		globalReqFailed: reqFailed,
		globalRespRecv:  respRecv,
		globalBytes:     bytes,
		globalConnected: connected,
	}

	// Split into Reader and Writer
	var wg sync.WaitGroup
	wg.Add(2)

	// Writer Loop
	go func() {
		defer wg.Done()
		w.writeLoop(ctx)
	}()

	// Reader Loop
	go func() {
		defer wg.Done()
		w.readLoop(ctx)
	}()

	wg.Wait()

	// Send per-connection stats to results channel
	if resultsChan != nil {
		resultsChan <- ConnectionStats{
			ConnID:       id,
			ReqSent:      w.localReqSent.Load(),
			RespRecv:     w.localRespRecv.Load(),
			RespFailed:   w.localRespFailed.Load(),
			RTTSuccess:   w.localRTTSuccess.Load(),
			LatencyP50Ms: float64(w.localHist.ValueAtQuantile(50)) / 1000.0,
			LatencyP99Ms: float64(w.localHist.ValueAtQuantile(99)) / 1000.0,
			LatencyMaxMs: float64(w.localHist.Max()) / 1000.0,
		}
	}
}

func (w *Worker) writeLoop(ctx context.Context) {
	// Initialize Local STAN counter (avoid collision between workers by offsetting)
	// We give each worker a 100,000 range, wrapping around 999999
	//nolint:gosec // id is small enough
	stan := uint32(w.id * 100000 % 1000000)

	// Initialize Message once
	msg := iso8583.NewMessage(w.spec)
	msg.MTI("0800")
	if err := msg.Field(70, "301"); err != nil { // 301 = Echo Test
		log.Printf("Worker %d: Failed to set Field 70: %v", w.id, err)
		w.localReqFailed.Add(1)
		w.globalReqFailed.Add(1)
		return
	}

	for {
		if err := w.limiter.Wait(ctx); err != nil {
			return // Done
		}

		// Update STAN
		stan = (stan + 1) % 1000000
		stanStr := fmt.Sprintf("%06d", stan)

		// Update Dynamic Fields
		if err := msg.Field(11, stanStr); err != nil {
			w.localReqFailed.Add(1)
			w.globalReqFailed.Add(1)
			continue
		}
		if err := msg.Field(7, time.Now().UTC().Format("0102150405")); err != nil {
			w.localReqFailed.Add(1)
			w.globalReqFailed.Add(1)
			continue
		}

		// Pack Message using library
		packed, err := msg.Pack()
		if err != nil {
			w.localReqFailed.Add(1)
			w.globalReqFailed.Add(1)
			continue
		}

		// Stateless Timestamp Header
		// Construct Payload: [Header(Timestamp+ConnID+Padding)][Body]
		// Header Format: [8-byte timestamp][2-byte conn_id][2-byte padding]
		fullPayload := make([]byte, *correlationBytes+len(packed))

		// Fill Header
		if w.staticHeader != nil {
			copy(fullPayload[0:], w.staticHeader)
		} else if w.rttEnabled {
			// Write Timestamp (Nanoseconds, Big Endian) into first 8 bytes
			nowNs := time.Now().UnixNano()
			//nolint:gosec // timestamp is positive
			binary.BigEndian.PutUint64(fullPayload[0:], uint64(nowNs))
			// Write Connection ID (2 bytes, Big Endian) into bytes 8-9
			if *correlationBytes >= 10 {
				//nolint:gosec // id fits in uint16
				binary.BigEndian.PutUint16(fullPayload[8:], uint16(w.id))
			}
		}

		// Copy Packed ISO Message
		copy(fullPayload[*correlationBytes:], packed)

		frame := buildFrame(fullPayload)
		if _, err := w.conn.Write(frame); err != nil {
			// If we can't write, likely connection issue.
			// We count error and exit loop (which closes connection).
			w.localReqFailed.Add(1)
			w.globalReqFailed.Add(1)
			return
		}
		w.localReqSent.Add(1)
		w.globalReqSent.Add(1)
		w.collector.Record(MetricEvent{Type: "sent"})
	}
}

func (w *Worker) readLoop(ctx context.Context) {
	headerBuf := make([]byte, *framingBytes)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Set Read Deadline
		_ = w.conn.SetReadDeadline(time.Now().Add(1 * time.Second))

		// 1. Read Length Prefix
		if _, err := io.ReadFull(w.conn, headerBuf); err != nil {
			if ctx.Err() != nil {
				return // Normal shutdown
			}
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				// Timeout is normal - just continue polling for responses
				continue
			}
			// Actual read error (connection closed, etc.)
			w.localRespFailed.Add(1)
			return
		}

		var length int
		if *framingBytes == 2 {
			if *endian == "little" {
				//nolint:gosec // payload len fits
				length = int(binary.LittleEndian.Uint16(headerBuf))
			} else {
				//nolint:gosec // payload len fits
				length = int(binary.BigEndian.Uint16(headerBuf))
			}
		} else {
			if *endian == "little" {
				//nolint:gosec // payload len fits
				length = int(binary.LittleEndian.Uint32(headerBuf))
			} else {
				//nolint:gosec // payload len fits
				length = int(binary.BigEndian.Uint32(headerBuf))
			}
		}

		// 2. Read All Body
		body := make([]byte, length)
		if _, err := io.ReadFull(w.conn, body); err != nil {
			w.localRespFailed.Add(1)
			return
		}

		// 3. Extract Stateless Timestamp
		if w.rttEnabled && len(body) >= 8 {
			ts := binary.BigEndian.Uint64(body[:8])
			//nolint:gosec // timestamp logic safe
			t0 := time.Unix(0, int64(ts))

			// Latency Calculation
			latency := time.Since(t0).Microseconds()

			// Record to both local and global histograms
			if latency > 0 {
				_ = w.localHist.RecordValue(latency)
				_ = w.globalHist.RecordValue(latency)
				w.collector.Record(MetricEvent{Type: "recv", LatencyUs: latency})
				w.localRTTSuccess.Add(1)
			}
		} else {
			// Even if no RTT or short header, record recv count
			w.collector.Record(MetricEvent{Type: "recv", LatencyUs: 0})
		}
		w.localRespRecv.Add(1)
		w.globalRespRecv.Add(1)
		w.localBytes.Add(int64(length + *framingBytes))
		w.globalBytes.Add(int64(length + *framingBytes))
	}
}

func getEchoSpec() *iso8583.MessageSpec {
	// Determine Encoder
	var enc encoding.Encoder = encoding.ASCII
	if *encodingFlag == "bcd" {
		enc = encoding.BCD
	}

	return &iso8583.MessageSpec{
		Name: "Echo",
		Fields: map[int]field.Field{
			0: field.NewString(&field.Spec{
				Length:      4,
				Description: "MTI",
				Enc:         enc,
				Pref:        prefix.None.Fixed,
			}),
			1: field.NewBitmap(&field.Spec{
				Description: "Bitmap",
				Enc:         encoding.Binary,
				Pref:        prefix.None.Fixed,
			}),
			7: field.NewString(&field.Spec{
				Length:      10,
				Description: "Transmission Date & Time",
				Enc:         enc,
				Pref:        prefix.None.Fixed,
				Pad:         padding.NewLeftPadder('0'),
			}),
			11: field.NewString(&field.Spec{
				Length:      6,
				Description: "Systems Trace Audit Number",
				Enc:         enc,
				Pref:        prefix.None.Fixed,
				Pad:         padding.NewLeftPadder('0'),
			}),
			70: field.NewString(&field.Spec{
				Length:      3,
				Description: "Network Management Information Code",
				Enc:         enc,
				Pref:        prefix.None.Fixed,
				Pad:         padding.NewLeftPadder('0'),
			}),
		},
	}
}

func buildFrame(payload []byte) []byte {
	buf := make([]byte, *framingBytes+len(payload))
	if *framingBytes == 2 {
		if *endian == "little" {
			//nolint:gosec // payload len fits
			binary.LittleEndian.PutUint16(buf[0:], uint16(len(payload)))
		} else {
			//nolint:gosec // payload len fits
			binary.BigEndian.PutUint16(buf[0:], uint16(len(payload)))
		}
	} else {
		if *endian == "little" {
			//nolint:gosec // payload len fits
			binary.LittleEndian.PutUint32(buf[0:], uint32(len(payload)))
		} else {
			//nolint:gosec // payload len fits
			binary.BigEndian.PutUint32(buf[0:], uint32(len(payload)))
		}
	}
	copy(buf[*framingBytes:], payload)
	return buf
}
