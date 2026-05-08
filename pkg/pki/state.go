// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package pki

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fxamacker/cbor/v2"
	"github.com/google/uuid"
)

// --- Mixer Side ---

// ClusterKey represents the Authority's Keypair.
type ClusterKey struct {
	Private ed25519.PrivateKey // 64 bytes
	Public  ed25519.PublicKey  // 32 bytes
}

// GenerateClusterKey creates a new random ClusterKey.
func GenerateClusterKey() (*ClusterKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &ClusterKey{
		Private: priv,
		Public:  pub,
	}, nil
}

// Sign creates a StateEnvelope for a Rack.
func (c *ClusterKey) Sign(state *RackState) (*StateEnvelope, error) {
	// 1. Serialize Payload
	payload, err := cbor.Marshal(state)
	if err != nil {
		return nil, err
	}

	// 2. Sign Payload
	sig := ed25519.Sign(c.Private, payload)

	return &StateEnvelope{
		Payload:   payload,
		Signature: sig,
	}, nil
}

// Save writes the ClusterKey to disk.
func (c *ClusterKey) Save(path string) error {
	// We save the private key as raw bytes (seed or private key bytes).
	return os.WriteFile(path, c.Private, 0600)
}

// LoadClusterKey loads the private key from disk.
func LoadClusterKey(path string) (*ClusterKey, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}

	// Assume raw bytes for now. If len is 64 (Private), OK. If 32 (Seed), NewKeyFromSeed.
	var priv ed25519.PrivateKey
	if len(data) == ed25519.SeedSize {
		priv = ed25519.NewKeyFromSeed(data)
	} else if len(data) == ed25519.PrivateKeySize {
		priv = ed25519.PrivateKey(data)
	} else {
		// Try Hex decode?
		decoded := make([]byte, hex.DecodedLen(len(data)))
		if _, err := hex.Decode(decoded, data); err == nil {
			if len(decoded) == ed25519.PrivateKeySize {
				priv = ed25519.PrivateKey(decoded)
			}
		}
	}

	if priv == nil {
		return nil, fmt.Errorf("invalid key length: %d", len(data))
	}

	return &ClusterKey{
		Private: priv,
		Public:  priv.Public().(ed25519.PublicKey),
	}, nil
}

// --- Rack Side ---

// StateEnvelope is the signed passport persisted on the Rack.
type StateEnvelope struct {
	Payload   []byte // CBOR(RackState)
	Signature []byte // Sig(Payload)
}

// RackState is the Core Identity Data.
// A Rack is always associated with a specific Mixer (logical tenant).
// The Scenario field is optional and contains the projected scenario for this rack.
// When present, it is signed along with the identity for tamper-proof storage.
type RackState struct {
	MixerID     uuid.UUID         `cbor:"mixer_id"`     // Mixer's fluxEntityID (binary)
	MachineID   uuid.UUID         `cbor:"machine_id"`   // Rack's unique machine ID
	Name        string            `cbor:"name"`         // Rack's display name
	Status      string            `cbor:"status"`       // e.g. "pending", "active"
	Secret      string            `cbor:"secret"`       // Bearer Token
	MixerPublic ed25519.PublicKey `cbor:"mixer_pub"`    // Mixer's signing key (for verification)
	Version     string            `cbor:"version"`      // Service version of the issuing Mixer
	CreatedAt   int64             `cbor:"created_at"`   // Unix timestamp of initial issuance
	UpdatedAt   int64             `cbor:"updated_at"`   // Unix timestamp of last update
	UpdateCount int               `cbor:"update_count"` // Number of times this passport was re-issued
	Scenario    []byte            `cbor:"scenario"`     // YAML-encoded projected scenario (optional)
	ScenarioVer string            `cbor:"scenario_ver"` // Scenario version for quick check
}

// Verify checks the envelope's signature using the embedded Public Key.
func (e *StateEnvelope) Verify() (*RackState, error) {
	// 1. Unmarshal Payload to get Public Key
	var state RackState
	if err := cbor.Unmarshal(e.Payload, &state); err != nil {
		return nil, fmt.Errorf("invalid payload format: %w", err)
	}

	if len(state.MixerPublic) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid mixer public key in state")
	}

	// 2. Verify Signature
	if !ed25519.Verify(state.MixerPublic, e.Payload, e.Signature) {
		return nil, fmt.Errorf("signature verification failed! state is tampered")
	}

	return &state, nil
}

// VerifyMixer checks the envelope's signature using a provided Cluster Public Key.
// Use this for mixer.flux files.
func (e *StateEnvelope) VerifyMixer(pub ed25519.PublicKey) (*MixerState, error) {
	if !ed25519.Verify(pub, e.Payload, e.Signature) {
		return nil, fmt.Errorf("signature verification failed! state is tampered")
	}

	var state MixerState
	if err := cbor.Unmarshal(e.Payload, &state); err != nil {
		return nil, fmt.Errorf("invalid mixer payload format: %w", err)
	}
	return &state, nil
}

// Save writes the envelope to disk.
func (e *StateEnvelope) Save(path string) error {
	data, err := cbor.Marshal(e)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// LoadStateEnvelope reads the envelope from disk.
func LoadStateEnvelope(path string) (*StateEnvelope, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	var env StateEnvelope
	if err := cbor.Unmarshal(data, &env); err != nil {
		return nil, err
	}
	return &env, nil
}

// MixerState represents the sovereign identity of the Authority node.
type MixerState struct {
	MachineID   uuid.UUID `cbor:"machine_id"`
	Name        string    `cbor:"name"`
	Version     string    `cbor:"version"`
	CreatedAt   int64     `cbor:"created_at"`
	UpdatedAt   int64     `cbor:"updated_at"`
	UpdateCount int       `cbor:"update_count"`
}

// SignMixer signs the Mixer's own identity using the Cluster Key.
func (c *ClusterKey) SignMixer(state *MixerState) (*StateEnvelope, error) {
	payload, err := cbor.Marshal(state)
	if err != nil {
		return nil, err
	}
	sig := ed25519.Sign(c.Private, payload)
	return &StateEnvelope{
		Payload:   payload,
		Signature: sig,
	}, nil
}

// LoadMixerState reads and verifies the Mixer's identity envelope.
func LoadMixerState(path string, clusterPub ed25519.PublicKey) (*MixerState, error) {
	env, err := LoadStateEnvelope(path)
	if err != nil {
		return nil, err
	}

	if !ed25519.Verify(clusterPub, env.Payload, env.Signature) {
		return nil, fmt.Errorf("mixer identity verification failed! passport is tampered")
	}

	var state MixerState
	if err := cbor.Unmarshal(env.Payload, &state); err != nil {
		return nil, err
	}
	return &state, nil
}
