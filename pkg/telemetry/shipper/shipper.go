// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package shipper

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/fxamacker/cbor/v2"
	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/telemetry/wal"
)

// LogShipper tails the WAL and ships logs to the bus.
type LogShipper struct {
	bus         bus.Bus
	wal         *wal.WAL
	cursor      *Cursor
	baseSubject string
	maxSizeMB   int
	limiter     *rate.Limiter // Token Bucket for QoS
	stopCh      chan struct{}
	doneCh      chan struct{}
	stopOnce    sync.Once
}

// NewLogShipper creates a new shipper instance.
func NewLogShipper(b bus.Bus, w *wal.WAL, cursor *Cursor, baseSubject string, maxSizeMB int, throttleRate float64, throttleBurst int) *LogShipper {
	var lim *rate.Limiter
	if throttleRate > 0 && throttleBurst > 0 {
		lim = rate.NewLimiter(rate.Limit(throttleRate), throttleBurst)
	}

	return &LogShipper{
		bus:         b,
		wal:         w,
		cursor:      cursor,
		baseSubject: baseSubject,
		maxSizeMB:   maxSizeMB,
		limiter:     lim,
		stopCh:      make(chan struct{}),
		doneCh:      make(chan struct{}),
	}
}

// Start spawns the shipping loop.
func (s *LogShipper) Start() {
	go s.loop()
}

// Stop halts the shipper and performs a final cursor save.
func (s *LogShipper) Stop() {
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
	<-s.doneCh
	_ = s.cursor.Save()
}

func (s *LogShipper) loop() {
	defer close(s.doneCh)

	// Save cursor periodically to avoid excessive IO
	saveTicker := time.NewTicker(1 * time.Second)
	defer saveTicker.Stop()

	// Initial index (stored in cursor)
	index := s.cursor.Get()
	if index == 0 {
		// Optimization: If starting fresh, check if WAL has preserved logs starting later
		if first, err := s.wal.FirstIndex(); err == nil && first > 1 {
			slog.Info("WAL Truncated, fast-forwarding cursor", "first_index", first)
			index = first - 1
			s.cursor.Update(index)
		}
	}

	// Create a cancellable context from stopCh
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-s.stopCh
		cancel()
	}()

	for {
		select {
		case <-s.stopCh:
			return
		case <-saveTicker.C:
			if err := s.cursor.Save(); err != nil {
				slog.Error("Failed to save cursor", "error", err)
			}
		default:
		}

		// Read the entries that follow, at most one batch. Sent one at a
		// time, each waiting for the stream, a Rack across a slow link
		// shipped a few lines a second and fell behind its own log for good.
		max := shipBatch
		if s.limiter != nil && s.limiter.Burst() < max {
			max = s.limiter.Burst()
		}
		var entries [][]byte
		var readErr error
		for len(entries) < max {
			data, err := s.wal.Read(index + uint64(len(entries)) + 1)
			if err != nil {
				readErr = err
				break
			}
			entries = append(entries, data)
		}
		if len(entries) == 0 {
			if readErr != nil && readErr != wal.ErrNotFound {
				slog.Error("WAL Read Error", "index", index+1, "error", readErr)
			}
			// End of log, wait for more data
			select {
			case <-s.stopCh:
				return
			case <-time.After(100 * time.Millisecond):
				continue
			}
		}

		// [QoS] Rate Limiting: a token per entry.
		if s.limiter != nil {
			if err := s.limiter.WaitN(ctx, len(entries)); err != nil {
				return // canceled
			}
		}

		sent, err := s.ship(ctx, entries)
		if sent > 0 {
			before := index
			index += uint64(sent) //nolint:gosec // at most one batch
			s.cursor.Update(index)
			// Periodic Cleanup: truncate the WAL segments fully processed,
			// each time the cursor passes a thousand.
			if index/1000 > before/1000 {
				s.cleanup(index)
			}
		}
		if err != nil {
			slog.Error("Failed to process log", "error", err)
			select {
			case <-s.stopCh:
				return
			case <-time.After(1 * time.Second):
				continue
			}
		}
	}
}

// shipBatch bounds the WAL entries one round sends.
const shipBatch = 64

// ship sends entries in order, consecutive entries of one subject in one
// batch, and returns how many of the first ones were handled: sent, or
// skipped as undecodable.
func (s *LogShipper) ship(ctx context.Context, entries [][]byte) (int, error) {
	handled := 0
	var run []bus.RawMessage
	runSubject := ""
	flush := func() error {
		if len(run) == 0 {
			return nil
		}
		n, err := bus.PublishRawBatch(ctx, s.bus, runSubject, run)
		handled += n
		run = run[:0]
		return err
	}
	for _, payload := range entries {
		subject, id, ok := s.route(ctx, payload)
		if !ok {
			// Undecodable: reported by route, and skipped in its place.
			if err := flush(); err != nil {
				return handled, err
			}
			handled++
			continue
		}
		if subject != runSubject {
			if err := flush(); err != nil {
				return handled, err
			}
			runSubject = subject
		}
		run = append(run, bus.RawMessage{Data: payload, ID: id})
	}
	return handled, flush()
}

// cleanup truncates the WAL before index, and warns when it is still over
// its size.
func (s *LogShipper) cleanup(index uint64) {
	if err := s.wal.TruncateFront(index); err != nil {
		slog.Warn("Failed to truncate WAL", "error", err)
	}
	if s.maxSizeMB <= 0 {
		return
	}
	if sizeBytes, err := s.wal.Size(); err == nil {
		if sizeMB := int(sizeBytes / 1024 / 1024); sizeMB > s.maxSizeMB {
			slog.Warn("WAL exceeded max size, forcing truncate", "current_mb", sizeMB, "max_mb", s.maxSizeMB)
		}
	}
}

// route returns the subject and the ID of a WAL entry. An entry that does not
// decode is reported, with a metric and a log, and ok is false.
func (s *LogShipper) route(ctx context.Context, payload []byte) (subject string, id uuid.UUID, ok bool) {
	var msg fluxmsg.FluxMsg
	if err := cbor.Unmarshal(payload, &msg); err != nil {
		metricMsg := fluxmsg.New()
		metricMsg.Metadata["type"] = "telemetry.metric"
		metricMsg.Metadata["metric.name"] = "fluxrig_telemetry_decode_failures_total"
		metricMsg.Data = map[string]any{
			"name":  "fluxrig_telemetry_decode_failures_total",
			"value": 1.0,
			"error": err.Error(),
		}
		// QoS: Optional/Best effort
		_ = s.bus.Publish(ctx, s.baseSubject+".metrics", metricMsg)
		slog.Error("Telemetry Decode Failure", "error", err, "size", len(payload))
		return "", uuid.Nil, false
	}
	suffix := ".logs"
	if t, ok := msg.Metadata["type"]; ok && t == "telemetry.metric" {
		suffix = ".metrics"
	}
	return s.baseSubject + suffix, msg.FluxID, true
}
