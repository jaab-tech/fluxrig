// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/registry/kvstore"
)

type fakeLease struct {
	held   bool
	holder kvstore.LeaseHolder
	ok     bool
	err    error
}

func (f fakeLease) Held() bool { return f.held }
func (f fakeLease) Holder(ctx context.Context) (kvstore.LeaseHolder, bool, error) {
	return f.holder, f.ok, f.err
}

func health(t *testing.T, s *Server) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleHealth(w, httptest.NewRequest("GET", "/api/v1/health", nil))
	require.Equal(t, 200, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	return body
}

// A single Mixer (lease off) reports no lease at all.
func TestHealth_NoLeaseWhenOff(t *testing.T) {
	s := NewServer(&MockRegistry{}, nil, nil, nil, nil, uuid.Nil, uuid.Nil, nil, nil, nil)
	_, present := health(t, s)["lease"]
	assert.False(t, present)
}

// A clustered Mixer reports the holder, since when, and whether it holds the
// lease itself.
func TestHealth_ReportsTheLeaseHolder(t *testing.T) {
	holder := uuid.New()
	since := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	renewed := since.Add(40 * time.Second)
	s := NewServer(&MockRegistry{}, nil, nil, nil, nil, uuid.Nil, uuid.Nil, nil, nil, nil).
		WithLease(fakeLease{held: false, ok: true, holder: kvstore.LeaseHolder{MixerID: holder, Since: since, Renewed: renewed}}, time.Second)

	lease, ok := health(t, s)["lease"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, holder.String(), lease["holder"])
	assert.Equal(t, since.Format(time.RFC3339), lease["holder_since"])
	assert.Equal(t, renewed.Format(time.RFC3339), lease["renewed"])
	assert.Equal(t, false, lease["held"])
	_, hasErr := lease["error"]
	assert.False(t, hasErr)
}

// With no holder the lease is reported without one: control writes answer
// 503 until a Mixer takes it.
func TestHealth_NoHolder(t *testing.T) {
	s := NewServer(&MockRegistry{}, nil, nil, nil, nil, uuid.Nil, uuid.Nil, nil, nil, nil).
		WithLease(fakeLease{}, time.Second)
	lease, ok := health(t, s)["lease"].(map[string]any)
	require.True(t, ok)
	_, hasHolder := lease["holder"]
	assert.False(t, hasHolder)
}

// A lease the cluster cannot read is reported in the answer; the probe still
// answers 200, because the Mixer itself is up. /health is unauthenticated, so
// the answer carries only a generic "unavailable", never the raw error.
func TestHealth_LeaseReadError(t *testing.T) {
	s := NewServer(&MockRegistry{}, nil, nil, nil, nil, uuid.Nil, uuid.Nil, nil, nil, nil).
		WithLease(fakeLease{held: true, err: errors.New("cluster not available")}, time.Second)
	lease, ok := health(t, s)["lease"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "unavailable", lease["error"])
	assert.Equal(t, true, lease["held"])
}
