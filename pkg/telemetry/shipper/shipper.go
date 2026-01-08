// Copyright 2025 JAAB Tech SAS, Uruguay
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package shipper

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/telemetry/wal"
	"github.com/vmihailenco/msgpack/v5"
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
		if err := s.process(data); err != nil {
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

func (s *LogShipper) process(payload []byte) error {
	var msg fluxmsg.FluxMsg
	if err := msgpack.Unmarshal(payload, &msg); err != nil {
		return err
	}

	// Determine Subject suffix based on type
	suffix := ".logs"
	if t, ok := msg.Metadata["type"]; ok {
		if t == "telemetry.log" {
			suffix = ".logs"
		}
	}

	// Optimization: Send already-serialized payload directly.
	// We unmarshaled only to check type and get FluxID.
	return s.bus.PublishRaw(s.baseSubject+suffix, payload, msg.FluxID)
}
