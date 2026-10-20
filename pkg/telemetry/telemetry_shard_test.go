package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/fxamacker/cbor/v2"
	"github.com/stretchr/testify/assert"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

func TestSourceHandler_Decorators(t *testing.T) {
	inner := slog.Default().Handler()
	h := &SourceHandler{next: inner}

	t.Run("Next", func(t *testing.T) {
		assert.Equal(t, inner, h.Next())
	})

	t.Run("WithAttrs", func(t *testing.T) {
		h2 := h.WithAttrs([]slog.Attr{slog.String("k", "v")}).(*SourceHandler)
		assert.NotNil(t, h2)
		assert.NotNil(t, h2.next)
	})

	t.Run("WithGroup", func(t *testing.T) {
		h2 := h.WithGroup("test").(*SourceHandler)
		assert.NotNil(t, h2)
		assert.NotNil(t, h2.next)
	})
}

func TestMultiHandler_Decorators(t *testing.T) {
	h1 := slog.Default().Handler()
	h2 := slog.Default().Handler()
	m := NewMultiHandler(h1, h2)

	t.Run("Handlers", func(t *testing.T) {
		assert.Len(t, m.Handlers(), 2)
	})

	t.Run("Enabled", func(t *testing.T) {
		assert.True(t, m.Enabled(context.Background(), slog.LevelInfo))
	})

	t.Run("WithAttrs", func(t *testing.T) {
		m2 := m.WithAttrs([]slog.Attr{slog.String("k", "v")}).(*MultiHandler)
		assert.Len(t, m2.Handlers(), 2)
	})

	t.Run("WithGroup", func(t *testing.T) {
		m2 := m.WithGroup("test").(*MultiHandler)
		assert.Len(t, m2.Handlers(), 2)
	})

	t.Run("Handle", func(t *testing.T) {
		err := m.Handle(context.Background(), slog.Record{Level: slog.LevelInfo})
		assert.NoError(t, err)
	})
}

func TestProvider_GetMetters(t *testing.T) {
	prev := currentMetrics
	defer func() { currentMetrics = prev }()
	currentMetrics = nil

	t.Run("GetMeter_Nil", func(t *testing.T) {
		m := GetMeter("test")
		assert.NotNil(t, m)
	})

	t.Run("GetMetrics_Nil", func(t *testing.T) {
		m := GetMetrics()
		assert.Nil(t, m)
	})
}

func TestVerifyConnectivity(t *testing.T) {
	mock := bus.NewMockBus()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := VerifyConnectivity(ctx, mock, "test", 10*time.Millisecond, 10*time.Millisecond)
	assert.Error(t, err)
}

// Forensic Expansion: Zero-Coverage Islands

func TestMetricsCache_Forensic(t *testing.T) {
	cache := NewMetricsCache()

	id100 := uuid.MustParse("00000000-0000-0000-0000-000000000100")
	t.Run("UpdateCounter", func(t *testing.T) {
		cache.UpdateCounter(id100, "pub_count", 5, "rack-1", "RACK")
		stats := cache.GetStats(id100)
		assert.NotNil(t, stats)
		assert.Equal(t, 5.0, stats.Metrics["pub_count"].Value)

		// Update again
		cache.UpdateCounter(id100, "pub_count", 10, "rack-1", "RACK")
		stats = cache.GetStats(id100)
		assert.Equal(t, 15.0, stats.Metrics["pub_count"].Value)
	})

	t.Run("SetGauge", func(t *testing.T) {
		cache.SetGauge(id100, "cpu_usage", 45.5, "rack-1", "RACK")
		stats := cache.GetStats(id100)
		assert.Equal(t, 45.5, stats.Metrics["cpu_usage"].Value)
	})

	t.Run("GetAllStats", func(t *testing.T) {
		all := cache.GetAllStats()
		assert.NotEmpty(t, all)
	})

	t.Run("GetStats_NonExistent", func(t *testing.T) {
		assert.Nil(t, cache.GetStats(uuid.New()))
	})
}

func TestInstrumentedBus_Forensic(t *testing.T) {
	// Clear global metrics to avoid panic on nil instruments if previously initialized partially
	prev := currentMetrics
	currentMetrics = nil
	defer func() { currentMetrics = prev }()

	mock := bus.NewMockBus()
	ibus := NewInstrumentedBus(mock, nil)
	ctx := context.Background()

	t.Run("Lifecycle", func(t *testing.T) {
		err := ibus.Connect("nats://localhost", bus.ConnectOptions{})
		assert.NoError(t, err)
		assert.Nil(t, ibus.Core())
		assert.NotNil(t, ibus.KV())
		ibus.Close()
	})

	t.Run("Publish_NoMetrics", func(t *testing.T) {
		msg := fluxmsg.New()
		err := ibus.Publish(ctx, "test.subject", msg)
		assert.NoError(t, err)
	})

	t.Run("PublishRaw_NoMetrics", func(t *testing.T) {
		// Use a valid CBOR payload to satisfy the MockBus unmarshal
		data, _ := cbor.Marshal(fluxmsg.New())
		err := ibus.PublishRaw(ctx, "test.raw", data, uuid.New())
		assert.NoError(t, err)
	})

	t.Run("Subscribe", func(t *testing.T) {
		_, err := ibus.Subscribe("test.sub", func(ctx context.Context, msg *fluxmsg.FluxMsg) error { return nil })
		assert.NoError(t, err)
	})

	t.Run("SubscribeRaw", func(t *testing.T) {
		_, err := ibus.SubscribeRaw("test.raw", "stream", func(ctx context.Context, subject string, data []byte) {})
		assert.NoError(t, err)
	})

	t.Run("SubscribeDurable", func(t *testing.T) {
		_, err := ibus.SubscribeDurable("test.dur", "durable", func(ctx context.Context, msg *fluxmsg.FluxMsg) error { return nil })
		assert.NoError(t, err)
	})
}

func TestJanitor_Forensic(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "janitor_test")
	assert.NoError(t, err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Create a dummy file with an old mod time
	oldFile := filepath.Join(tmpDir, "metrics_123456789.parquet")
	err = os.WriteFile(oldFile, []byte("data"), 0600)
	assert.NoError(t, err)

	// Force old mod time (1 year ago)
	oldTime := time.Now().AddDate(-1, 0, 0)
	err = os.Chtimes(oldFile, oldTime, oldTime)
	assert.NoError(t, err)

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	janitor := NewJanitor(logger, tmpDir, 30, time.Hour)

	t.Run("Clean", func(t *testing.T) {
		err := janitor.clean()
		assert.NoError(t, err)

		// Verify file is gone
		_, err = os.Stat(oldFile)
		assert.True(t, os.IsNotExist(err))
	})

	t.Run("StartStop", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		janitor.Start(ctx)
		cancel() // Stop immediately
	})
}

// TestJanitor_KeepsFreshFiles pins the control the e2e suite relies on: a
// janitor that deletes everything would pass a test that only checks an old
// file disappeared. The recent file must survive the same sweep.
func TestJanitor_KeepsFreshFiles(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "janitor_fresh_test")
	assert.NoError(t, err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// A name in the shape the store writes (type_<unix-nanos>.parquet) with a
	// current timestamp must survive, whatever its mtime says.
	freshFile := filepath.Join(tmpDir, fmt.Sprintf("logs_%d.parquet", time.Now().UnixNano()))
	err = os.WriteFile(freshFile, []byte("data"), 0600)
	assert.NoError(t, err)

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	janitor := NewJanitor(logger, tmpDir, 30, time.Hour)

	err = janitor.clean()
	assert.NoError(t, err)

	_, err = os.Stat(freshFile)
	assert.NoError(t, err, "a file inside the retention window must survive the sweep")
}

// TestJanitor_IntervalAndRetentionDefaults pins the fallbacks. A non-positive
// interval or retention count must not produce a zero ticker or a sweep that
// deletes everything.
func TestJanitor_IntervalAndRetentionDefaults(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	janitor := NewJanitor(logger, t.TempDir(), 0, 0)
	assert.Equal(t, defaultRetentionDays, janitor.retentionDays)
	assert.Equal(t, defaultInterval, janitor.interval)

	janitor = NewJanitor(logger, t.TempDir(), 7, 15*time.Minute)
	assert.Equal(t, 7, janitor.retentionDays)
	assert.Equal(t, 15*time.Minute, janitor.interval)
}

// TestJanitor_IgnoresImplausibleNameTimestamp pins the guard: a file whose name
// suffix parses as an epoch too far in the past (a truncated or malformed
// flush timestamp, not a real one) must fall back to its modification time. A
// fresh file with such a name must survive; before the guard it was deleted
// because the name read as 1970.
func TestJanitor_IgnoresImplausibleNameTimestamp(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	t.Run("fresh file with an old-looking name survives", func(t *testing.T) {
		tmpDir := t.TempDir()
		// 987654321 as epoch nanos is 1969-12-31. The mtime is now.
		fresh := filepath.Join(tmpDir, "logs_987654321.parquet")
		assert.NoError(t, os.WriteFile(fresh, []byte("data"), 0600))

		janitor := NewJanitor(logger, tmpDir, 30, time.Hour)
		assert.NoError(t, janitor.clean())

		_, err := os.Stat(fresh)
		assert.NoError(t, err, "the mtime governs when the name timestamp is implausible")
	})

	t.Run("old file with a real old name is deleted", func(t *testing.T) {
		tmpDir := t.TempDir()
		oldNanos := time.Now().AddDate(-1, 0, 0).UnixNano()
		old := filepath.Join(tmpDir, fmt.Sprintf("logs_%d.parquet", oldNanos))
		assert.NoError(t, os.WriteFile(old, []byte("data"), 0600))

		janitor := NewJanitor(logger, tmpDir, 30, time.Hour)
		assert.NoError(t, janitor.clean())

		_, err := os.Stat(old)
		assert.True(t, os.IsNotExist(err), "a plausible old name must still be honored")
	})
}
