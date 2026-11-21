// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/store/duckdb"
)

// OTLPCommitter commits a batch by sending it to an OpenTelemetry collector,
// as OTLP over HTTP with JSON bodies. The Mixers keep none of it.
//
// Within a batch it remembers the kinds the collector took, so a retry after
// a failure sends only the rest. A holder that dies after the collector took
// a batch and before the stream had its confirmation leaves that batch to the
// next holder, which sends it again: with OTLP, which has no way to replace
// what it took, that one batch can arrive twice.
type OTLPCommitter struct {
	Store    *duckdb.Store
	Endpoint string
	Client   *http.Client
	Headers  http.Header
	Log      *slog.Logger

	name string
	sent map[string]bool
}

// OTLPClient builds the HTTP client and the headers of an OTLPCommitter from a
// CA file and a headers file, either of which may be empty.
func OTLPClient(caFile, headersFile string, timeout time.Duration) (*http.Client, http.Header, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, nil, fmt.Errorf("ingest: otlp ca file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, nil, fmt.Errorf("ingest: otlp ca file %s holds no certificate", caFile)
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	headers := http.Header{}
	if headersFile != "" {
		f, err := os.Open(headersFile)
		if err != nil {
			return nil, nil, fmt.Errorf("ingest: otlp headers file: %w", err)
		}
		defer func() { _ = f.Close() }()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			name, value, ok := strings.Cut(line, ":")
			if !ok {
				return nil, nil, fmt.Errorf("ingest: otlp headers file: %q is not Name: value", line)
			}
			headers.Add(strings.TrimSpace(name), strings.TrimSpace(value))
		}
		if err = sc.Err(); err != nil {
			return nil, nil, err
		}
	}
	return &http.Client{Transport: transport, Timeout: timeout}, headers, nil
}

func (c *OTLPCommitter) Commit(ctx context.Context, first uint64, _ time.Time) error {
	if name := batchName(first); c.name != name {
		c.name, c.sent = name, map[string]bool{}
	}
	return c.Store.ExportTelemetry(ctx, func(rows duckdb.TelemetryRows) error {
		for _, part := range []struct {
			kind, path string
			body       func() any
			empty      bool
		}{
			{"logs", "/v1/logs", func() any { return otlpLogs(rows.Logs) }, len(rows.Logs) == 0},
			{"metrics", "/v1/metrics", func() any { return otlpMetrics(rows.Metrics) }, len(rows.Metrics) == 0},
			{"spans", "/v1/traces", func() any { return otlpSpans(rows.Spans) }, len(rows.Spans) == 0},
		} {
			if part.empty || c.sent[part.kind] {
				continue
			}
			if err := c.post(ctx, part.path, part.body()); err != nil {
				return fmt.Errorf("send the %s: %w", part.kind, err)
			}
			c.sent[part.kind] = true
		}
		return nil
	})
}

func (c *OTLPCommitter) Discard(ctx context.Context) {
	c.name, c.sent = "", nil
}

func (c *OTLPCommitter) post(ctx context.Context, path string, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(c.Endpoint, "/")+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	for name, values := range c.Headers {
		for _, v := range values {
			req.Header.Add(name, v)
		}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("the collector answered %s: %s", resp.Status, bytes.TrimSpace(msg))
	}
	return nil
}

// The OTLP/JSON shapes: https://opentelemetry.io/docs/specs/otlp/#json-protobuf-encoding.
// Times are nanoseconds as decimal strings; trace and span ids are hex.

type otlpKV struct {
	Key   string    `json:"key"`
	Value otlpValue `json:"value"`
}

type otlpValue struct {
	StringValue *string  `json:"stringValue,omitempty"`
	BoolValue   *bool    `json:"boolValue,omitempty"`
	IntValue    *string  `json:"intValue,omitempty"`
	DoubleValue *float64 `json:"doubleValue,omitempty"`
}

func otlpString(v string) otlpValue { return otlpValue{StringValue: &v} }

// otlpAttrs turns a JSON object of attributes into OTLP attributes.
func otlpAttrs(raw string) []otlpKV {
	var m map[string]any
	if raw == "" || json.Unmarshal([]byte(raw), &m) != nil {
		return nil
	}
	out := make([]otlpKV, 0, len(m))
	for k, v := range m {
		var val otlpValue
		switch x := v.(type) {
		case string:
			val = otlpString(x)
		case bool:
			val = otlpValue{BoolValue: &x}
		case float64:
			if x == float64(int64(x)) {
				s := strconv.FormatInt(int64(x), 10)
				val = otlpValue{IntValue: &s}
			} else {
				val = otlpValue{DoubleValue: &x}
			}
		default:
			b, _ := json.Marshal(x)
			val = otlpString(string(b))
		}
		out = append(out, otlpKV{Key: k, Value: val})
	}
	return out
}

type otlpResource struct {
	Attributes []otlpKV `json:"attributes"`
}

func resourceOf(entityID, entityName, entityType string) otlpResource {
	attrs := []otlpKV{{Key: "service.name", Value: otlpString(entityName)}}
	if entityID != "" {
		attrs = append(attrs, otlpKV{Key: "fluxrig.entity.id", Value: otlpString(entityID)})
	}
	if entityType != "" {
		attrs = append(attrs, otlpKV{Key: "fluxrig.entity.type", Value: otlpString(entityType)})
	}
	return otlpResource{Attributes: attrs}
}

type otlpScope struct {
	Name string `json:"name"`
}

var fluxrigScope = otlpScope{Name: "fluxrig"}

func nanos(t time.Time) string { return strconv.FormatInt(t.UnixNano(), 10) }

// otlpID returns id when it is a hex id of n bytes, and "" otherwise.
func otlpID(id string, n int) string {
	if b, err := hex.DecodeString(id); err == nil && len(b) == n {
		return id
	}
	return ""
}

var severityNumbers = map[string]int{"TRACE": 1, "DEBUG": 5, "INFO": 9, "WARN": 13, "WARNING": 13, "ERROR": 17, "FATAL": 21}

type otlpLogRecord struct {
	TimeUnixNano   string    `json:"timeUnixNano"`
	SeverityNumber int       `json:"severityNumber,omitempty"`
	SeverityText   string    `json:"severityText,omitempty"`
	Body           otlpValue `json:"body"`
	Attributes     []otlpKV  `json:"attributes,omitempty"`
	TraceID        string    `json:"traceId,omitempty"`
	SpanID         string    `json:"spanId,omitempty"`
}

func otlpLogs(rows []duckdb.LogRow) any {
	type scopeLogs struct {
		Scope      otlpScope       `json:"scope"`
		LogRecords []otlpLogRecord `json:"logRecords"`
	}
	type resourceLogs struct {
		Resource  otlpResource `json:"resource"`
		ScopeLogs []scopeLogs  `json:"scopeLogs"`
	}
	byEntity := map[string]*resourceLogs{}
	var order []string
	for _, r := range rows {
		key := r.EntityID + "|" + r.EntityName
		rl, ok := byEntity[key]
		if !ok {
			rl = &resourceLogs{Resource: resourceOf(r.EntityID, r.EntityName, r.EntityType), ScopeLogs: []scopeLogs{{Scope: fluxrigScope}}}
			byEntity[key] = rl
			order = append(order, key)
		}
		attrs := otlpAttrs(r.Attr)
		if r.SourceFile != "" {
			attrs = append(attrs, otlpKV{Key: "code.filepath", Value: otlpString(r.SourceFile)},
				otlpKV{Key: "code.lineno", Value: otlpString(strconv.Itoa(r.SourceLine))})
		}
		sev := strings.ToUpper(r.Severity)
		rl.ScopeLogs[0].LogRecords = append(rl.ScopeLogs[0].LogRecords, otlpLogRecord{
			TimeUnixNano: nanos(r.Timestamp), SeverityNumber: severityNumbers[sev], SeverityText: sev,
			Body: otlpString(r.Body), Attributes: attrs, TraceID: otlpID(r.TraceID, 16), SpanID: otlpID(r.SpanID, 8),
		})
	}
	out := make([]resourceLogs, 0, len(order))
	for _, k := range order {
		out = append(out, *byEntity[k])
	}
	return map[string]any{"resourceLogs": out}
}

type otlpPoint struct {
	TimeUnixNano string   `json:"timeUnixNano"`
	AsDouble     float64  `json:"asDouble"`
	Attributes   []otlpKV `json:"attributes,omitempty"`
}

type otlpMetric struct {
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Unit        string    `json:"unit,omitempty"`
	Gauge       *otlpData `json:"gauge,omitempty"`
	Sum         *otlpSum  `json:"sum,omitempty"`
}

type otlpData struct {
	DataPoints []otlpPoint `json:"dataPoints"`
}

type otlpSum struct {
	DataPoints             []otlpPoint `json:"dataPoints"`
	AggregationTemporality int         `json:"aggregationTemporality"`
	IsMonotonic            bool        `json:"isMonotonic"`
}

func otlpMetrics(rows []duckdb.MetricRow) any {
	type scopeMetrics struct {
		Scope   otlpScope    `json:"scope"`
		Metrics []otlpMetric `json:"metrics"`
	}
	type resourceMetrics struct {
		Resource     otlpResource   `json:"resource"`
		ScopeMetrics []scopeMetrics `json:"scopeMetrics"`
	}
	byEntity := map[string]*resourceMetrics{}
	var order []string
	for _, r := range rows {
		key := r.EntityID + "|" + r.EntityName
		rm, ok := byEntity[key]
		if !ok {
			rm = &resourceMetrics{Resource: resourceOf(r.EntityID, r.EntityName, ""), ScopeMetrics: []scopeMetrics{{Scope: fluxrigScope}}}
			byEntity[key] = rm
			order = append(order, key)
		}
		point := otlpPoint{TimeUnixNano: nanos(r.Timestamp), AsDouble: r.Value, Attributes: otlpAttrs(r.Attr)}
		m := otlpMetric{Name: r.Name, Description: r.Description, Unit: r.Unit}
		switch strings.ToLower(r.Type) {
		case "counter", "sum":
			// Cumulative (2): the Racks report counters as running totals.
			m.Sum = &otlpSum{DataPoints: []otlpPoint{point}, AggregationTemporality: 2, IsMonotonic: true}
		default:
			m.Gauge = &otlpData{DataPoints: []otlpPoint{point}}
		}
		rm.ScopeMetrics[0].Metrics = append(rm.ScopeMetrics[0].Metrics, m)
	}
	out := make([]resourceMetrics, 0, len(order))
	for _, k := range order {
		out = append(out, *byEntity[k])
	}
	return map[string]any{"resourceMetrics": out}
}

type otlpSpan struct {
	TraceID           string     `json:"traceId"`
	SpanID            string     `json:"spanId"`
	ParentSpanID      string     `json:"parentSpanId,omitempty"`
	Name              string     `json:"name"`
	Kind              int        `json:"kind"`
	StartTimeUnixNano string     `json:"startTimeUnixNano"`
	EndTimeUnixNano   string     `json:"endTimeUnixNano"`
	Attributes        []otlpKV   `json:"attributes,omitempty"`
	Status            otlpStatus `json:"status"`
}

type otlpStatus struct {
	Code    int    `json:"code"`
	Message string `json:"message,omitempty"`
}

var spanKinds = map[string]int{"internal": 1, "server": 2, "client": 3, "producer": 4, "consumer": 5}
var statusCodes = map[string]int{"ok": 1, "error": 2}

func otlpSpans(rows []duckdb.SpanRow) any {
	type scopeSpans struct {
		Scope otlpScope  `json:"scope"`
		Spans []otlpSpan `json:"spans"`
	}
	type resourceSpans struct {
		Resource   otlpResource `json:"resource"`
		ScopeSpans []scopeSpans `json:"scopeSpans"`
	}
	byEntity := map[string]*resourceSpans{}
	var order []string
	for _, r := range rows {
		traceID, spanID := otlpID(r.TraceID, 16), otlpID(r.SpanID, 8)
		if traceID == "" || spanID == "" {
			continue // OTLP has no span without ids
		}
		key := r.EntityID + "|" + r.EntityName
		rs, ok := byEntity[key]
		if !ok {
			rs = &resourceSpans{Resource: resourceOf(r.EntityID, r.EntityName, ""), ScopeSpans: []scopeSpans{{Scope: fluxrigScope}}}
			byEntity[key] = rs
			order = append(order, key)
		}
		rs.ScopeSpans[0].Spans = append(rs.ScopeSpans[0].Spans, otlpSpan{
			TraceID: traceID, SpanID: spanID, ParentSpanID: otlpID(r.ParentSpanID, 8), Name: r.Name,
			Kind:              spanKinds[strings.ToLower(strings.TrimPrefix(strings.ToLower(r.Kind), "span_kind_"))],
			StartTimeUnixNano: nanos(r.Start), EndTimeUnixNano: nanos(r.End), Attributes: otlpAttrs(r.Attr),
			Status: otlpStatus{Code: statusCodes[strings.ToLower(r.StatusCode)], Message: r.StatusMessage},
		})
	}
	out := make([]resourceSpans, 0, len(order))
	for _, k := range order {
		out = append(out, *byEntity[k])
	}
	return map[string]any{"resourceSpans": out}
}
