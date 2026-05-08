// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/store/duckdb"
	"github.com/jaab-tech/fluxrig/pkg/telemetry"
)

const (
	TypeLog          = "telemetry.log"
	TypeLogJSON      = "telemetry.log.json"
	TypeBatchLogs    = "telemetry.batch.logs"
	TypeMetric       = "telemetry.metric"
	TypeBatchMetrics = "telemetry.batch.metrics"
	TypeSpan         = "telemetry.span"
	TypeBatchSpans   = "telemetry.batch.spans"
)

var (
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
	subject       string
	dataDir       string
	stopCh        chan struct{}
	flushInterval time.Duration
	cache         *telemetry.MetricsCache
}

func NewTelemetrySink(b bus.Bus, s *duckdb.Store, subject, dataDir string, interval time.Duration, cache *telemetry.MetricsCache) *TelemetrySink {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &TelemetrySink{
		bus:           b,
		store:         s,
		subject:       subject,
		dataDir:       dataDir,
		flushInterval: interval,
		stopCh:        make(chan struct{}),
		cache:         cache,
	}
}

func (s *TelemetrySink) Start(ctx context.Context) error {
	// 1. Subscribe to all telemetry
	sub, err := s.bus.Subscribe(s.subject, s.handleMessage)
	if err != nil {
		return err
	}
	s.sub = sub
	slog.Info("Telemetry Sink Started", "subject", s.subject)

	// 2. Start Background Flusher
	go func() {
		// Wait a few seconds for system to stabilize before first flush
		time.Sleep(3 * time.Second)

		ticker := time.NewTicker(s.flushInterval)
		defer ticker.Stop()

		slog.Debug("Telemetry Flusher Started", "interval", s.flushInterval)

		for {
			select {
			case <-ctx.Done():
				slog.Debug("Telemetry Flusher Context Done")
				return
			case <-s.stopCh:
				slog.Debug("Telemetry Flusher Stopped via channel")
				return
			case <-ticker.C:
				slog.Debug("Telemetry Flusher Firing")
				if err := s.store.FlushTelemetry(context.Background(), s.dataDir); err != nil {
					slog.Error("Failed to flush telemetry to parquet", "error", err)
				} else {
					slog.Debug("Telemetry Flush Successful")
				}
				// Also flush archiver buffer if it has data
				if err := s.store.FlushArchiverBuffer(context.Background(), s.dataDir); err != nil {
					slog.Debug("Archiver buffer flush skipped or failed", "error", err)
				}
			}
		}
	}()

	return nil
}

func (s *TelemetrySink) Stop() error {
	close(s.stopCh)
	if s.sub != nil {
		return s.sub.Unsubscribe()
	}
	return nil
}

func (s *TelemetrySink) handleMessage(ctx context.Context, msg *fluxmsg.FluxMsg) {
	msgType := msg.Metadata["type"]

	var err error
	switch msgType {
	case TypeLog, TypeLogJSON, TypeBatchLogs:
		err = s.persistLogs(msg)
	case TypeMetric, TypeBatchMetrics:
		err = s.persistMetrics(msg)
	case TypeSpan, TypeBatchSpans:
		err = s.persistSpans(msg)
	default:
		slog.Debug("Unknown telemetry type", "type", msgType)
	}

	if err != nil {
		slog.Error("Failed to persist telemetry", "error", err, "type", msgType)
	}
}

func (s *TelemetrySink) persistLogs(msg *fluxmsg.FluxMsg) error {
	msgType := msg.Metadata["type"]
	var logs []map[string]any

	if msgType == TypeLog {
		logs = []map[string]any{msg.Data}
	} else if msgType == TypeLogJSON {
		var record map[string]any
		switch v := msg.Data["record"].(type) {
		case json.RawMessage:
			_ = json.Unmarshal(v, &record)
		case []byte:
			_ = json.Unmarshal(v, &record)
		case string:
			_ = json.Unmarshal([]byte(v), &record)
		case map[string]any:
			record = v
		case map[interface{}]interface{}:
			record = make(map[string]any)
			for k, val := range v {
				if ks, ok := k.(string); ok {
					record[ks] = val
				}
			}
		}
		if record != nil {
			logs = []map[string]any{record}
		}
	} else {
		batch, ok := msg.Data["batch"].([]any)
		if !ok {
			return fmt.Errorf("invalid batch format for logs")
		}
		for _, item := range batch {
			if l, ok := item.(map[string]any); ok {
				logs = append(logs, l)
			} else if lIface, okIface := item.(map[interface{}]interface{}); okIface {
				lMap := make(map[string]any)
				for k, v := range lIface {
					if ks, okK := k.(string); okK {
						lMap[ks] = v
					}
				}
				logs = append(logs, lMap)
			}
		}
	}

	for _, log := range logs {
		log = cleanMap(log).(map[string]any)

		str := func(k string) string { v, _ := log[k].(string); return v }
		u64 := func(k string) uint64 {
			switch v := log[k].(type) {
			case uint64:
				return v
			case float64:
				return uint64(v)
			case int64:
				if v < 0 {
					return 0
				}
				return uint64(v)
			case int:
				if v < 0 {
					return 0
				}
				return uint64(v)
			default:
				return 0
			}
		}
		parseUUID := func(k string) uuid.UUID {
			switch v := log[k].(type) {
			case uuid.UUID:
				return v
			case string:
				id, _ := uuid.Parse(v)
				return id
			case []byte:
				id, _ := uuid.FromBytes(v)
				return id
			default:
				return uuid.Nil
			}
		}

		baseName := str("entity_name")
		eType, eName, sFile, sFunc, sLine := extractIdentityAndSource(baseName, log)

		// Collect attributes: Use existing map or collect unknown top-level fields
		attrs, ok := log["attributes"].(map[string]any)
		if !ok {
			// collect unknown fields as attributes for flat JSON logs
			a := make(map[string]any)
			for k, v := range log {
				switch k {
				case "timestamp", "entity_id", "entity_type", "entity_name", "trace_id", "span_id", "severity", "body", "level", "msg", "time":
					continue
				default:
					a[k] = v
				}
			}
			attrs = a
		}
		attrJSON, _ := json.Marshal(cleanMap(attrs))

		severity := str("severity")
		if severity == "" {
			severity = str("level")
		}

		body := str("body")
		if body == "" {
			body = str("msg")
		}

		var ts int64
		tsU64 := u64("timestamp")
		if tsU64 != 0 {
			if tsU64 > 1e17 { // Heuristic: it's nanoseconds
				//nolint:gosec // timestamp in micros fits in int64 until year 2262
				ts = int64(tsU64 / 1000)
			} else {
				//nolint:gosec // timestamp in micros fits in int64
				ts = int64(tsU64)
			}
		} else if tStr, ok := log["time"].(string); ok {
			if t, err := time.Parse(time.RFC3339Nano, tStr); err == nil {
				ts = t.UTC().UnixMicro()
			} else if t, err := time.Parse(time.RFC3339, tStr); err == nil {
				ts = t.UTC().UnixMicro()
			}
		}

		_, err := s.store.DB().Exec(queryInsertLog,
			ts, parseUUID("entity_id"), eType, eName, str("trace_id"), str("span_id"),
			severity, sFile, sLine, sFunc, body, string(attrJSON))
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
		metrics = []map[string]any{msg.Data}
	} else {
		batch, ok := msg.Data["batch"].([]any)
		if !ok {
			return fmt.Errorf("invalid batch format for metrics")
		}
		for _, item := range batch {
			if m, ok := item.(map[string]any); ok {
				metrics = append(metrics, m)
			} else if mIface, okIface := item.(map[interface{}]interface{}); okIface {
				mMap := make(map[string]any)
				for k, v := range mIface {
					if ks, okK := k.(string); okK {
						mMap[ks] = v
					}
				}
				metrics = append(metrics, mMap)
			}
		}
	}

	for _, metric := range metrics {
		metric = cleanMap(metric).(map[string]any)
		str := func(k string) string { v, _ := metric[k].(string); return v }
		parseUUID := func(k string) uuid.UUID {
			switch v := metric[k].(type) {
			case uuid.UUID:
				return v
			case string:
				id, _ := uuid.Parse(v)
				return id
			case []byte:
				id, _ := uuid.FromBytes(v)
				return id
			default:
				return uuid.Nil
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
				return 0
			}
		}

		eID := parseUUID("entity_id")
		eName := str("entity_name")

		var ts int64
		switch v := metric["timestamp"].(type) {
		case time.Time:
			ts = v.UnixMicro()
		case int64:
			if v > 1e17 { // Nanoseconds
				ts = v / 1000
			} else {
				ts = v
			}
		case float64:
			if v > 1e17 { // Nanoseconds
				ts = int64(v / 1000)
			} else {
				ts = int64(v)
			}
		default:
			ts = time.Now().UnixMicro()
		}

		valFloat := f64("value")
		attrJSON, _ := json.Marshal(cleanMap(metric["attributes"]))

		if eID != uuid.Nil {
			if _, err := s.store.DB().Exec(queryInsertMetric, ts, eID, eName, str("name"), str("description"), str("unit"), str("type"), valFloat, string(attrJSON)); err != nil {
				slog.Error("Failed to insert metric", "error", err, "name", str("name"))
			}
			if s.cache != nil {
				s.cache.SetGauge(eID, str("name"), valFloat, eName, "")
			}
		}
	}
	return nil
}

func (s *TelemetrySink) persistSpans(msg *fluxmsg.FluxMsg) error {
	msgType := msg.Metadata["type"]
	var spans []map[string]any

	if msgType == TypeSpan {
		spans = []map[string]any{msg.Data}
	} else {
		batch, ok := msg.Data["batch"].([]any)
		if !ok {
			return fmt.Errorf("invalid batch format for spans")
		}
		for _, item := range batch {
			if s, ok := item.(map[string]any); ok {
				spans = append(spans, s)
			} else if sIface, okIface := item.(map[interface{}]interface{}); okIface {
				sMap := make(map[string]any)
				for k, v := range sIface {
					if ks, okK := k.(string); okK {
						sMap[ks] = v
					}
				}
				spans = append(spans, sMap)
			}
		}
	}

	for _, span := range spans {
		span = cleanMap(span).(map[string]any)
		str := func(k string) string { v, _ := span[k].(string); return v }
		u64 := func(k string) uint64 {
			switch v := span[k].(type) {
			case uint64:
				return v
			case float64:
				return uint64(v)
			case int64:
				if v < 0 {
					return 0
				}
				return uint64(v)
			default:
				return 0
			}
		}
		parseUUID := func(k string) uuid.UUID {
			switch v := span[k].(type) {
			case uuid.UUID:
				return v
			case string:
				id, _ := uuid.Parse(v)
				return id
			case []byte:
				id, _ := uuid.FromBytes(v)
				return id
			default:
				return uuid.Nil
			}
		}

		attrJSON, _ := json.Marshal(cleanMap(span["attributes"]))

		if str("parent_span_id") != "" {
			slog.Debug("Ingest: Persisting linked span", "trace_id", str("trace_id"), "span_id", str("span_id"), "parent_id", str("parent_span_id"))
		}
		startU64 := u64("start_time")
		endU64 := u64("end_time")

		var startTS, endTS int64
		if startU64 > math.MaxInt64 {
			startTS = math.MaxInt64
		} else {
			startTS = int64(startU64)
		}

		if endU64 > math.MaxInt64 {
			endTS = math.MaxInt64
		} else {
			endTS = int64(endU64)
		}
		if startTS > 1e17 {
			startTS /= 1000
		}
		if endTS > 1e17 {
			endTS /= 1000
		}

		if _, err := s.store.DB().Exec(queryInsertSpan,
			str("trace_id"), str("span_id"), str("parent_span_id"), str("name"),
			startTS, endTS,
			parseUUID("entity_id"), str("entity_name"), string(attrJSON)); err != nil {
			slog.Error("Failed to insert span", "error", err, "name", str("name"))
		}
	}
	return nil
}

func extractIdentityAndSource(baseName string, log map[string]any) (eType, eName, sFile, sFunc string, sLine int) {
	eName = baseName
	eType = "UNKNOWN"

	// 1. Try top-level entity_type
	if val, ok := log["entity_type"].(string); ok && val != "" {
		eType = strings.ToUpper(val)
	}

	if log["attributes"] != nil {
		if attrs, ok := log["attributes"].(map[string]any); ok {
			// 2. Try attributes['entity_type'] or attributes['component']
			if eType == "UNKNOWN" {
				if val, ok := attrs["entity_type"].(string); ok && val != "" {
					eType = strings.ToUpper(val)
				} else if val, ok := attrs["component"].(string); ok && val != "" {
					eType = strings.ToUpper(val)
				}
			}

			if val, ok := attrs["code.filepath"].(string); ok {
				sFile = val
			}
			if val, ok := attrs["code.function"].(string); ok {
				sFunc = val
			}
			if val, ok := attrs["code.lineno"].(float64); ok {
				sLine = int(val)
			}
		}
	}
	return
}
