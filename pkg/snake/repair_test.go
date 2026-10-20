// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package snake_test

import (
	"context"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/snake"
)

// startRepairServer boots an embedded server with no provisioning and returns
// a raw JetStream handle. The repair tool starts the same way: diagnosis
// comes before any stream is touched.
func startRepairServer(t *testing.T) (jetstream.JetStream, func()) {
	t.Helper()
	srv, err := snake.NewServer(context.Background(), snake.Config{
		Port:        -1,
		ClusterName: t.Name(),
		StoreDir:    t.TempDir(),
		StreamName:  "", // no provisioning: repair diagnoses first
	})
	require.NoError(t, err)

	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	js, err := jetstream.New(nc)
	require.NoError(t, err)

	return js, func() {
		nc.Close()
		srv.Shutdown()
	}
}

func wantedStreams() []snake.ExpectedStream {
	return []snake.ExpectedStream{
		{Name: "flux-msg", Subjects: []string{"flux.msg.>"}},
		{Name: "flux-telemetry", Subjects: []string{"flux.telemetry.>"}},
	}
}

// A fresh store reports every expected stream absent, and repairing it creates
// them without asking for force: creation loses nothing.
func TestRepair_AbsentStreamsAreCreated(t *testing.T) {
	ctx := context.Background()
	js, done := startRepairServer(t)
	defer done()

	report, err := snake.Diagnose(ctx, js, wantedStreams())
	require.NoError(t, err)
	require.Len(t, report.Streams, 2)
	for _, d := range report.Streams {
		assert.Equal(t, snake.StreamAbsent, d.State, "a fresh store has no streams")
	}
	assert.False(t, report.NeedsForce())

	result, err := snake.Repair(ctx, js, report, wantedStreams(), false)
	require.NoError(t, err)
	assert.Len(t, result.Created, 2)
	assert.Empty(t, result.Recreated)

	// Diagnose again: everything must now be healthy.
	report, err = snake.Diagnose(ctx, js, wantedStreams())
	require.NoError(t, err)
	for _, d := range report.Streams {
		assert.Equal(t, snake.StreamHealthy, d.State)
	}
}

// A stream whose subjects drifted is updated in place: updating a config loses
// no messages, so no force is needed.
func TestRepair_DriftIsUpdated(t *testing.T) {
	ctx := context.Background()
	js, done := startRepairServer(t)
	defer done()

	_, err := js.CreateStream(ctx, jetstream.StreamConfig{
		Name:     "flux-msg",
		Subjects: []string{"flux.msg.>", "stale.extra.>"},
	})
	require.NoError(t, err)
	_, err = js.CreateStream(ctx, jetstream.StreamConfig{
		Name:     "flux-telemetry",
		Subjects: []string{"flux.telemetry.>"},
	})
	require.NoError(t, err)

	report, err := snake.Diagnose(ctx, js, wantedStreams())
	require.NoError(t, err)
	require.Len(t, report.Streams, 2)
	var drifted snake.StreamDiagnosis
	for _, d := range report.Streams {
		if d.Name == "flux-msg" {
			drifted = d
		}
	}
	assert.Equal(t, snake.StreamDrift, drifted.State)
	assert.False(t, report.NeedsForce())

	result, err := snake.Repair(ctx, js, report, wantedStreams(), false)
	require.NoError(t, err)
	assert.Equal(t, []string{"flux-msg"}, result.Updated)

	st, err := js.Stream(ctx, "flux-msg")
	require.NoError(t, err)
	info, err := st.Info(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"flux.msg.>"}, info.Config.Subjects)
}

// An unreadable stream blocks the repair without force: recreating it would
// discard messages, and a partial repair that leaves the killer in place would
// read as success.
func TestRepair_UnreadableNeedsForce(t *testing.T) {
	ctx := context.Background()
	js, done := startRepairServer(t)
	defer done()

	_, err := js.CreateStream(ctx, jetstream.StreamConfig{
		Name:     "flux-msg",
		Subjects: []string{"flux.msg.>"},
	})
	require.NoError(t, err)

	// Publish two messages, then corrupt one read path by deleting the second
	// message's sequence is not possible via the API; instead exercise the
	// force path on a healthy-looking stream would mislead. So this test pins
	// the guard on a synthetic report: a stream the diagnosis already marked
	// unreadable refuses to repair without force.
	_, err = js.Publish(ctx, "flux.msg.x", []byte("one"))
	require.NoError(t, err)
	_, err = js.Publish(ctx, "flux.msg.x", []byte("two"))
	require.NoError(t, err)

	forced := snake.Report{Streams: []snake.StreamDiagnosis{{
		Name:   "flux-msg",
		State:  snake.StreamUnreadable,
		Detail: "synthetic: messages cannot be read",
		Msgs:   2,
	}}}
	assert.True(t, forced.NeedsForce())

	_, err = snake.Repair(ctx, js, forced, wantedStreams(), false)
	require.Error(t, err, "an unreadable stream must block the repair without force")
	assert.Contains(t, err.Error(), "--force")

	result, err := snake.Repair(ctx, js, forced, wantedStreams(), true)
	require.NoError(t, err)
	assert.Equal(t, []string{"flux-msg"}, result.Recreated)
	assert.Equal(t, uint64(2), result.Discarded["flux-msg"])

	// The recreated stream exists empty; a fresh diagnosis sees it healthy.
	report, err := snake.Diagnose(ctx, js, []snake.ExpectedStream{{Name: "flux-msg", Subjects: []string{"flux.msg.>"}}})
	require.NoError(t, err)
	require.Len(t, report.Streams, 1)
	assert.Equal(t, snake.StreamHealthy, report.Streams[0].State)
	assert.Zero(t, report.Streams[0].Msgs)
}
