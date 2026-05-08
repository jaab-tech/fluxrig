package telemetry

import (
	"context"
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
		_, err := ibus.Subscribe("test.sub", func(ctx context.Context, msg *fluxmsg.FluxMsg) {})
		assert.NoError(t, err)
	})

	t.Run("SubscribeRaw", func(t *testing.T) {
		_, err := ibus.SubscribeRaw("test.raw", "stream", func(ctx context.Context, subject string, data []byte) {})
		assert.NoError(t, err)
	})

	t.Run("SubscribeDurable", func(t *testing.T) {
		_, err := ibus.SubscribeDurable("test.dur", "durable", func(ctx context.Context, msg *fluxmsg.FluxMsg) {})
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
	janitor := NewJanitor(logger, tmpDir, 30)

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
