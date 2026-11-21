// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The gear names the NATS subject the command is published on
// ("flux.ctrl.<gear>"). A separator or wildcard in it would publish beside
// the named gear — to every gear with ">", or to no token at all with "."
// splitting it — so the endpoint refuses those names with 400.
func TestHandleSimControl_RejectsGearNamesOutsideOneSubjectToken(t *testing.T) {
	srv, _ := newSimControlServer(t)

	for _, gear := range []string{
		"sim-source-1.extra", // "." splits the token
		"sim-source-1>",      // ">" fans out to every gear
		"sim-*",              // "*" fans out to a level
		"sim source 1",       // " " is not a subject character at all
	} {
		req := httptest.NewRequest("POST", "/api/v1/control/sim/start",
			strings.NewReader(`{"gear":`+quoteJSON(gear)+`}`))
		req.SetPathValue("action", "start")
		w := httptest.NewRecorder()

		srv.handleSimControl(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Result().StatusCode,
			"gear %q must be refused", gear)
	}
}

func quoteJSON(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

// A plain gear name keeps working: the rejection above must not catch what a
// gear is actually called. Nobody listens here, so the answer is 503, not a
// refusal of the name.
func TestHandleSimControl_PlainGearNameStillReachesTheControlPlane(t *testing.T) {
	srv, _ := newSimControlServer(t)

	req := httptest.NewRequest("POST", "/api/v1/control/sim/start",
		strings.NewReader(`{"gear":"sim-source-1"}`))
	req.SetPathValue("action", "start")
	w := httptest.NewRecorder()

	srv.handleSimControl(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Result().StatusCode,
		"a plain name passes validation and fails only for having no listener")
}

// /health is unauthenticated, so a lease read failure answers a generic
// "unavailable": the raw JetStream error would leak cluster internals to any
// caller that can reach the port.
func TestLeaseStatus_WithholdsTheRawJetStreamError(t *testing.T) {
	raw := errors.New("nats: no response from stream (api error code=503 err_code=10008 description=\"JetStream system temporarily unavailable\")")
	s := NewServer(&MockRegistry{}, nil, nil, nil, nil, uuid.Nil, uuid.Nil, nil, nil, nil).
		WithLease(fakeLease{held: true, err: raw}, time.Second)

	st := s.leaseStatus(context.Background())
	require.NotNil(t, st)
	assert.Equal(t, "unavailable", st.Error)
	assert.NotContains(t, st.Error, "JetStream")
	assert.NotContains(t, st.Error, "10008")
}
