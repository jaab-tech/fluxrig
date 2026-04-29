// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/telemetry/wal"
)

func TestInspectConfig_Success(t *testing.T) {
	// 1. Mock Mixer API
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"version":"0.4.3","mode":"mixer"}`)
	})
	mux.HandleFunc("/api/v1/racks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `[{"machine_id":1,"name":"rack1","status":"online","config":{"port":8080}}]`)
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	// 2. Execute Command
	var out bytes.Buffer
	err := showMixerConfig(context.Background(), ts.URL, &out)
	assert.NoError(t, err)
	assert.Contains(t, out.String(), `"version": "0.4.3"`)

	out.Reset()
	err = showRacksConfig(context.Background(), ts.URL, &out)
	assert.NoError(t, err)
	assert.Contains(t, out.String(), "rack1")
	assert.Contains(t, out.String(), "online")
}

func TestInspectLogs_Success(t *testing.T) {
	// 1. Prepare Mock WAL
	tmpDir := t.TempDir()
	w, err := wal.Open(tmpDir, nil)
	require.NoError(t, err)

	// Create a dummy FluxMsg envelope in CBOR
	msg := map[string]interface{}{
		"flux_id": uint64(1),
		"data":    map[string]interface{}{"msg": "test"},
	}
	err = w.Write(msg)
	require.NoError(t, err)
	_ = w.Close()

	// 2. Execute Command
	var out bytes.Buffer
	var errOut bytes.Buffer
	err = inspectLogs(tmpDir, &out, &errOut)
	assert.NoError(t, err)
	assert.Contains(t, errOut.String(), "Inspecting WAL")
	assert.Contains(t, out.String(), `[1] {"data":{"msg":"test"},"flux_id":1}`)
}

func TestInspectLogs_NotFound(t *testing.T) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	err := inspectLogs("/non/existent/path/that/does/not/exist", &out, &errOut)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to open WAL")
}
