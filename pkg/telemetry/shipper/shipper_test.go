package shipper

import (
	"context"
	"testing"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/telemetry/wal"
)

type mockBus struct {
	published []*fluxmsg.FluxMsg
}

func (m *mockBus) Connect(url, name string, timeout, retryWait time.Duration) error { return nil }
func (m *mockBus) Close()                                                           {}
func (m *mockBus) Publish(subject string, msg *fluxmsg.FluxMsg) error {
	m.published = append(m.published, msg)
	return nil
}
func (m *mockBus) PublishWithContext(ctx context.Context, subject string, msg *fluxmsg.FluxMsg) error {
	return m.Publish(subject, msg)
}
func (m *mockBus) Subscribe(subject string, handler bus.Handler) (bus.Subscription, error) {
	return nil, nil
}
func (m *mockBus) SubscribeDurable(subject, durableName string, handler bus.Handler) (bus.Subscription, error) {
	return nil, nil
}
func (m *mockBus) Request(subject string, data []byte, timeout time.Duration) ([]byte, error) {
	return nil, nil
}

func TestShipper(t *testing.T) {
	tmpDir := t.TempDir()
	cursorPath := tmpDir + "/cursor.json"

	w, err := wal.Open(tmpDir, nil)
	if err != nil {
		t.Fatalf("Failed to open WAL: %v", err)
	}
	defer w.Close()

	// Write a proper FluxMsg
	msg := fluxmsg.New()
	msg.Data = map[string]any{"foo": "bar"}
	msg.Metadata["type"] = "telemetry.log"

	if err := w.Write(msg); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	cursor, err := NewCursor(cursorPath)
	if err != nil {
		t.Fatalf("Failed to create cursor: %v", err)
	}
	// Use pointer to mockBus to satisfy interface
	mb := &mockBus{}
	// Shipper
	s := NewLogShipper(mb, w, cursor, "test.subject", 100, 0, 0)
	s.Start()

	time.Sleep(500 * time.Millisecond)
	s.Stop()

	if len(mb.published) != 1 {
		t.Fatalf("Expected 1 published msg, got %d", len(mb.published))
	}

	// Check content
	dataMap := mb.published[0].Data
	if val, ok := dataMap["foo"]; !ok || val != "bar" {
		t.Errorf("Expected foo=bar, got %v", val)
	}

	// Verify Offset
	if cursor.Get() != 1 {
		t.Errorf("Expected cursor offset 1, got %d", cursor.Get())
	}
}
