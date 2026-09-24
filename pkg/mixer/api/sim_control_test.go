// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/config"
	"github.com/jaab-tech/fluxrig/pkg/ctrl"
	"github.com/jaab-tech/fluxrig/pkg/snake"
)

func newSimControlServer(t *testing.T) (*Server, *nats.Conn) {
	t.Helper()
	s, err := snake.NewServer(context.Background(), snake.Config{
		Port:        -1,
		ClusterName: "sim-control-test",
		StoreDir:    t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(s.Shutdown)

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)

	cfg := &config.MixerConfig{}
	cfg.API.ControlConfirmTimeout = "500ms"
	srv := NewServer(&MockRegistry{}, nil, nil, nil, nil, uuid.Nil, uuid.Nil, cfg, nil, nc)
	return srv, nc
}

// Before this fix, a bare fire-and-forget publish reported 200 whether or not
// any gear was listening. With a gear actually subscribed, the command must
// both reach it and answer 200.
func TestHandleSimControl_AckedWhenAGearIsListening(t *testing.T) {
	srv, nc := newSimControlServer(t)
	ch, err := ctrl.NewNATSControlPlane(nc).Subscribe("sim-source-1")
	require.NoError(t, err)

	req := httptest.NewRequest("POST", "/api/v1/control/sim/start",
		strings.NewReader(`{"gear":"sim-source-1"}`))
	req.SetPathValue("action", "start")
	w := httptest.NewRecorder()

	srv.handleSimControl(w, req)

	require.Equal(t, http.StatusOK, w.Result().StatusCode, "body: %s", w.Body.String())
	select {
	case cmd := <-ch:
		require.Equal(t, ctrl.CmdSimStart, cmd.Cmd)
	case <-time.After(2 * time.Second):
		t.Fatal("the gear must actually receive the command, not just have the request answer 200")
	}
}

// The regression this whole fix closes: nobody subscribed on the target
// gear's subject must not read as success.
func TestHandleSimControl_FailsWhenNoGearIsListening(t *testing.T) {
	srv, _ := newSimControlServer(t)

	req := httptest.NewRequest("POST", "/api/v1/control/sim/start",
		strings.NewReader(`{"gear":"nobody-home"}`))
	req.SetPathValue("action", "start")
	w := httptest.NewRecorder()

	srv.handleSimControl(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Result().StatusCode)
	require.Contains(t, w.Body.String(), "nobody-home")
}

// Without a control-plane connection at all (a Server built without one, as
// most tests here do), the endpoint must say so rather than panic on a nil
// *nats.Conn.
func TestHandleSimControl_FailsCleanlyWithNoControlPlaneConnection(t *testing.T) {
	srv := NewServer(&MockRegistry{}, nil, nil, nil, nil, uuid.Nil, uuid.Nil, nil, nil, nil)

	req := httptest.NewRequest("POST", "/api/v1/control/sim/start",
		strings.NewReader(`{"gear":"sim-source-1"}`))
	req.SetPathValue("action", "start")
	w := httptest.NewRecorder()

	srv.handleSimControl(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Result().StatusCode)
	require.Contains(t, w.Body.String(), "Control plane not available")
}
