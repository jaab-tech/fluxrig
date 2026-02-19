// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package security

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Data Privacy & Compliance
//
// fluxrig handles sensitive financial data (PAN, CVV, etc.). This package provides
// utilities for data protection:
//
//   - Masking: Obscure sensitive fields before logging/storage (e.g., PAN → "411111******1234")
//   - Filtering: Remove fields entirely based on compliance rules (e.g., CVV never stored)
//   - Encryption: Field-level encryption for data at rest (future)
//
// Compliance Standards:
//   - PCI-DSS: Primary card number protection
//   - GDPR: Right to erasure, data minimization
//
// Data Classification:
//   - PCI Scope: PAN, CVV, Track Data, PIN Block
//   - PII: Cardholder name, address, email
//   - Non-sensitive: Transaction amounts, timestamps, result codes
//
// Implementation Strategy:
//   - Edge Masking: Sensitive data masked at Input Gear before telemetry emission
//   - Raw Preservation: Full payload stored in encrypted Parquet (audit requirement)
//   - Promoted Fields: Only non-sensitive fields promoted to OpenSearch
//
// Reference: ops/docs/public/2_architecture/security.md (Data Privacy section)

// Verifier validates signatures on configuration/state bundles.
// Reference: ops/docs/public/2_architecture/security.md (Offline Trust)
type Verifier struct {
	clusterPubKey ed25519.PublicKey
}

// NewVerifier creates a verifier from a raw 32-byte public key.
func NewVerifier(pubKeyBytes []byte) (*Verifier, error) {
	if len(pubKeyBytes) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid public key size: need %d, got %d", ed25519.PublicKeySize, len(pubKeyBytes))
	}
	return &Verifier{
		clusterPubKey: ed25519.PublicKey(pubKeyBytes),
	}, nil
}

// LoadVerifierFromFile loads the cluster public key from disk (TOFU).
func LoadVerifierFromFile(path string) (*Verifier, error) {
	cnt, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	// In a real implementation we might have PEM decoding here.
	// For "Clean Slate", assuming raw bytes or simple handling for now.

	if len(cnt) != ed25519.PublicKeySize {
		return nil, errors.New("key file invalid size (expected 32 bytes raw)")
	}

	return NewVerifier(cnt)
}

// Verify checks if the data was signed by the cluster private key.
func (v *Verifier) Verify(data []byte, signature []byte) bool {
	if len(signature) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(v.clusterPubKey, data, signature)
}

// MaskPAN masks a Primary Account Number, showing first 6 and last 4 digits.
// Example: "4111111111111111" → "411111******1111"
func MaskPAN(pan string) string {
	if len(pan) < 13 {
		return "************"
	}
	return pan[:6] + "******" + pan[len(pan)-4:]
}

// MaskCVV always returns masked CVV (never expose).
func MaskCVV(_ string) string {
	return "***"
}
