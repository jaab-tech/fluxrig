package pki

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/vmihailenco/msgpack/v5"
)

// --- Mixer Side ---

// ClusterKey represents the Authority's Keypair.
type ClusterKey struct {
	Private ed25519.PrivateKey // 64 bytes
	Public  ed25519.PublicKey  // 32 bytes
}

// Sign creates a StateEnvelope for a Rack.
func (c *ClusterKey) Sign(state *RackState) (*StateEnvelope, error) {
	// 1. Serialize Payload
	payload, err := msgpack.Marshal(state)
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
	data, err := os.ReadFile(path)
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
	Payload   []byte // MsgPack(RackState)
	Signature []byte // Sig(Payload)
}

// RackState is the Core Identity Data.
type RackState struct {
	ClusterID     string            `msgpack:"cluster_id"`
	MachineID     uint16            `msgpack:"machine_id"`
	Name          string            `msgpack:"name"`
	Status        string            `msgpack:"status"`      // e.g. "pending", "active"
	Secret        string            `msgpack:"secret"`      // Bearer Token
	ClusterPublic ed25519.PublicKey `msgpack:"cluster_pub"` // Validation Root
}

// Verify checks the envelope's signature using the embedded Public Key.
func (e *StateEnvelope) Verify() (*RackState, error) {
	// 1. Unmarshal Payload to get Public Key
	var state RackState
	if err := msgpack.Unmarshal(e.Payload, &state); err != nil {
		return nil, fmt.Errorf("invalid payload format: %w", err)
	}

	if len(state.ClusterPublic) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid cluster public key in state")
	}

	// 2. Verify Signature
	if !ed25519.Verify(state.ClusterPublic, e.Payload, e.Signature) {
		return nil, fmt.Errorf("signature verification failed! state is tampered")
	}

	return &state, nil
}

// Save writes the envelope to disk.
func (e *StateEnvelope) Save(path string) error {
	data, err := msgpack.Marshal(e)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// LoadStateEnvelope reads the envelope from disk.
func LoadStateEnvelope(path string) (*StateEnvelope, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var env StateEnvelope
	if err := msgpack.Unmarshal(data, &env); err != nil {
		return nil, err
	}
	return &env, nil
}
