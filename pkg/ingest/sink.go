package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/store/duckdb"
)

// TelemetrySink consumes telemetry batches from NATS and writes them to DuckDB.
type TelemetrySink struct {
	bus     bus.Bus
	store   *duckdb.Store
	sub     bus.Subscription
	dataDir string
	stopCh  chan struct{}
}

func NewTelemetrySink(b bus.Bus, s *duckdb.Store, dataDir string) *TelemetrySink {
	return &TelemetrySink{
		bus:     b,
		store:   s,
		dataDir: dataDir,
		stopCh:  make(chan struct{}),
	}
}

// Start subscribes to the telemetry stream.
func (s *TelemetrySink) Start() error {
	// Subscribe to all telemetry signals
	sub, err := s.bus.Subscribe("flux.telemetry.>", s.handleMessage)
	if err != nil {
		return err
	}
	s.sub = sub

	// Start Flush Loop (5s)
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := s.store.FlushTelemetry(context.Background(), s.dataDir); err != nil {
					slog.Error("Failed to flush telemetry", "error", err)
				}
			case <-s.stopCh:
				return
			}
		}
	}()

	return nil
}

// Stop unsubscribes.
func (s *TelemetrySink) Stop() error {
	close(s.stopCh)
	if s.sub != nil {
		return s.sub.Unsubscribe()
	}
	return nil
}

// handleMessage dispatches the batch to the appropriate handler based on type.
func (s *TelemetrySink) handleMessage(msg *fluxmsg.FluxMsg) {
	msgType := msg.Metadata["type"]

	var err error
	switch msgType {
	case "telemetry.batch.spans":
		err = s.persistSpans(msg)
	case "telemetry.batch.logs":
		err = s.persistLogs(msg)
	case "telemetry.batch.metrics":
		err = s.persistMetrics(msg)
	case "telemetry.metric":
		// Individual metric (not batched)
		err = s.persistMetrics(msg)
	case "telemetry.log.json":
		// Individual log from JSON handler
		err = s.persistSingleLog(msg)
	default:
		// Ignore unknown types
		return
	}

	if err != nil {
		slog.Error("Failed to persist telemetry", "type", msgType, "error", err)
	}
}

func (s *TelemetrySink) persistSpans(msg *fluxmsg.FluxMsg) error {
	// Payload is msg.Data["batch"] -> []map[string]interface{}
	batch, ok := msg.Data["batch"].([]interface{}) // JSON decoding often gives []interface{}
	if !ok {
		// If it came from internal Go code directly it might be []map[string]interface{}
		// But msgpack unmarshal usually gives generic types.
		// For now, let's assume it works or add robust check.
		return fmt.Errorf("invalid batch format")
	}

	// Prepare Statement (Bulk Insert optimization omitted for brevity/POC)
	// We do row-by-row for simplicity in POC.
	for _, item := range batch {
		span, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		// Helper to safely get string/etc
		str := func(k string) string { v, _ := span[k].(string); return v }
		u64 := func(k string) uint64 {
			switch v := span[k].(type) {
			case uint64:
				return v
			case int64:
				//nolint:gosec // conversion safe
				return uint64(v)
			case int:
				//nolint:gosec // conversion safe
				return uint64(v)
			case float64:
				return uint64(v)
			case string:
				i, _ := strconv.ParseUint(v, 10, 64)
				return i
			default:
				return 0
			}
		}

		attrJSON, _ := json.Marshal(span["attributes"])

		_, err := s.store.DB().Exec(`
			INSERT INTO telemetry_spans (trace_id, span_id, parent_span_id, name, start_time, end_time, entity_id, entity_name, attributes)
			VALUES (?, ?, ?, ?, to_timestamp(?/1000000.0), to_timestamp(?/1000000.0), ?, ?, ?)
		`,
			str("trace_id"), str("span_id"), str("parent_span_id"), str("name"),
			//nolint:gosec // conversion safe for this context
			int64(u64("start_time")),
			//nolint:gosec // conversion safe for this context
			int64(u64("end_time")),
			u64("entity_id"), str("entity_name"), string(attrJSON))

		if err != nil {
			return err
		}
	}
	return nil
}

func (s *TelemetrySink) persistLogs(msg *fluxmsg.FluxMsg) error {
	batch, ok := msg.Data["batch"].([]interface{})
	if !ok {
		return fmt.Errorf("invalid batch format")
	}

	for _, item := range batch {
		log, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		str := func(k string) string { v, _ := log[k].(string); return v }
		u64 := func(k string) uint64 {
			switch v := log[k].(type) {
			case uint64:
				return v
			case int64:
				//nolint:gosec // conversion safe
				return uint64(v)
			case int:
				//nolint:gosec // conversion safe
				return uint64(v)
			case float64:
				return uint64(v)
			case string:
				i, _ := strconv.ParseUint(v, 10, 64)
				return i
			default:
				return 0
			}
		}

		attrJSON, _ := json.Marshal(log["attributes"])

		_, err := s.store.DB().Exec(`
			INSERT INTO telemetry_logs (timestamp, entity_id, entity_name, trace_id, span_id, severity, body, attributes)
			VALUES (to_timestamp(?/1000000.0), ?, ?, ?, ?, ?, ?, ?)
		`,
			//nolint:gosec // timestamp conversion safe
			int64(u64("timestamp")), u64("entity_id"), str("entity_name"), str("trace_id"), str("span_id"),
			str("severity"), str("body"), string(attrJSON))

		if err != nil {
			return err
		}
	}
	return nil
}

func (s *TelemetrySink) persistMetrics(msg *fluxmsg.FluxMsg) error {
	msgType := msg.Metadata["type"]
	var metrics []map[string]any

	if msgType == "telemetry.metric" {
		// Single metric - msg.Data is the metric itself
		metrics = []map[string]any{msg.Data}
	} else if msgType == "telemetry.batch.metrics" {
		// Batched metrics
		batch, ok := msg.Data["batch"].([]interface{})
		if !ok {
			return fmt.Errorf("invalid batch format")
		}
		for _, item := range batch {
			if m, ok := item.(map[string]interface{}); ok {
				metrics = append(metrics, m)
			}
		}
	}

	// Persist each metric
	for _, metric := range metrics {
		str := func(k string) string { v, _ := metric[k].(string); return v }
		u64 := func(k string) uint64 {
			switch v := metric[k].(type) {
			case uint64:
				return v
			case int64:
				//nolint:gosec // conversion safe
				return uint64(v)
			case int:
				//nolint:gosec // conversion safe
				return uint64(v)
			case float64:
				return uint64(v)
			case string:
				i, _ := strconv.ParseUint(v, 10, 64)
				return i
			default:
				return 0
			}
		}
		f64 := func(k string) float64 {
			switch v := metric[k].(type) {
			case float64:
				return v
			case int64:
				return float64(v)
			case int:
				return float64(v)
			default:
				return 0.0
			}
		}

		// timestamp might be time.Time or int64 (UnixMicro)
		var ts int64
		switch v := metric["timestamp"].(type) {
		case time.Time:
			ts = v.UnixMicro()
		case int64:
			ts = v
		case float64:
			ts = int64(v)
		default:
			ts = time.Now().UnixMicro()
		}

		attrJSON, _ := json.Marshal(metric["attributes"])

		_, err := s.store.DB().Exec(`
			INSERT INTO telemetry_metrics (timestamp, entity_id, entity_name, name, description, unit, type, value, attributes)
			VALUES (to_timestamp(?/1000000.0), ?, ?, ?, ?, ?, ?, ?, ?)
		`,
			ts, u64("entity_id"), str("entity_name"), str("name"),
			str("description"), str("unit"), str("type"), f64("value"), string(attrJSON))

		if err != nil {
			return err
		}
	}
	return nil
}

func (s *TelemetrySink) persistSingleLog(msg *fluxmsg.FluxMsg) error {
	// Handle individual log from NatsWriter (telemetry.log.json)
	record, ok := msg.Data["record"].(json.RawMessage)
	if !ok {
		return fmt.Errorf("invalid log record format")
	}

	var log map[string]interface{}
	if err := json.Unmarshal(record, &log); err != nil {
		return err
	}

	str := func(k string) string { v, _ := log[k].(string); return v }
	u64 := func(k string) uint64 {
		switch v := log[k].(type) {
		case uint64:
			return v
		case int64:
			//nolint:gosec // conversion safe
			return uint64(v)
		case int:
			//nolint:gosec // conversion safe
			return uint64(v)
		case float64:
			return uint64(v)
		case string:
			i, _ := strconv.ParseUint(v, 10, 64)
			return i
		default:
			return 0
		}
	}

	attrJSON, _ := json.Marshal(log["attributes"])

	_, err := s.store.DB().Exec(`
		INSERT INTO telemetry_logs (timestamp, entity_id, entity_name, trace_id, span_id, severity, body, attributes)
		VALUES (to_timestamp(?/1000000.0), ?, ?, ?, ?, ?, ?, ?)
	`,
		//nolint:gosec // timestamp conversion safe
		int64(u64("timestamp")), u64("entity_id"), str("entity_name"), str("trace_id"), str("span_id"),
		str("severity"), str("body"), string(attrJSON))

	return err
}
