// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The Mixer keeps only a hash of a Rack's secret, so only the first passport
// (fresh registration) carries the plaintext; every re-issue carries none.
// The passport is signed and cannot be edited to add it back, so the Rack
// keeps its secret in a file beside the passport: base.state_file with a
// .secret suffix, readable by the Rack's user only.

func secretPath(statePath string) string {
	return statePath + ".secret"
}

// storePassportSecret keeps secret beside the passport when the passport
// carried one. An empty secret leaves the kept one untouched.
func storePassportSecret(statePath, secret string) error {
	if secret == "" {
		return nil
	}
	path := secretPath(statePath)
	tmp, err := os.CreateTemp(filepath.Dir(path), ".secret-*")
	if err != nil {
		return fmt.Errorf("store rack secret: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("store rack secret: %w", err)
	}
	if _, err := tmp.WriteString(secret); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("store rack secret: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("store rack secret: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("store rack secret: %w", err)
	}
	return nil
}

// rackSecret returns the secret this Rack presents: the passport's own when it
// carries one, otherwise the one kept beside it. An empty result means the
// Rack has none and must enroll again.
func rackSecret(statePath, passportSecret string) string {
	if passportSecret != "" {
		return passportSecret
	}
	b, err := os.ReadFile(secretPath(statePath)) //nolint:gosec // path derives from base.state_file
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// removeRackSecret deletes the kept secret along with a dropped identity.
func removeRackSecret(statePath string) error {
	if err := os.Remove(secretPath(statePath)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
