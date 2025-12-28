package controller

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/pki"
	"github.com/jaab-tech/fluxrig/pkg/registry"
	"github.com/jaab-tech/fluxrig/pkg/router"
	"github.com/vmihailenco/msgpack/v5"
)

// EnrollmentController handles Rack registration and heartbeats.
type EnrollmentController struct {
	reg             registry.Registry
	publisher       message.Publisher
	signer          *pki.ClusterKey
	processedHellos sync.Map // Deduplication cache: Name -> time.Time
}

func NewEnrollmentController(reg registry.Registry, pub message.Publisher, signer *pki.ClusterKey) *EnrollmentController {
	return &EnrollmentController{
		reg:       reg,
		publisher: pub,
		signer:    signer,
	}
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
	// 1. Unmarshal FluxMsg
	var fm fluxmsg.FluxMsg
	if err := msgpack.Unmarshal(msg.Payload, &fm); err != nil {
		slog.Error("failed to unmarshal hello", "error", err)
		return nil, nil // Don't retry malformed
	}

	// 2. Parse Hello Payload
	hello, err := fluxmsg.ParseHello(fm.Data)
	if err != nil {
		slog.Error("invalid hello payload", "error", err)
		return nil, nil
	}

	slog.Info("Received Hello", "name", hello.Name, "ip", hello.IP, "port", hello.Port)

	// Deduplication Check with TTL
	if val, loaded := c.processedHellos.Load(hello.Name); loaded {
		lastSeen := val.(time.Time)
		if time.Since(lastSeen) < 5*time.Second {
			slog.Warn("Duplicate Hello ignored (throttled)", "name", hello.Name)
			return nil, nil
		}
	}
	c.processedHellos.Store(hello.Name, time.Now())

	// 3. Register
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	rack, err := c.reg.Register(ctx, hello.Name, hello.Secret, hello.IP, hello.Port, hello.Version)
	if err != nil {
		slog.Error("failed to register rack", "error", err)
		// If DB failure, we might retry, but for now log and drop.
		return nil, err
	}

	slog.Info("Rack Registered", "id", rack.MachineID, "name", rack.Name, "status", rack.Status)

	// 4. Issue Passport (StateEnvelope)
	// Construct Rack State
	rackState := pki.RackState{
		MachineID:     rack.MachineID,
		Name:          rack.Name,
		Status:        rack.Status,
		Secret:        rack.Secret, // Embed Secret in Passport
		ClusterPublic: c.signer.Public,
	}

	// Create Envelope and Sign
	envelope, err := c.signer.Sign(&rackState)
	if err != nil {
		slog.Error("failed to sign state", "error", err)
		return nil, err
	}

	// Serialize Envelope
	envBytes, err := msgpack.Marshal(envelope)
	if err != nil {
		slog.Error("failed to marshal envelope", "error", err)
		return nil, err
	}

	// Publish Response
	// Topic: fluxrig.agent.enrollment.<Name>
	topic := fmt.Sprintf("fluxrig.agent.enrollment.%s", hello.Name)

	// Prepare HelloResponse
	resp := fluxmsg.HelloResponse{
		Status:   rack.Status,
		Passport: envBytes,
		Message:  fmt.Sprintf("Welcome to FluxRig. Status: %s", rack.Status),
	}
	respData, _ := resp.ToData() // Convert to map[string]any

	respMsg := fluxmsg.New()
	respMsg.Data = respData // Send as Data payload

	respBytes, err := msgpack.Marshal(respMsg)
	if err != nil {
		return nil, err
	}

	pubMsg := message.NewMessage(watermill.NewUUID(), respBytes)

	if err := c.publisher.Publish(topic, pubMsg); err != nil {
		slog.Error("failed to publish enrollment response", "topic", topic, "error", err)
		return nil, err
	}

	slog.Info("Issued Passport", "name", rack.Name, "topic", topic)

	return nil, nil
}

func (c *EnrollmentController) HandleHeartbeat(msg *message.Message) ([]*message.Message, error) {
	var fm fluxmsg.FluxMsg
	if err := msgpack.Unmarshal(msg.Payload, &fm); err != nil {
		return nil, nil
	}

	hb, err := fluxmsg.ParseHeartbeat(fm.Data)
	if err != nil {
		slog.Warn("invalid heartbeat payload", "error", err)
		return nil, nil
	}

	// Update LastSeen
	slog.Debug("Heartbeat", "id", hb.MachineID)
	if err := c.reg.Heartbeat(context.Background(), hb.MachineID, hb.Stats); err != nil {
		return nil, nil
	}

	rack, err := c.reg.Get(context.Background(), hb.MachineID)
	if err != nil {
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
	respMsg.SrcGearID = 0

	respBytes, err := msgpack.Marshal(respMsg)
	if err != nil {
		return nil, err
	}

	// Reply with Status Update via Notification Topic
	// Racks listen to 'fluxrig.agent.notify.<ID>' for status changes.
	topic := fmt.Sprintf("fluxrig.agent.notify.%d", hb.MachineID)

	pubMsg := message.NewMessage(watermill.NewUUID(), respBytes)
	if err := c.publisher.Publish(topic, pubMsg); err != nil {
		slog.Error("failed to publish heartbeat response", "topic", topic, "error", err)
	}

	return nil, nil
}
