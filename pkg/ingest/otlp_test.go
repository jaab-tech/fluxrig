// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

// collector is an OTLP/HTTP receiver that keeps what it takes, and refuses
// the first request to fail.
type collector struct {
	mu     sync.Mutex
	bodies map[string][]map[string]any
	fail   string
}

func (c *collector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if r.URL.Path == c.fail {
		c.fail = ""
		http.Error(w, "busy", http.StatusServiceUnavailable)
		return
	}
	b, _ := io.ReadAll(r.Body)
	var m map[string]any
	if json.Unmarshal(b, &m) != nil || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Authorization") != "Bearer t" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if c.bodies == nil {
		c.bodies = map[string][]map[string]any{}
	}
	c.bodies[r.URL.Path] = append(c.bodies[r.URL.Path], m)
}

// A batch reaches the collector as OTLP: logs and metrics, each grouped by
// the Rack that sent it. A retry after the collector refused the metrics
// sends the metrics, and not the logs it took already.
func TestOTLPCommitter_SendsEachKindOnce(t *testing.T) {
	col := &collector{fail: "/v1/metrics"}
	srv := httptest.NewServer(col)
	defer srv.Close()
	store := mixerStore(t)
	sink := &TelemetrySink{store: store}
	ctx := context.Background()
	now := uint64(time.Now().UnixMicro())
	for _, d := range []map[string]any{
		{"body": "hello", "entity_name": "rack-a", "severity": "WARN", "timestamp": now},
	} {
		require.NoError(t, sink.persistLogs(logMsg(d)))
	}
	_, err := store.DB().Exec(`INSERT INTO telemetry_metrics (timestamp, entity_name, name, type, value, unit, attributes)
		VALUES (now(), 'rack-a', 'flux.rack.heartbeats_sent', 'counter', 7, '1', '{"site":"a"}')`)
	require.NoError(t, err)

	c := &OTLPCommitter{Store: store, Endpoint: srv.URL, Client: srv.Client(), Headers: http.Header{"Authorization": {"Bearer t"}}, Log: quiet}
	require.Error(t, c.Commit(ctx, 1, time.Now()), "the collector refused the metrics")
	require.NoError(t, c.Commit(ctx, 1, time.Now()))

	col.mu.Lock()
	defer col.mu.Unlock()
	require.Len(t, col.bodies["/v1/logs"], 1, "the logs are sent once")
	require.Len(t, col.bodies["/v1/metrics"], 1)
	rl := col.bodies["/v1/logs"][0]["resourceLogs"].([]any)[0].(map[string]any)
	attrs := rl["resource"].(map[string]any)["attributes"].([]any)
	assert.Equal(t, "rack-a", attrs[0].(map[string]any)["value"].(map[string]any)["stringValue"])
	rec := rl["scopeLogs"].([]any)[0].(map[string]any)["logRecords"].([]any)[0].(map[string]any)
	assert.Equal(t, "hello", rec["body"].(map[string]any)["stringValue"])
	assert.EqualValues(t, 13, rec["severityNumber"])
	m := col.bodies["/v1/metrics"][0]["resourceMetrics"].([]any)[0].(map[string]any)["scopeMetrics"].([]any)[0].(map[string]any)["metrics"].([]any)[0].(map[string]any)
	assert.Equal(t, "flux.rack.heartbeats_sent", m["name"])
	assert.NotNil(t, m["sum"], "a counter is an OTLP sum")

	var left int
	require.NoError(t, store.DB().QueryRow(`SELECT count(*) FROM telemetry_logs`).Scan(&left))
	assert.Zero(t, left, "a committed batch leaves the tables empty")
}

func logMsg(data map[string]any) *fluxmsg.FluxMsg {
	m := fluxmsg.New()
	m.Metadata["type"] = TypeLog
	m.Data = data
	return m
}
