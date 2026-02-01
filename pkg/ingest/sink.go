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
	"github.com/jaab-tech/fluxrig/pkg/telemetry"
)

const (
	// Telemetry Message Types
	TypeBatchSpans   = "telemetry.batch.spans"
	TypeBatchLogs    = "telemetry.batch.logs"
	TypeBatchMetrics = "telemetry.batch.metrics"
	TypeMetric       = "telemetry.metric"
	TypeLogJSON      = "telemetry.log.json"
	TypeLog          = "telemetry.log"

	// SQL Queries
	queryInsertSpan = `
		INSERT INTO telemetry_spans (trace_id, span_id, parent_span_id, name, start_time, end_time, entity_id, entity_name, attributes)
		VALUES (?, ?, ?, ?, to_timestamp(?/1000000.0), to_timestamp(?/1000000.0), ?, ?, ?)
	`
	queryInsertLog = `
		INSERT INTO telemetry_logs (timestamp, entity_id, entity_type, entity_name, trace_id, span_id, severity, source_file, source_line, source_func, body, attributes)
		VALUES (to_timestamp(?/1000000.0), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	queryInsertMetric = `
		INSERT INTO telemetry_metrics (timestamp, entity_id, entity_name, name, description, unit, type, value, attributes)
		VALUES (to_timestamp(?/1000000.0), ?, ?, ?, ?, ?, ?, ?, ?)
	`
)

// TelemetrySink consumes telemetry batches from NATS and writes them to DuckDB.
type TelemetrySink struct {
	bus           bus.Bus
	store         *duckdb.Store
	sub           bus.Subscription
	dataDir       string
	stopCh        chan struct{}
	flushInterval time.Duration
	cache         *telemetry.MetricsCache
}

func NewTelemetrySink(b bus.Bus, s *duckdb.Store, dataDir string, interval time.Duration, cache *telemetry.MetricsCache) *TelemetrySink {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &TelemetrySink{
		bus:           b,
		store:         s,
		dataDir:       dataDir,
		stopCh:        make(chan struct{}),
		flushInterval: interval,
		cache:         cache,
	}
}

// Start subscribes to the telemetry stream.
func (s *TelemetrySink) Start() error {
	// Subscribe with a durable consumer to prevent duplicates across restarts
	sub, err := s.bus.SubscribeDurable("flux.telemetry.>", "flux-telemetry-ingest", s.handleMessage)
	if err != nil {
		return err
	}
	s.sub = sub

	// Start Flush Loop
	go func() {
		ticker := time.NewTicker(s.flushInterval)
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

// Stop unsubscribes and performs a final flush.
func (s *TelemetrySink) Stop() error {
	close(s.stopCh)

	// Perform final flush to ensure no data loss on shutdown
	if err := s.store.FlushTelemetry(context.Background(), s.dataDir); err != nil {
		slog.Error("Final telemetry flush failed", "error", err)
	}

	if s.sub != nil {
		return s.sub.Unsubscribe()
	}
	return nil
}

// handleMessage dispatches the batch to the appropriate handler based on type.
func (s *TelemetrySink) handleMessage(ctx context.Context, msg *fluxmsg.FluxMsg) {
	msgType := msg.Metadata["type"]

	var err error
	switch msgType {

	case TypeBatchSpans:
		err = s.persistSpans(msg)
	case TypeBatchLogs:
		err = s.persistLogs(msg)
	case TypeBatchMetrics:
		err = s.persistMetrics(msg)
	case TypeMetric:
		// Individual metric (not batched)
		err = s.persistMetrics(msg)
	case TypeLogJSON:
		// Individual log from JSON handler
		err = s.persistSingleLog(msg)
	case TypeLog:
		// Individual log from WAL (MsgPack)
		err = s.persistMsgPackLog(msg)
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
	// Payload is msg.Data["batch"] -> []map[string]interface{}
	batch, ok := msg.Data["batch"].([]interface{})
	if !ok {
		return fmt.Errorf("invalid batch format for spans: expected []interface{}, got %T", msg.Data["batch"])
	}

	// Prepare Statement (Bulk Insert optimization deferred)
	// We iterate row-by-row.
	for _, item := range batch {
		span, ok := item.(map[string]interface{})
		if !ok {
			// Skip invalid items instead of aborting
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

		_, err := s.store.DB().Exec(queryInsertSpan,
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

// helper to extracting override identity from attributes
func extractIdentityAndSource(defaultName string, log map[string]interface{}) (eType, eName, sFile, sFunc string, sLine int) {
	eName = defaultName
	eType = "PROCESS" // Default type
	sLine = 0

	// 1. Check Top-Level Overrides (From WAL Handler)
	if t, ok := log["entity_type"].(string); ok && t != "" {
		eType = t
	}

	if attrs, ok := log["attributes"]; ok {
		if m, ok := attrs.(map[string]interface{}); ok {
			// Extract Identity
			// Prioritize namespaced keys to avoid collision (e.g. heartbeat metrics having "name")
			if t, ok := m["flux.type"].(string); ok && t != "" {
				eType = t
			} else if c, ok := m["component"].(string); ok && c != "" {
				// Fallback for legacy / other loggers
				eType = c
			}

			if n, ok := m["flux.name"].(string); ok && n != "" {
				// Only promote to Entity Name if it's a Gear or Snake.
				// For Controllers/Scenarios, we want to preserve the Physical Entity (e.g. Mixer).
				if eType == "GEAR" || eType == "SNAKE" || eType == "SCENARIO" {
					eName = n
					delete(m, "flux.name") // Remove if promoted
				}
				// If not promoted, it remains in m (attributes) for the DB.
			}
			// Note: We intentionally DO NOT fallback to "name" for EntityName
			// because "name" is too common for metrics/events (e.g. "heartbeats_sent").
			// The baseName (from entity_name field) is usually correct for the host/process.
			// Only specific overrides (flux.name) should change it.

			// Extract Source
			if f, ok := m["code.file.path"].(string); ok {
				sFile = f
			}
			if fn, ok := m["code.function.name"].(string); ok {
				sFunc = fn
			}
			// Handle line number (various types)
			if l, ok := m["code.line.number"]; ok {
				switch v := l.(type) {
				case int:
					sLine = v
				case float64:
					sLine = int(v)
				case int64:
					sLine = int(v)
				case string:
					sLine, _ = strconv.Atoi(v)
				}
			}

			// Clean up extracted attributes (Keep flux.* for trace context if needed, but maybe remove?)
			// Keeping them in attributes is fine, but we remove the source ones to save space.
			delete(m, "code.file.path")
			delete(m, "code.function.name")
			delete(m, "code.line.number")

			// Clean up identity keys that were promoted to columns
			delete(m, "flux.type")
			delete(m, "name")
			delete(m, "component")
		}
	}
	return
}

func (s *TelemetrySink) persistLogs(msg *fluxmsg.FluxMsg) error {
	batch, ok := msg.Data["batch"].([]interface{})
	if !ok {
		return fmt.Errorf("invalid batch format for logs: expected []interface{}, got %T", msg.Data["batch"])
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

		// Extract Identity & Source, and CLEAN attributes
		baseName := str("entity_name")
		eType, eName, sFile, sFunc, sLine := extractIdentityAndSource(baseName, log)

		attrJSON, _ := json.Marshal(log["attributes"])

		_, err := s.store.DB().Exec(queryInsertLog,
			//nolint:gosec // timestamp conversion safe
			int64(u64("timestamp")), u64("entity_id"), eType, eName, str("trace_id"), str("span_id"),
			str("severity"), sFile, sLine, sFunc, str("body"), string(attrJSON))

		if err != nil {
			return err
		}
	}
	return nil
}

func (s *TelemetrySink) persistMetrics(msg *fluxmsg.FluxMsg) error {
	msgType := msg.Metadata["type"]
	var metrics []map[string]any

	if msgType == TypeMetric {
		// Single metric - msg.Data is the metric itself
		metrics = []map[string]any{msg.Data}
	} else if msgType == TypeBatchMetrics {
		// Batched metrics
		batch, ok := msg.Data["batch"].([]interface{})
		if !ok {
			return fmt.Errorf("invalid batch format for metrics: expected []interface{}, got %T", msg.Data["batch"])
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

		_, err := s.store.DB().Exec(queryInsertMetric,
			ts, u64("entity_id"), str("entity_name"), str("name"),
			str("description"), str("unit"), str("type"), f64("value"), string(attrJSON))

		if err != nil {
			return err
		}

		// Update In-Memory Cache
		if s.cache != nil {
			// Convert to int64 for cache (which uses Atomic/Gauge)
			// Truncate float values
			valFloat := f64("value")

			// Try to extract type from attributes if possible, or leave empty
			eType := "" // Default

			s.cache.SetGauge(u64("entity_id"), str("name"), valFloat, str("entity_name"), eType)
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

	// Extract Identity & Source, and CLEAN attributes
	baseName := str("entity_name")
	eType, eName, sFile, sFunc, sLine := extractIdentityAndSource(baseName, log)

	attrJSON, _ := json.Marshal(log["attributes"])

	_, err := s.store.DB().Exec(queryInsertLog,
		//nolint:gosec // timestamp conversion safe
		int64(u64("timestamp")), u64("entity_id"), eType, eName, str("trace_id"), str("span_id"),
		str("severity"), sFile, sLine, sFunc, str("body"), string(attrJSON))

	return err
}

func (s *TelemetrySink) persistMsgPackLog(msg *fluxmsg.FluxMsg) error {
	// msg.Data IS the log record map[string]any
	log := msg.Data

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

	// Extract Identity & Source, and CLEAN attributes
	baseName := str("entity_name")
	eType, eName, sFile, sFunc, sLine := extractIdentityAndSource(baseName, log)

	// Attributes might need marshalling if they are map/slice
	attrJSON := []byte("{}")
	if attrs, ok := log["attributes"]; ok {
		attrJSON, _ = json.Marshal(attrs)
	}

	_, err := s.store.DB().Exec(queryInsertLog,
		//nolint:gosec // timestamp conversion safe
		int64(u64("timestamp")), u64("entity_id"), eType, eName, str("trace_id"), str("span_id"),
		str("severity"), sFile, sLine, sFunc, str("body"), string(attrJSON))

	return err
}
