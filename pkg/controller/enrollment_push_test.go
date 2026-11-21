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

// A push must not fire after the request ends: the delay waits on the
// request context, so shutting down (or any cancellation) during the delay
// drops the push instead of running it on a Background context nothing can
// cancel.
func TestEnrollmentController_PushSkippedWhenTheRequestEnds(t *testing.T) {
	ctrl, gate := testEnrollment(t, 100*time.Millisecond)

	msg := helloMsg(t, "test-rack", "nonce-1")
	ctx, cancel := context.WithCancel(context.Background())
	msg.SetContext(ctx)
	cancel() // the request ends before the delay passes

	_, err := ctrl.HandleHello(msg)
	if err != nil {
		t.Fatalf("HandleHello failed: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	if got := gate.count(); got != 0 {
		t.Fatalf("the push fired after the request ended: %d calls", got)
	}
}

// While the request lives the push still runs, on the request's context.
func TestEnrollmentController_PushRunsOnTheRequestContext(t *testing.T) {
	ctrl, gate := testEnrollment(t, 20*time.Millisecond)

	msg := helloMsg(t, "test-rack", "nonce-2")
	reqCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	msg.SetContext(reqCtx)

	_, err := ctrl.HandleHello(msg)
	if err != nil {
		t.Fatalf("HandleHello failed: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for gate.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if gate.count() != 1 {
		t.Fatal("the push did not run while the request lived")
	}
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if gate.ctxs[0] == context.Background() {
		t.Fatal("the push ran on a detached context instead of the request's")
	}
	select {
	case <-gate.ctxs[0].Done():
		t.Fatal("the push context ended before the request did")
	default:
	}
}
