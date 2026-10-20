// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package translate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jaab-tech/fluxrig/pkg/manager"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
)

// storeContext is a gear context whose Manager is a real store.
type storeContext struct {
	sdk.GearContext
	mgr manager.Manager
}

func (c storeContext) Manager() manager.Manager { return c.mgr }

// The translation spec, the protocol specs and the lookup records all resolve
// from the store: a Rack needs no file of them.
func TestEverythingResolvesFromTheStore(t *testing.T) {
	ctx := context.Background()
	mgr, err := manager.NewManager(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	put := func(path, name, tag string, content []byte) {
		t.Helper()
		if content == nil {
			b, errRead := os.ReadFile(path)
			if errRead != nil {
				t.Fatal(errRead)
			}
			content = b
		}
		if _, _, _, errImport := mgr.ImportContent(ctx, content, name, tag); errImport != nil {
			t.Fatalf("import %s: %v", path, errImport)
		}
	}
	put("../../../../examples/specs/iso8583-v87-ascii.yaml", "", "", nil)
	put(rtpSpec, "", "", nil)
	put("testdata/lookups/billers.yaml", "billers", "v1", nil)
	raw, err := os.ReadFile(billpaySpec)
	if err != nil {
		t.Fatal(err)
	}
	spec := strings.NewReplacer(
		`"../../../../examples/specs/iso8583-v87-ascii.yaml"`, `"iso8583-v87-ascii:v3.0.0"`,
		`"../../../../examples/specs/iso20022-rtp.yaml"`, `"iso20022-rtp:v1.0.0"`,
		`"testdata/lookups/billers.yaml"`, `"billers:v1"`,
	).Replace(string(raw))
	if !strings.Contains(spec, `"billers:v1"`) || strings.Contains(spec, "examples/") {
		t.Fatalf("the fixture still names files:\n%s", spec)
	}
	put("billpay", "", "", []byte(spec))

	g := &Gear{}
	gctx := storeContext{GearContext: sdk.NewMockGearContext(map[string]any{"spec_path": "billpay-rtp:v1.0.0", "validation": "enforce", "on_error": "reject"}), mgr: mgr}
	if err := g.Init(gctx); err != nil {
		t.Fatalf("init from the store: %v", err)
	}
	out := process(t, g, request0200("BILLER000000001", "000000004250"))
	if get(out, "creditor_name") != "Example Water Utility" {
		t.Fatalf("creditor_name %q: the lookup did not answer from the store", get(out, "creditor_name"))
	}
	if _, ok := g.spec.LookupSources()["billers:v1"]; !ok {
		t.Fatalf("lookup sources %v", g.spec.LookupSources())
	}
}
