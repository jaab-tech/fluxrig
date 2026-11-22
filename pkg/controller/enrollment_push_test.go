// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/fxamacker/cbor/v2"
	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/pki"
)

// scenarioGate records pushes and optionally holds them.
type scenarioGate struct {
	mu    sync.Mutex
	calls int
	ctxs  []context.Context
}

func (s *scenarioGate) PushActiveToRackSince(ctx context.Context, _ string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.ctxs = append(s.ctxs, ctx)
	return nil
}

func (s *scenarioGate) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func helloMsg(t *testing.T, name, nonce string) *message.Message {
	t.Helper()
	hello := &fluxmsg.HelloPayload{
		Name:      name,
		MachineID: uuid.Nil,
		IP:        "10.0.0.1",
		Port:      8080,
		Nonce:     nonce,
	}
	helloData, err := hello.ToData()
	if err != nil {
		t.Fatal(err)
	}
	fm := fluxmsg.New()
	fm.Data = helloData
	payload, err := cbor.Marshal(fm)
	if err != nil {
		t.Fatal(err)
	}
	return message.NewMessage(watermill.NewUUID(), payload)
}

func testEnrollment(t *testing.T, pushDelay time.Duration) (*EnrollmentController, *scenarioGate) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer := &pki.ClusterKey{Private: priv, Public: pub}
	ctrl := NewEnrollmentController(slog.Default(), &MockRegistry{}, &MockPublisher{}, signer, uuid.New(), pushDelay)
	gate := &scenarioGate{}
	ctrl.SetScenario(gate)
	return ctrl, gate
}

// waitForPush polls until the gate saw n pushes or the deadline passes.
func waitForPush(gate *scenarioGate, n int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for gate.count() < n && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	return gate.count() >= n
}

// handleLikeTheSubscriber hands msg to HandleHello the way the NATS
// subscriber does: the message context ends as soon as the handler returns.
func handleLikeTheSubscriber(t *testing.T, ctrl *EnrollmentController, msg *message.Message) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	msg.SetContext(ctx)
	_, err := ctrl.HandleHello(msg)
	cancel()
	if err != nil {
		t.Fatalf("HandleHello failed: %v", err)
	}
}

// The push after an enrollment runs although the message context ended when
// the handler returned. A Rack that restarts gets the active scenario from it.
func TestEnrollmentController_PushOutlivesTheMessage(t *testing.T) {
	ctrl, gate := testEnrollment(t, 20*time.Millisecond)

	handleLikeTheSubscriber(t, ctrl, helloMsg(t, "test-rack", "nonce-1"))

	if !waitForPush(gate, 1, 5*time.Second) {
		t.Fatal("the push did not run after the message context ended")
	}
}

// The push is bounded by the push timeout, and runs within the lifetime of
// the Mixer.
func TestEnrollmentController_PushIsBounded(t *testing.T) {
	ctrl, gate := testEnrollment(t, 0)
	lifetime, stop := context.WithCancel(context.Background())
	defer stop()
	ctrl.SetPushContext(lifetime)
	ctrl.SetPushTimeout(time.Minute)

	handleLikeTheSubscriber(t, ctrl, helloMsg(t, "test-rack", "nonce-2"))

	if !waitForPush(gate, 1, 5*time.Second) {
		t.Fatal("the push did not run")
	}
	gate.mu.Lock()
	ctx := gate.ctxs[0]
	gate.mu.Unlock()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("the push has no deadline")
	}
	if left := time.Until(deadline); left > time.Minute || left < 50*time.Second {
		t.Fatalf("the push deadline is %v away, want about the push timeout of 1m", left)
	}
	stop()
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("stopping the Mixer did not cancel the push")
	}
}

// A push that still waits for its delay when the Mixer stops is dropped.
func TestEnrollmentController_PushSkippedWhenTheMixerStops(t *testing.T) {
	delay := 100 * time.Millisecond
	ctrl, gate := testEnrollment(t, delay)
	lifetime, stop := context.WithCancel(context.Background())
	ctrl.SetPushContext(lifetime)

	handleLikeTheSubscriber(t, ctrl, helloMsg(t, "test-rack", "nonce-3"))
	stop() // the Mixer stops before the delay passes

	// Absence can only be observed over a window: five times the delay.
	if waitForPush(gate, 1, 5*delay) {
		t.Fatalf("the push fired after the Mixer stopped: %d calls", gate.count())
	}
}
