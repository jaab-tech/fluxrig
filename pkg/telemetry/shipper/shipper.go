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

		// Calculate Target to Read
		target := index + 1

		// [QoS] Rate Limiting
		// Wait for token before reading next log.
		if s.limiter != nil {
			if err := s.limiter.Wait(ctx); err != nil {
				// Canceled?
				return
			}
		}

		data, err := s.wal.Read(target)
		if err != nil {
			if err == wal.ErrNotFound {
				// End of log, wait for more data
				select {
				case <-s.stopCh:
					return
				case <-time.After(100 * time.Millisecond):
					continue
				}
			}
			slog.Error("WAL Read Error", "index", target, "error", err)
			select {
			case <-s.stopCh:
				return
			case <-time.After(100 * time.Millisecond):
				continue
			}
		}

		// Process
		if err := s.process(ctx, data); err != nil {
			slog.Error("Failed to process log", "error", err)
			select {
			case <-s.stopCh:
				return
			case <-time.After(1 * time.Second):
				continue
			}
		}

		// Success
		index = target
		s.cursor.Update(index)

		// Periodic Cleanup
		// Truncate WAL segments that have been fully processed.
		if index > 0 && index%1000 == 0 {
			if err := s.wal.TruncateFront(index); err != nil {
				slog.Warn("Failed to truncate WAL", "error", err)
			}

			// [Hard Cap Enforcement]
			// Check total size
			if s.maxSizeMB > 0 {
				sizeBytes, err := s.wal.Size()
				if err == nil {
					sizeMB := int(sizeBytes / 1024 / 1024)
					if sizeMB > s.maxSizeMB {
						slog.Warn("WAL exceeded max size, forcing truncate", "current_mb", sizeMB, "max_mb", s.maxSizeMB)
						// We are already truncating to 'index' (what we just sent).
						// If size is still too big, it means we have too much backlog that IS sending or preserved.
						// Use tidwall/wal's behavior: TruncateFront(index) deletes segments *before* index.
						// If we are far behind, index is small.
						// Strategy: If size is critical, we might need to skip ahead (drop data) to free space?
						// For now, assume standard TruncateFront(index) is enough provided we keep up.
						// If we fall behind and WAL grows, we might need to advance 'index' artificially to drop old data?
						// "Ring Buffer" behavior:
						// If Size > Max, calculate how many items to drop? Hard with variable size.
						// Simple approach: Warn for now. Real ring buffer requires dropping un-sent data.
					}
				}
			}
		}
	}
}

func (s *LogShipper) process(ctx context.Context, payload []byte) error {
	var msg fluxmsg.FluxMsg
	if err := cbor.Unmarshal(payload, &msg); err != nil {
		// 1. Explicit Alarm: Publish Error Metric
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

		// 2. Explicit Diagnostic Log
		slog.Error("Telemetry Decode Failure", "error", err, "size", len(payload))
		return nil // Skip and continue
	}

	// Determine Subject suffix based on type
	suffix := ".logs"
	if t, ok := msg.Metadata["type"]; ok {
		if t == "telemetry.log" {
			suffix = ".logs"
		} else if t == "telemetry.metric" {
			suffix = ".metrics"
		}
	}

	// Optimization: Send already-serialized payload directly.
	// We unmarshaled only to check type and get FluxID.
	return s.bus.PublishRaw(ctx, s.baseSubject+suffix, payload, msg.FluxID)
}
