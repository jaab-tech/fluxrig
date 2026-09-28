// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package pki

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
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

// SignBytes signs arbitrary raw bytes using the Cluster Key.
func (c *ClusterKey) SignBytes(payload []byte) ([]byte, error) {
	return ed25519.Sign(c.Private, payload), nil
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

// ErrPinMismatch means the envelope's claimed Mixer key does not match the
// pinned trust anchor. This differs from a corrupt or tampered envelope,
// such as a bad signature or a bad payload. A pin mismatch on a
// well-formed, correctly-signed envelope looks like a Mixer key rotation.
// It also looks like an attacker's own key. This file cannot distinguish
// the two cases. An operator must decide whether to accept the new key.
// See PinnedKeyPath.
var ErrPinMismatch = errors.New("mixer public key does not match the pinned trust anchor")

// Verify checks the envelope's signature using the embedded Public Key.
//
// This trusts whatever Mixer key the payload itself claims: an envelope that
// carries its own key and a signature made with the matching private key
// verifies here regardless of who wrote it. That makes Verify safe only for
// a first, trust-on-first-use acceptance (initial enrollment) or for
// diagnostics on a file whose origin is not being trusted for anything.
// Every other caller must use VerifyPinned against a key it obtained
// independently of this payload.
func (e *StateEnvelope) Verify() (*RackState, error) {
	return e.VerifyPinned(nil)
}

// VerifyPinned checks the envelope's signature, and, when pinned is not
// empty, requires the payload's embedded Mixer key to equal it before the
// signature is even checked. This is what keeps a locally pinned trust
// anchor meaningful: without it, an envelope supplies both the key and the
// signature that key must match, so anyone who can write an envelope can
// always make it verify, no matter which private key they hold.
//
// Pass nil for pinned only on the first envelope ever accepted for a given
// identity (trust-on-first-use); the caller is expected to persist the
// returned state's MixerPublic as the pin for every verification after that.
func (e *StateEnvelope) VerifyPinned(pinned ed25519.PublicKey) (*RackState, error) {
	// 1. Unmarshal Payload to get Public Key
	var state RackState
	if err := cbor.Unmarshal(e.Payload, &state); err != nil {
		return nil, fmt.Errorf("invalid payload format: %w", err)
	}

	if len(state.MixerPublic) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid mixer public key in state")
	}

	// 2. The payload's own claimed key must match the pinned trust anchor,
	// not just itself.
	if len(pinned) > 0 && !bytes.Equal(pinned, state.MixerPublic) {
		return nil, fmt.Errorf("%w: state is untrusted", ErrPinMismatch)
	}

	// 3. Verify Signature
	if !ed25519.Verify(state.MixerPublic, e.Payload, e.Signature) {
		return nil, fmt.Errorf("signature verification failed! state is tampered")
	}

	return &state, nil
}

// PinnedKeyPath returns the sidecar path that holds the Mixer public key
// pinned for the passport at statePath, independent of the passport payload
// itself so a rewritten passport cannot also supply the key it is checked
// against.
func PinnedKeyPath(statePath string) string {
	return statePath + ".pub"
}

// LoadPinnedMixerKey reads the Mixer public key previously pinned for
// statePath. It returns (nil, nil) when nothing has been pinned yet, which
// is expected before the first passport a Rack ever accepts.
func LoadPinnedMixerKey(statePath string) (ed25519.PublicKey, error) {
	data, err := os.ReadFile(filepath.Clean(PinnedKeyPath(statePath)))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid pinned mixer key length: %d", len(data))
	}
	return ed25519.PublicKey(data), nil
}

// PinMixerKey persists pub as the trust anchor for statePath.
func PinMixerKey(statePath string, pub ed25519.PublicKey) error {
	return os.WriteFile(PinnedKeyPath(statePath), pub, 0600)
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
