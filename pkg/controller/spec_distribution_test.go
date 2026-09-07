// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/manager"
	"github.com/jaab-tech/fluxrig/pkg/registry"
)

const specBody = `spec:
  id: acme-auth
  name: acme-auth
  version: 2.2.0
  wire:
    fields:
      0: {type: String, length: 4, enc: ASCII, prefix: ASCII.Fixed}
  fields:
    0: {name: MTI}
`

// A scenario names a spec; the rack resolves it against its own store. Nothing
// ever put anything in that store, so the reference could only resolve by luck.
// These assert that what the scenario names is what travels.
func TestScenarioCarriesTheSpecsItNames(t *testing.T) {
	mgr, urn := storeWithSpec(t)
	c := &ScenarioController{log: slog.Default(), specs: mgr}

	sc := &registry.Scenario{Gears: []registry.GearSpec{
		{Name: "codec-in", Config: map[string]any{"spec_path": urn}},
		{Name: "codec-out", Config: map[string]any{"spec": urn}},         // legacy key
		{Name: "on-disk", Config: map[string]any{"spec_path": "s.yaml"}}, // a path travels alone
		{Name: "no-spec", Config: map[string]any{"port": 5000}},
	}}

	got := c.collectSpecs(context.Background(), sc)
	if len(got) != 1 {
		t.Fatalf("carried %d artefacts, want 1 (deduplicated, paths excluded): %+v", len(got), got)
	}
	if got[0].URN() != urn {
		t.Errorf("carried %q, want %q", got[0].URN(), urn)
	}
	if string(got[0].Content) != specBody {
		t.Error("the bytes that travelled are not the bytes that were stored")
	}
}

// The artefact has to survive CBOR, because that is how it reaches the rack.
func TestSpecsSurviveThePayloadRoundTrip(t *testing.T) {
	in := &fluxmsg.ScenarioPayload{
		Name:     "payment-switch",
		Version:  "1.0.0",
		Scenario: []byte("meta:\n  name: payment-switch\n"),
		Specs:    []fluxmsg.SpecArtifact{{Name: "acme-auth", Tag: "v2.2.0", Content: []byte(specBody)}},
	}
	data, err := in.ToData()
	if err != nil {
		t.Fatal(err)
	}
	out, err := fluxmsg.ParseScenarioPayload(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Specs) != 1 {
		t.Fatalf("%d specs survived, want 1", len(out.Specs))
	}
	if out.Specs[0].URN() != "acme-auth:v2.2.0" || string(out.Specs[0].Content) != specBody {
		t.Errorf("artefact changed in transit: %+v", out.Specs[0])
	}
}

// The receiving half: a rack files what arrived, and the reference the scenario
// uses then resolves against its own store.
func TestARackCanStoreWhatArrivedAndResolveIt(t *testing.T) {
	rack, err := manager.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	artifact := fluxmsg.SpecArtifact{Name: "acme-auth", Tag: "v2.2.0", Content: []byte(specBody)}

	ctx := context.Background()
	if _, _, tag, errImp := rack.ImportContent(ctx, artifact.Content, artifact.Name, artifact.Tag); errImp != nil {
		t.Fatalf("store what arrived: %v", errImp)
	} else if tag != "v2.2.0" {
		t.Errorf("filed under %q, not the reference the scenario uses", tag)
	}

	back, err := rack.Load(ctx, artifact.URN())
	if err != nil {
		t.Fatalf("resolve %s after storing it: %v", artifact.URN(), err)
	}
	if string(back) != specBody {
		t.Error("what came back is not what was filed")
	}
}

func storeWithSpec(t *testing.T) (manager.Manager, string) {
	t.Helper()
	dir := t.TempDir()
	mgr, err := manager.NewManager(filepath.Join(dir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "acme.yaml")
	if errW := os.WriteFile(src, []byte(specBody), 0o600); errW != nil {
		t.Fatal(errW)
	}
	if _, _, _, errI := mgr.Import(context.Background(), src, "", ""); errI != nil {
		t.Fatal(errI)
	}
	return mgr, "acme-auth:v2.2.0"
}
