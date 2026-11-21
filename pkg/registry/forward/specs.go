// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package forward

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jaab-tech/fluxrig/pkg/manager"
)

// SpecImporter is what the holder runs a forwarded spec import against.
type SpecImporter interface {
	ImportContent(ctx context.Context, content []byte, name, tag string) (hash, finalName, finalTag string, err error)
}

// Specs wraps a spec store whose imports are control writes. When this Mixer
// does not hold the lease, an import is forwarded to the holder, so one
// writer allocates versions for the whole cluster. Reads stay local: the
// store they read is the shared one.
type Specs struct {
	manager.Manager
	fwd    *Forwarder
	holder Holder
}

// NewSpecs wraps local. holder nil means pass-through.
func NewSpecs(local manager.Manager, fwd *Forwarder, holder Holder) *Specs {
	return &Specs{Manager: local, fwd: fwd, holder: holder}
}

func (s *Specs) forwarding() bool {
	return s.fwd != nil && s.holder != nil && !s.holder.Held()
}

// ImportContent imports locally on the holder, and forwards elsewhere.
func (s *Specs) ImportContent(ctx context.Context, content []byte, name, tag string) (string, string, string, error) {
	if !s.forwarding() {
		return s.Manager.ImportContent(ctx, content, name, tag)
	}
	resp, err := s.fwd.Do(ctx, Request{Op: OpImportSpec, SpecContent: content, SpecName: name, SpecTag: tag})
	if err != nil {
		return "", "", "", err
	}
	if errResp := ErrorOf(resp.ErrorCode, resp.ErrorText); errResp != nil {
		return "", "", "", errResp
	}
	return resp.SpecHash, resp.SpecName, resp.SpecTag, nil
}

// Import reads a file and imports it as ImportContent does. A document that
// declares no name takes the file name.
func (s *Specs) Import(ctx context.Context, filePath, name, tag string) (string, string, string, error) {
	content, err := os.ReadFile(filepath.Clean(filePath))
	if err != nil {
		return "", "", "", fmt.Errorf("forward: read %s: %w", filePath, err)
	}
	if name == "" && manager.ParseHeader(content).Name == "" {
		base := filepath.Base(filePath)
		name = strings.TrimSuffix(base, filepath.Ext(base))
	}
	return s.ImportContent(ctx, content, name, tag)
}

var _ manager.Manager = (*Specs)(nil)
