// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jaab-tech/fluxrig/pkg/manager"
)

// storeWith imports the reference spec into a fresh store and returns a server
// wired to it.
func storeWith(t *testing.T, body []byte, name string) *Server {
	t.Helper()
	dir := t.TempDir()
	mgr, err := manager.NewManager(filepath.Join(dir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, name)
	if errW := os.WriteFile(src, body, 0o600); errW != nil {
		t.Fatal(errW)
	}
	if _, _, _, errI := mgr.Import(context.Background(), src, "", ""); errI != nil {
		t.Fatalf("import: %v", errI)
	}
	return (&Server{}).WithSpecStore(mgr)
}

func referenceSpec(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../../examples/specs/iso8583-v87-ascii.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func get(t *testing.T, s *Server, h http.HandlerFunc, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	// The routes take name and tag from the path; the recorder needs them set
	// the way the mux would.
	parts := strings.Split(strings.TrimPrefix(req.URL.Path, "/api/v1/specs/"), "/")
	if len(parts) >= 2 {
		req.SetPathValue("name", parts[0])
		req.SetPathValue("tag", parts[1])
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

// The store already holds the spec, and the reference is derived from it. A
// document kept beside its source is a document that disagrees with it.
func TestTheAPIRendersAStoredSpec(t *testing.T) {
	s := storeWith(t, referenceSpec(t), "iso.yaml")

	rec := get(t, s, s.handleSpecDoc, "/api/v1/specs/iso8583-v87-ascii/v2.2.0/doc")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content type is %q", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{"<!doctype html>", "ISO 8583:1987 (ASCII)", "Data elements"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page is missing %q", want)
		}
	}
	// Served over HTTP or opened from disk, it must still fetch nothing.
	if strings.Contains(body, `src="http`) || strings.Contains(body, `href="http`) {
		t.Error("the served page reaches the network")
	}
}

// This endpoint carries no authentication of its own, and `scope: private` marks
// what a spec's author decided not to publish. Complete has to be asked for.
func TestTheAPIServesThePublicVariantByDefault(t *testing.T) {
	s := storeWith(t, referenceSpec(t), "iso.yaml")

	pub := get(t, s, s.handleSpecDoc, "/api/v1/specs/iso8583-v87-ascii/v2.2.0/doc").Body.String()
	full := get(t, s, s.handleSpecDoc, "/api/v1/specs/iso8583-v87-ascii/v2.2.0/doc?scope=complete").Body.String()

	if !strings.Contains(pub, "private elements are omitted") {
		t.Error("the default response does not withhold, or does not say it did")
	}
	if len(full) <= len(pub) {
		t.Error("the complete variant is not larger than the public one")
	}
	if strings.Contains(pub, `id="de-104"`) {
		t.Error("a private element reached the default response")
	}
	if !strings.Contains(full, `id="de-104"`) {
		t.Error("the complete variant is missing the private element")
	}
}

func TestTheAPIRendersMarkdownWhenAsked(t *testing.T) {
	s := storeWith(t, referenceSpec(t), "iso.yaml")
	rec := get(t, s, s.handleSpecDoc, "/api/v1/specs/iso8583-v87-ascii/v2.2.0/doc?format=markdown")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Errorf("content type is %q", ct)
	}
	if !strings.HasPrefix(rec.Body.String(), "# ") {
		t.Error("the body does not look like markdown")
	}
}

// `latest` is how a scenario refers to a spec it wants to track, so the
// reference has to resolve the same way.
func TestTheAPIResolvesLatest(t *testing.T) {
	s := storeWith(t, referenceSpec(t), "iso.yaml")
	if rec := get(t, s, s.handleSpecDoc, "/api/v1/specs/iso8583-v87-ascii/latest/doc"); rec.Code != http.StatusOK {
		t.Fatalf("latest does not resolve: %d %s", rec.Code, rec.Body.String())
	}
}

func TestTheAPIRefusesWhatItCannotServe(t *testing.T) {
	s := storeWith(t, referenceSpec(t), "iso.yaml")

	for name, tc := range map[string]struct {
		target string
		want   int
	}{
		"a spec that is not there": {"/api/v1/specs/nope/v1.0.0/doc", http.StatusNotFound},
		"a tag that is not there":  {"/api/v1/specs/iso8583-v87-ascii/v9.9.9/doc", http.StatusNotFound},
		"an unknown scope":         {"/api/v1/specs/iso8583-v87-ascii/v2.2.0/doc?scope=internal", http.StatusBadRequest},
		"an unknown format":        {"/api/v1/specs/iso8583-v87-ascii/v2.2.0/doc?format=pdf", http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			if rec := get(t, s, s.handleSpecDoc, tc.target); rec.Code != tc.want {
				t.Errorf("got %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

// A spec that no longer resolves would render a reference to a protocol nobody
// can serve. Saying so is more use than the document.
func TestTheAPIRefusesToDocumentASpecThatDoesNotLoad(t *testing.T) {
	broken := strings.Replace(string(referenceSpec(t)), `values_ref: "response_code"`, `values_ref: "gone"`, 1)
	s := storeWith(t, []byte(broken), "broken.yaml")

	rec := get(t, s, s.handleSpecDoc, "/api/v1/specs/iso8583-v87-ascii/v2.2.0/doc")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "gone") {
		t.Errorf("the error should name what is missing: %s", rec.Body.String())
	}
}

func TestTheAPIListsWhatTheStoreHolds(t *testing.T) {
	s := storeWith(t, referenceSpec(t), "iso.yaml")
	rec := get(t, s, s.handleSpecs, "/api/v1/specs")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	var items []SpecSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range items {
		if it.URN == "iso8583-v87-ascii:v2.2.0" {
			found = true
		}
	}
	if !found {
		t.Errorf("the imported spec is not listed: %+v", items)
	}
}

// Without a store the routes answer why rather than vanishing: an endpoint that
// is documented and missing is harder to diagnose than one that explains itself.
func TestTheAPISaysWhenItHasNoStore(t *testing.T) {
	s := &Server{}
	if rec := get(t, s, s.handleSpecDoc, "/api/v1/specs/a/v1.0.0/doc"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("got %d, want 503", rec.Code)
	}
}

// storeWithVersions files several versions of one spec, plus a scenario, so a
// listing has something to get wrong.
func storeWithVersions(t *testing.T, versions ...string) *Server {
	t.Helper()
	dir := t.TempDir()
	mgr, err := manager.NewManager(filepath.Join(dir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, v := range versions {
		body := []byte("spec:\n  id: acme\n  name: The Acme Protocol\n  version: \"" + v + "\"\n  protocol: iso8583\n")
		if _, _, _, errI := mgr.ImportContent(ctx, body, "", ""); errI != nil {
			t.Fatalf("import %s: %v", v, errI)
		}
	}
	src := filepath.Join(dir, "nightly.yaml")
	if errW := os.WriteFile(src, []byte("meta:\n  name: nightly\n  version: \"1.0.0\"\n"), 0o600); errW != nil {
		t.Fatal(errW)
	}
	if _, _, _, errI := mgr.ImportScenario(ctx, src, "", ""); errI != nil {
		t.Fatal(errI)
	}
	return (&Server{}).WithSpecStore(mgr)
}

func decodeSummaries(t *testing.T, body string) []SpecSummary {
	t.Helper()
	var out []SpecSummary
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode: %v\n%s", err, body)
	}
	return out
}

// The store holds specs and scenarios in one index. This endpoint serves specs,
// and served both until something said which was which.
func TestTheSpecListingHoldsOnlySpecs(t *testing.T) {
	srv := storeWithVersions(t, "1.0.0")
	rec := httptest.NewRecorder()
	srv.handleSpecs(rec, httptest.NewRequest(http.MethodGet, "/api/v1/specs", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "nightly") {
		t.Error("a scenario is listed as a spec")
	}
	got := decodeSummaries(t, rec.Body.String())
	if len(got) != 1 {
		t.Fatalf("listed %d specs, expected 1", len(got))
	}
}

// A listing is worth having only if it says more than the reference already
// does.
func TestTheSpecListingCarriesTheAttributesTheStoreKept(t *testing.T) {
	srv := storeWithVersions(t, "1.0.0")
	rec := httptest.NewRecorder()
	srv.handleSpecs(rec, httptest.NewRequest(http.MethodGet, "/api/v1/specs", nil))

	got := decodeSummaries(t, rec.Body.String())[0]
	if got.URN != "acme:v1.0.0" {
		t.Errorf("urn %q", got.URN)
	}
	if got.Title != "The Acme Protocol" {
		t.Errorf("title %q: the human name is not the reference and both are wanted", got.Title)
	}
	if got.Protocol != "iso8583" {
		t.Errorf("protocol %q", got.Protocol)
	}
	if got.Size == 0 {
		t.Error("no size")
	}
	if got.ImportedAt == nil || got.ImportedAt.IsZero() {
		t.Error("no import date")
	}
	if !got.Latest {
		t.Error("the only version is the one `latest` resolves to")
	}
	// A listing that made the reader assemble the doc URL would be a listing
	// they have to read the routing table to use.
	if got.Doc != "/api/v1/specs/acme/v1.0.0/doc" {
		t.Errorf("doc %q", got.Doc)
	}
}

// Newest means the highest version, not the last imported.
func TestTheHistoryIsOrderedByVersion(t *testing.T) {
	srv := storeWithVersions(t, "1.0.0", "2.0.0", "1.1.0", "1.0.1")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/specs/acme", nil)
	req.SetPathValue("name", "acme")
	rec := httptest.NewRecorder()
	srv.handleSpecHistory(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	got := decodeSummaries(t, rec.Body.String())
	var tags []string
	for _, s := range got {
		tags = append(tags, s.Tag)
	}
	want := []string{"v2.0.0", "v1.1.0", "v1.0.1", "v1.0.0"}
	if strings.Join(tags, ",") != strings.Join(want, ",") {
		t.Errorf("history order %v, want %v", tags, want)
	}
	if !got[0].Latest || got[2].Latest {
		t.Error("`latest` does not follow the highest version")
	}
}

func TestTheHistoryOfAnUnknownSpecIsNotFound(t *testing.T) {
	srv := storeWithVersions(t, "1.0.0")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/specs/nope", nil)
	req.SetPathValue("name", "nope")
	rec := httptest.NewRecorder()
	srv.handleSpecHistory(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "nope") {
		t.Error("the refusal does not name what was asked for")
	}
}
