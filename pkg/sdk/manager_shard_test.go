// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package sdk

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jaab-tech/fluxrig/pkg/manager"
)

func TestDefaultContext_Certification(t *testing.T) {
	mockMgr := &mockManager{}
	c := NewDefaultContext(mockMgr)

	t.Run("Getters", func(t *testing.T) {
		assert.Equal(t, mockMgr, c.Manager())
		assert.Nil(t, c.Context())
		assert.Nil(t, c.Config())
		assert.Equal(t, "", c.GearName())
		assert.Equal(t, uint64(0), c.MachineID())
		assert.Nil(t, c.Logger())
		assert.Nil(t, c.IDGen())
		assert.Nil(t, c.Bus())
	})
}

// ------ Mocks ------

type mockManager struct {
	manager.Manager
}

func (m *mockManager) Import(ctx context.Context, filePath, name, tag string) (string, string, string, error) {
	return "", "", "", nil
}
func (m *mockManager) Load(ctx context.Context, urn string) ([]byte, error)     { return nil, nil }
func (m *mockManager) Export(ctx context.Context, urn, outputPath string) error { return nil }
func (m *mockManager) List(ctx context.Context) ([]manager.ArtifactInfo, error) { return nil, nil }
func (m *mockManager) ImportScenario(ctx context.Context, filePath, name, tag string) (string, string, string, error) {
	return "", "", "", nil
}
