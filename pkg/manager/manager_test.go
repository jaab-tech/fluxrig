package manager

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManager_Lifecycle(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flux-mgr-test")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	mgr, err := NewManager(tmpDir)
	require.NoError(t, err)

	ctx := context.Background()

	// Create dummy spec file
	specPath := filepath.Join(tmpDir, "visa_v1.yaml")
	err = os.WriteFile(specPath, []byte("type: iso8583"), 0600)
	require.NoError(t, err)

	// 1. Import
	hash, _, _, err := mgr.Import(ctx, specPath, "visa", "v1.0.0")
	require.NoError(t, err)
	assert.NotEmpty(t, hash)

	// 2. Load by Hash
	content, err := mgr.Load(ctx, hash)
	require.NoError(t, err)
	assert.Equal(t, "type: iso8583", string(content))

	// 3. Load by Name:Tag
	content, err = mgr.Load(ctx, "visa:v1.0.0")
	require.NoError(t, err)
	assert.Equal(t, "type: iso8583", string(content))

	// 4. Import v2
	specPath2 := filepath.Join(tmpDir, "visa_v2.yaml")
	err = os.WriteFile(specPath2, []byte("type: iso8583_v2"), 0600)
	require.NoError(t, err)
	_, _, _, err = mgr.Import(ctx, specPath2, "visa", "v2.0.0")
	require.NoError(t, err)

	// 5. Load "latest"
	// semver: v2.0.0 > v1.0.0
	content, err = mgr.Load(ctx, "visa:latest")
	require.NoError(t, err)
	assert.Equal(t, "type: iso8583_v2", string(content))

	// 6. List
	list, err := mgr.List(ctx)
	require.NoError(t, err)
	assert.Len(t, list, 2) // v1.0.0 and v2.0.0

	// 7. Overwrite Protection
	// Try to import modified content with existing tag v1.0.0
	specPath3 := filepath.Join(tmpDir, "visa_v1_mod.yaml")
	err = os.WriteFile(specPath3, []byte("type: modified"), 0600)
	require.NoError(t, err)
	_, _, _, err = mgr.Import(ctx, specPath3, "visa", "v1.0.0")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestManager_Export(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flux-mgr-export")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	mgr, err := NewManager(tmpDir)
	require.NoError(t, err)

	ctx := context.Background()

	// Import a spec
	specPath := filepath.Join(tmpDir, "payment.yaml")
	specContent := "name: payment\nversion: v1.0.0\ntype: iso8583"
	err = os.WriteFile(specPath, []byte(specContent), 0600)
	require.NoError(t, err)

	_, _, _, err = mgr.Import(ctx, specPath, "payment", "v1.0.0")
	require.NoError(t, err)

	// Export by name:tag
	exportPath := filepath.Join(tmpDir, "exported", "payment_out.yaml")
	err = mgr.Export(ctx, "payment:v1.0.0", exportPath)
	require.NoError(t, err)

	// Verify round-trip
	exported, err := os.ReadFile(filepath.Clean(exportPath))
	require.NoError(t, err)
	assert.Equal(t, specContent, string(exported))
}

func TestManager_ImportScenario(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flux-mgr-scenario")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	mgr, err := NewManager(tmpDir)
	require.NoError(t, err)

	ctx := context.Background()

	// Import a scenario
	scenarioPath := filepath.Join(tmpDir, "flow.yaml")
	scenarioContent := "name: payment-flow\nversion: v1.0.0"
	err = os.WriteFile(scenarioPath, []byte(scenarioContent), 0600)
	require.NoError(t, err)

	hash, name, tag, err := mgr.ImportScenario(ctx, scenarioPath, "payment-flow", "v1.0.0")
	require.NoError(t, err)
	assert.NotEmpty(t, hash)
	assert.Equal(t, "payment-flow", name)
	assert.Equal(t, "v1.0.0", tag)

	// Load via scenario index
	content, err := mgr.Load(ctx, "payment-flow:v1.0.0")
	require.NoError(t, err)
	assert.Equal(t, scenarioContent, string(content))

	// List should include it
	list, err := mgr.List(ctx)
	require.NoError(t, err)
	assert.Len(t, list, 1)
}

func TestManager_AutoIncrement(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flux-mgr-autoinc")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	mgr, err := NewManager(tmpDir)
	require.NoError(t, err)

	ctx := context.Background()

	// Import without explicit tag — should auto-increment
	specPath := filepath.Join(tmpDir, "auto.yaml")
	err = os.WriteFile(specPath, []byte("type: auto"), 0600)
	require.NoError(t, err)

	// First import: no tags exist → should default to v0.1.0
	_, _, tag1, err := mgr.Import(ctx, specPath, "auto", "")
	require.NoError(t, err)
	assert.Equal(t, "v0.1.0", tag1)

	// Second import (different content): should increment to v0.2.0
	specPath2 := filepath.Join(tmpDir, "auto2.yaml")
	err = os.WriteFile(specPath2, []byte("type: auto_v2"), 0600)
	require.NoError(t, err)

	_, _, tag2, err := mgr.Import(ctx, specPath2, "auto", "")
	require.NoError(t, err)
	assert.Equal(t, "v0.2.0", tag2)
}

func TestManager_LatestSemverOrdering(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flux-mgr-latest")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	mgr, err := NewManager(tmpDir)
	require.NoError(t, err)

	ctx := context.Background()

	// Import v2.0.0 FIRST
	p1 := filepath.Join(tmpDir, "v2.yaml")
	err = os.WriteFile(p1, []byte("v2 content"), 0600)
	require.NoError(t, err)
	_, _, _, err = mgr.Import(ctx, p1, "spec", "v2.0.0")
	require.NoError(t, err)

	// Import v1.0.0 SECOND (out of order)
	p2 := filepath.Join(tmpDir, "v1.yaml")
	err = os.WriteFile(p2, []byte("v1 content"), 0600)
	require.NoError(t, err)
	_, _, _, err = mgr.Import(ctx, p2, "spec", "v1.0.0")
	require.NoError(t, err)

	// "latest" should resolve to v2.0.0 (highest semver), NOT v1.0.0 (last imported)
	content, err := mgr.Load(ctx, "spec:latest")
	require.NoError(t, err)
	assert.Equal(t, "v2 content", string(content), "latest should resolve to highest semver, not last imported")
}

func TestManager_CorruptIndex(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flux-mgr-corrupt")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Write a corrupt index.json
	indexPath := filepath.Join(tmpDir, "index.json")
	err = os.WriteFile(indexPath, []byte("{invalid json"), 0600)
	require.NoError(t, err)

	// NewManager should fail gracefully
	_, err = NewManager(tmpDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "corrupt index.json")
}
