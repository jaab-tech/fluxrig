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

package telemetry

import (
	"context"
	"log/slog"
	"testing"
)

type MockHandler struct {
	Records []slog.Record
}

func (h *MockHandler) Enabled(ctx context.Context, level slog.Level) bool { return true }
func (h *MockHandler) Handle(ctx context.Context, r slog.Record) error {
	h.Records = append(h.Records, r)
	return nil
}
func (h *MockHandler) WithAttrs(attrs []slog.Attr) slog.Handler { return h }
func (h *MockHandler) WithGroup(name string) slog.Handler       { return h }

func TestBufferHandler(t *testing.T) {
	mock := &MockHandler{}
	buf := NewBufferHandler(mock)

	// 1. Log to buffer
	ctx := context.Background()
	logger := slog.New(buf)
	logger.Info("Message 1", "key", "val")

	// Default pass-through behavior?
	// BufferHandler calls h.next.Handle. So mock receives it immediately.
	if len(mock.Records) != 1 {
		t.Errorf("Expected pass-through, got %d", len(mock.Records))
	}

	// 2. Check Buffer content
	if len(buf.buffer.records) != 1 {
		t.Errorf("Buffer empty")
	}

	// 3. Flush
	target := &MockHandler{}
	if err := buf.FlushTo(ctx, target); err != nil {
		t.Errorf("Flush failed: %v", err)
	}

	if len(target.Records) != 1 {
		t.Errorf("Target didn't receive flushed record")
	}

	// 4. Verify Clear
	if len(buf.buffer.records) != 0 {
		t.Errorf("Buffer not cleared")
	}

	// 5. WithAttrs / WithGroup coverage
	logger2 := logger.With("attr", "val").WithGroup("grp")
	logger2.Info("Message 2")
	// Should still go to same buffer pointer due to implementation
	// buf.WithAttrs returns new BufferHandler but shares *Buffer
}
