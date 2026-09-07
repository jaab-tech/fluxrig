// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package manager

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func specDoc(id, version string) []byte {
	return []byte("spec:\n  id: " + id + "\n  name: The " + id + " Protocol\n  version: \"" + version + "\"\n  protocol: iso8583\n")
}

func scenarioDoc(name, version string) []byte {
	return []byte("meta:\n  name: " + name + "\n  version: \"" + version + "\"\n")
}

func storeWith(t *testing.T, f func(m *DefaultManager)) *DefaultManager {
	t.Helper()
	m, err := NewManager(t.TempDir())
	require.NoError(t, err)
	f(m)
	return m
}

// The store holds specs and scenarios in one index, and List returned both with
// nothing saying which was which -- so `fluxrig spec list` showed a scenario as
// a spec, and so did the API. Nothing errored; it just answered wrongly.
func TestAListingSaysWhatKindOfThingItIsListing(t *testing.T) {
	ctx := context.Background()
	m := storeWith(t, func(m *DefaultManager) {
		_, _, _, err := m.ImportContent(ctx, specDoc("acme", "1.0.0"), "", "")
		require.NoError(t, err)
		tmp := filepath.Join(t.TempDir(), "s.yaml")
		require.NoError(t, os.WriteFile(tmp, scenarioDoc("nightly", "1.0.0"), 0o600))
		_, _, _, err = m.ImportScenario(ctx, tmp, "", "")
		require.NoError(t, err)
	})

	list, err := m.List(ctx)
	require.NoError(t, err)
	require.Len(t, list, 2)

	byName := map[string]ArtifactInfo{}
	for _, a := range list {
		byName[a.Name] = a
	}
	require.Equal(t, KindSpec, byName["acme"].Kind)
	require.Equal(t, KindScenario, byName["nightly"].Kind)
}

// A listing is worth having only if it says more than the reference already
// does. These are the attributes the store actually recorded.
func TestAListingCarriesWhatTheStoreRecorded(t *testing.T) {
	ctx := context.Background()
	before := time.Now().UTC().Add(-time.Second)
	doc := specDoc("acme", "1.0.0")
	m := storeWith(t, func(m *DefaultManager) {
		_, _, _, err := m.ImportContent(ctx, doc, "", "")
		require.NoError(t, err)
	})

	list, err := m.List(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	a := list[0]

	require.Equal(t, "acme", a.Name)
	require.Equal(t, "v1.0.0", a.Tag)
	require.Equal(t, "The acme Protocol", a.Title, "the human title is not the reference and both are wanted")
	require.Equal(t, "iso8583", a.Protocol)
	require.Equal(t, int64(len(doc)), a.Size)
	require.True(t, a.ImportedAt.After(before), "no import date was recorded")
	require.True(t, a.Latest, "the only version is the one `latest` resolves to")
}

// Re-importing the same bytes creates nothing, so it must not restate when the
// artefact was filed. The date belongs to the artefact, not to the last time
// someone ran the command.
func TestReimportingKeepsTheOriginalDate(t *testing.T) {
	ctx := context.Background()
	m := storeWith(t, func(m *DefaultManager) {
		_, _, _, err := m.ImportContent(ctx, specDoc("acme", "1.0.0"), "", "")
		require.NoError(t, err)
	})
	first, err := m.List(ctx)
	require.NoError(t, err)

	time.Sleep(5 * time.Millisecond) // distinguishable clock reading, not a wait for a condition
	_, _, _, err = m.ImportContent(ctx, specDoc("acme", "1.0.0"), "", "")
	require.NoError(t, err)

	again, err := m.List(ctx)
	require.NoError(t, err)
	require.Equal(t, first[0].ImportedAt, again[0].ImportedAt)
}

// Newest means the highest version, not the last imported: a patch to an older
// branch arrives after a newer release and is not newer than it.
func TestHistoryIsOrderedByVersionNotByArrival(t *testing.T) {
	ctx := context.Background()
	m := storeWith(t, func(m *DefaultManager) {
		for _, v := range []string{"1.0.0", "2.0.0", "1.1.0", "1.0.1"} {
			_, _, _, err := m.ImportContent(ctx, specDoc("acme", v), "", "")
			require.NoError(t, err)
		}
	})

	hist, err := m.History(ctx, KindSpec, "acme")
	require.NoError(t, err)

	var tags []string
	for _, a := range hist {
		tags = append(tags, a.Tag)
	}
	require.Equal(t, []string{"v2.0.0", "v1.1.0", "v1.0.1", "v1.0.0"}, tags)
	// v1.0.1 was imported last and is not the latest.
	require.True(t, hist[0].Latest)
	require.False(t, hist[2].Latest)
}

func TestHistoryOfSomethingTheStoreDoesNotHold(t *testing.T) {
	m := storeWith(t, func(m *DefaultManager) {})
	_, err := m.History(context.Background(), KindSpec, "nothing-here")
	require.Error(t, err)
	require.Contains(t, err.Error(), "nothing-here")
}

// A store written before attributes existed still loads, and says it does not
// know rather than inventing a date. A blob file's timestamp would look like an
// answer and be a different fact: copying a store rewrites every one of them.
func TestAnIndexWithoutAttributesStillLists(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	m, err := NewManager(dir)
	require.NoError(t, err)
	_, _, _, err = m.ImportContent(ctx, specDoc("acme", "1.0.0"), "", "")
	require.NoError(t, err)

	// Strip the attributes, as an index from before this feature has none.
	path := filepath.Join(dir, "index.json")
	raw, err := os.ReadFile(path) //nolint:gosec // a path this test just wrote
	require.NoError(t, err)
	var idx map[string]any
	require.NoError(t, json.Unmarshal(raw, &idx))
	delete(idx, "attrs")
	out, err := json.Marshal(idx)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, out, 0o600))
	require.False(t, strings.Contains(string(out), "attrs"))

	reopened, err := NewManager(dir)
	require.NoError(t, err)
	list, err := reopened.List(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, "acme", list[0].Name)
	require.True(t, list[0].ImportedAt.IsZero(), "a date was invented for an artefact that has none")
	require.Zero(t, list[0].Size)
}
