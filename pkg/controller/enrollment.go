// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/fxamacker/cbor/v2"
	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/pki"
	"github.com/jaab-tech/fluxrig/pkg/registry"
	"github.com/jaab-tech/fluxrig/pkg/router"
	"github.com/jaab-tech/fluxrig/pkg/security"
	"github.com/jaab-tech/fluxrig/pkg/version"
)

// ScenarioProvider defines the subset of ScenarioController used by Enrollment.
type ScenarioProvider interface {
	PushActiveToRack(ctx context.Context, rackName string) error
}

// EnrollmentController handles Rack registration and heartbeats.
type EnrollmentController struct {
	reg             registry.Registry
	publisher       message.Publisher
	signer          *pki.ClusterKey
	scenario        ScenarioProvider
	mixerID         uuid.UUID // Mixer's fluxEntityID
	pushDelay       time.Duration
	processedHellos sync.Map // Deduplication cache: Name -> time.Time
	logger          *slog.Logger
}

func NewEnrollmentController(log *slog.Logger, reg registry.Registry, pub message.Publisher, signer *pki.ClusterKey, mixerID uuid.UUID, pushDelay time.Duration) *EnrollmentController {
	return &EnrollmentController{
		reg:       reg,
		publisher: pub,
		signer:    signer,
		mixerID:   mixerID,
		pushDelay: pushDelay,
		logger:    log,
	}
}

// SetScenario sets the scenario provider for pushing active scenarios on registration.
func (c *EnrollmentController) SetScenario(sc ScenarioProvider) {
	c.scenario = sc
}

func (c *EnrollmentController) RegisterRoutes(r *router.RouterWrapper) {
	// Hello Handler
	r.Router.AddHandler(
		"enrollment_hello",
		fluxmsg.SubjectAgentHello,
		r.Sub,
		"no_publish",
		nil,
		c.HandleHello,
	)

	// Heartbeat Handler
	r.Router.AddHandler(
		"enrollment_heartbeat",
		fluxmsg.SubjectAgentHeartbeat,
		r.Sub,
		"no_publish",
		nil,
		c.HandleHeartbeat,
	)
}

func (c *EnrollmentController) HandleHello(msg *message.Message) ([]*message.Message, error) {
	c.logger.Debug("MIXER HandleHello Triggered", "len", len(msg.Payload))
	// 1. Unmarshal FluxMsg
	var fm fluxmsg.FluxMsg
	if err := cbor.Unmarshal(msg.Payload, &fm); err != nil {
		c.logger.Error("failed to unmarshal hello", "error", err)
		return nil, nil // Don't retry malformed
	}

	// 2. Parse Hello Payload
	hello, err := fluxmsg.ParseHello(fm.Data)
	if err != nil {
		c.logger.Error("invalid hello payload", "error", err)
		return nil, nil
	}

	c.logger.Info("Received Hello", "name", hello.Name, "id", hello.MachineID, "ip", hello.IP, "port", hello.Port)

	// Deduplication Check with TTL. This used to skip entirely whenever a
	// secret was present ("bypassed for recovery"), but hello.Secret is
	// caller-supplied and unverified at this point: any Hello carrying any
	// non-empty string, valid or not, skipped the throttle, which is a flood
	// vector, not a recovery path. The throttle applies to every Hello now.
	//
	// Keyed by name AND MachineID, not name alone: a Rack whose Hello was
	// just denied drops its cached MachineID and immediately retries as a
	// fresh identity (MachineID uuid.Nil) to re-provision. That retry must
	// not be silently swallowed as a "duplicate" of the denied one just
	// because it shares a name; it is a different identity attempting a
	// different (fresh-registration) code path.
	dedupKey := hello.Name + "|" + hello.MachineID.String()
	if val, loaded := c.processedHellos.Load(dedupKey); loaded {
		lastSeen := val.(time.Time)
		if time.Since(lastSeen) < 1*time.Second {
			c.logger.Warn("Duplicate Hello ignored (throttled)", "name", hello.Name, "id", hello.MachineID)
			return nil, nil
		}
	}
	c.processedHellos.Store(dedupKey, time.Now())

	// 3. Register
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rack, err := c.reg.Register(ctx, hello.MachineID, hello.Name, hello.Secret, hello.IP, hello.Port, hello.Version, hello.Config, c.mixerID)
	if err != nil {
		c.logger.Error("failed to register rack", "error", err)
		if err == registry.ErrNameConflict {
			// Do not retry on name conflict, it requires manual intervention
			return nil, nil
		}
		if err == registry.ErrBootstrapSecretMismatch {
			// The caller's MachineID is unknown to us (fresh, or its prior
			// registration was removed by an admin) and it did not present
			// the current bootstrap secret: most often this is a Rack that
			// still holds a passport for an identity we no longer recognize.
			// Tell it explicitly, rather than staying silent, so it drops
			// that stale identity and re-provisions instead of concluding
			// the Mixer is merely unreachable and resuming offline forever.
			c.publishDenied(hello.Name, hello.Nonce)
			return nil, nil
		}
		// For other errors (DB down), we might want Watermill to retry
		return nil, err
	}

	c.logger.Info("Rack Registered", "id", rack.MachineID, "name", rack.Name, "status", rack.Status)

	// 4. Issue Passport (StateEnvelope)
	// Construct Rack State with lifecycle metadata
	rackState := pki.RackState{
		MixerID:     c.mixerID,
		MachineID:   rack.MachineID,
		Name:        rack.Name,
		Status:      rack.Status,
		Secret:      rack.Secret,
		MixerPublic: c.signer.Public,
		Version:     version.Version,
		CreatedAt:   rack.FirstSeen.Unix(),
		UpdatedAt:   rack.LastSeen.Unix(),
		UpdateCount: rack.UpdateCount,
	}

	// Create Envelope and Sign
	envelope, err := c.signer.Sign(&rackState)
	if err != nil {
		c.logger.Error("failed to sign state", "error", err)
		return nil, err
	}

	// Serialize Envelope
	envBytes, err := cbor.Marshal(envelope)
	if err != nil {
		c.logger.Error("failed to marshal envelope", "error", err)
		return nil, err
	}

	// Publish Response
	// Topic: fluxrig.agent.enrollment.<Name>.<Nonce>. Both are caller-supplied;
	// sanitized so a name or nonce containing "." (a subject separator) or a
	// wildcard cannot reroute this reply onto, or across, another subject.
	topic := fmt.Sprintf("flux.agent.enrollment.%s.%s", sanitizeName(hello.Name), sanitizeName(hello.Nonce))

	// Prepare HelloResponse
	resp := fluxmsg.HelloResponse{
		Status:   rack.Status,
		Passport: envBytes,
		Message:  fmt.Sprintf("Welcome to fluxrig. Status: %s", rack.Status),
	}
	respData, _ := resp.ToData() // Convert to map[string]any

	respMsg := fluxmsg.New()
	respMsg.Data = respData // Send as Data payload

	respBytes, err := cbor.Marshal(respMsg)
	if err != nil {
		return nil, err
	}

	pubMsg := message.NewMessage(watermill.NewUUID(), respBytes)

	if err := c.publisher.Publish(topic, pubMsg); err != nil {
		c.logger.Error("failed to publish enrollment response", "topic", topic, "error", err)
		return nil, err
	}

	c.logger.Info("Issued Passport", "name", rack.Name, "topic", topic)

	// 5. If there is an active scenario, push it to the rack immediately
	if c.scenario != nil {
		go func() {
			// Delay to ensure Rack has subscribed to its scenario topic
			if c.pushDelay > 0 {
				time.Sleep(c.pushDelay)
			}
			if err := c.scenario.PushActiveToRack(context.Background(), rack.Name); err != nil {
				c.logger.Warn("Failed to push active scenario to new rack", "rack", rack.Name, "error", err)
			}
		}()
	}

	return nil, nil
}

// publishDenied tells a Rack its Hello was rejected, on the same reply topic
// a successful Hello would have used, with an empty Passport and a "denied"
// status. Without this, a Rack whose MachineID we don't recognize (a fresh
// one with the wrong secret, or one whose registration was removed) gets no
// reply at all, which is indistinguishable on its side from the Mixer being
// unreachable, so it eventually resumes offline with its stale identity
// forever, instead of dropping it and re-provisioning as a new Zero-Config
// identity. A publish failure here is logged, not returned: the caller
// already decided not to have Watermill retry this Hello.
func (c *EnrollmentController) publishDenied(name, nonce string) {
	topic := fmt.Sprintf("flux.agent.enrollment.%s.%s", sanitizeName(name), sanitizeName(nonce))

	resp := fluxmsg.HelloResponse{
		Status:  "denied",
		Message: "registration denied: unrecognized identity or invalid bootstrap secret",
	}
	respData, err := resp.ToData()
	if err != nil {
		c.logger.Error("failed to build denial response", "error", err)
		return
	}

	respMsg := fluxmsg.New()
	respMsg.Data = respData

	respBytes, err := cbor.Marshal(respMsg)
	if err != nil {
		c.logger.Error("failed to marshal denial response", "error", err)
		return
	}

	if err := c.publisher.Publish(topic, message.NewMessage(watermill.NewUUID(), respBytes)); err != nil {
		c.logger.Error("failed to publish denial response", "topic", topic, "error", err)
	}
}

func (c *EnrollmentController) HandleHeartbeat(msg *message.Message) ([]*message.Message, error) {
	var fm fluxmsg.FluxMsg
	if err := cbor.Unmarshal(msg.Payload, &fm); err != nil {
		return nil, nil
	}

	hb, err := fluxmsg.ParseHeartbeat(fm.Data)
	if err != nil {
		c.logger.Warn("invalid heartbeat payload", "error", err)
		return nil, nil
	}

	// Authenticate before touching any state: without this, any caller who
	// learned a MachineID (formerly disclosed by the unauthenticated /racks
	// endpoint) could forge liveness and overwrite that Rack's stats and
	// config with none of its own.
	rack, err := c.reg.Get(context.Background(), hb.MachineID)
	if err != nil {
		return nil, nil
	}
	if !security.SecretsEqual(rack.Secret, hb.Secret) {
		c.logger.Warn("Heartbeat rejected: secret mismatch", "id", hb.MachineID)
		return nil, nil
	}

	// Update LastSeen
	c.logger.Debug("Heartbeat", "id", hb.MachineID)
	if errHB := c.reg.Heartbeat(context.Background(), hb.MachineID, hb.Stats, hb.Config); errHB != nil {
		return nil, nil
	}

	// Prepare Response
	resp := fluxmsg.HeartbeatResponse{
		Status:  rack.Status,
		Command: "",
	}
	if rack.Status == "inactive" {
		resp.Command = "sleep"
	}

	respData, _ := resp.ToData()

	respMsg := fluxmsg.New()
	respMsg.Data = respData
	respMsg.SrcGearID = uuid.Nil

	respBytes, err := cbor.Marshal(respMsg)
	if err != nil {
		return nil, err
	}

	// Reply with Status Update via Notification Topic
	// Racks listen to 'fluxrig.agent.notify.<ID>' for status changes.
	topic := fmt.Sprintf("flux.agent.notify.%s", hb.MachineID)

	pubMsg := message.NewMessage(watermill.NewUUID(), respBytes)
	if err := c.publisher.Publish(topic, pubMsg); err != nil {
		c.logger.Error("failed to publish heartbeat response", "topic", topic, "error", err)
	}

	return nil, nil
}
