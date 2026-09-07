// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/gears/native/iso8583/codec/sdl"
	"github.com/jaab-tech/fluxrig/pkg/manager"
	"github.com/jaab-tech/fluxrig/pkg/viz/protodoc"
)

// SpecSummary is one version of one spec, with what the store knows about it.
//
// The attributes are what was recorded, not what can be inferred. A spec filed
// before the store kept dates has none, and the field is omitted rather than
// filled with a blob file's timestamp -- copying a store rewrites every one of
// those, so it would be a different fact wearing the right name.
type SpecSummary struct {
	Name string `json:"name" example:"iso8583-v87-ascii"`
	Tag  string `json:"tag" example:"v2.2.0"`
	Hash string `json:"hash" example:"1c8831d23bdd8458feafe6a506c5d3730605b78fd22b5d5c7f1c147043ef71af"`
	URN  string `json:"urn" example:"iso8583-v87-ascii:v2.2.0"`

	// ImportedAt is absent for anything filed before the store kept dates.
	ImportedAt *time.Time `json:"imported_at,omitempty" example:"2026-09-06T18:47:26Z"`
	// Size of the stored document in bytes.
	Size int64 `json:"size,omitempty" example:"34441"`
	// Title is the human name the document declares, which is not the reference
	// it is filed under.
	Title string `json:"title,omitempty" example:"ISO 8583:1987 (ASCII)"`
	// Protocol is what the spec declares it speaks; empty when it declares none
	// and relies on the loader's default.
	Protocol string `json:"protocol,omitempty" example:"iso8583"`
	// Latest reports whether a reference without a tag resolves here.
	Latest bool `json:"latest" example:"true"`
	// Doc is where this version's protocol reference is served.
	Doc string `json:"doc" example:"/api/v1/specs/iso8583-v87-ascii/v2.2.0/doc"`
}

// summarise turns store listings into the API's shape.
func summarise(items []manager.ArtifactInfo) []SpecSummary {
	out := make([]SpecSummary, 0, len(items))
	for _, it := range items {
		// The store holds specs and scenarios in one index. This endpoint serves
		// specs, and listed both until something said which was which.
		if it.Kind != manager.KindSpec {
			continue
		}
		s := SpecSummary{
			Name: it.Name, Tag: it.Tag, Hash: it.Hash,
			URN:      it.Name + ":" + it.Tag,
			Size:     it.Size,
			Title:    it.Title,
			Protocol: it.Protocol,
			Latest:   it.Latest,
			Doc:      "/api/v1/specs/" + it.Name + "/" + it.Tag + "/doc",
		}
		if !it.ImportedAt.IsZero() {
			at := it.ImportedAt.UTC()
			s.ImportedAt = &at
		}
		out = append(out, s)
	}
	return out
}

// handleSpecs lists the specs the store holds.
//
//	@Summary		List stored specs
//	@Description	Every spec in the content-addressed store, by name, version tag and content hash.
//	@Tags			specs
//	@Produce		json
//	@Success		200	{array}		SpecSummary
//	@Failure		503	{string}	string
//	@Router			/specs [get]
func (s *Server) handleSpecs(w http.ResponseWriter, r *http.Request) {
	if s.specs == nil {
		http.Error(w, "no spec store is configured", http.StatusServiceUnavailable)
		return
	}
	items, err := s.specs.List(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(summarise(items))
}

// handleSpecHistory lists every stored version of one spec.
//
//	@Summary		Every version of one spec
//	@Description	Each version the store holds for this spec, newest first — newest meaning the highest version, not the last imported, since a patch to an older branch arrives after a newer release.
//	@Tags			specs
//	@Produce		json
//	@Param			name	path		string	true	"Spec name"
//	@Success		200		{array}		SpecSummary
//	@Failure		404		{string}	string
//	@Router			/specs/{name} [get]
func (s *Server) handleSpecHistory(w http.ResponseWriter, r *http.Request) {
	if s.specs == nil {
		http.Error(w, "no spec store is configured", http.StatusServiceUnavailable)
		return
	}
	name := r.PathValue("name")
	if name == "" {
		http.Error(w, "a spec is named", http.StatusBadRequest)
		return
	}
	items, err := s.specs.History(r.Context(), manager.KindSpec, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(summarise(items))
}

// handleSpecSource serves a stored spec as it was imported.
//
//	@Summary		Fetch a stored spec
//	@Description	The spec document as imported, byte for byte.
//	@Tags			specs
//	@Produce		plain
//	@Param			name	path		string	true	"Spec name"
//	@Param			tag		path		string	true	"Version tag, or 'latest'"
//	@Success		200		{string}	string	"the spec document"
//	@Failure		404		{string}	string
//	@Router			/specs/{name}/{tag} [get]
func (s *Server) handleSpecSource(w http.ResponseWriter, r *http.Request) {
	content, ok := s.resolveSpec(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

// handleSpecDoc renders the protocol reference for a stored spec.
//
// The reference is derived on every request rather than stored beside the spec.
// A document kept alongside its source is a document that disagrees with it, and
// nothing about a stale protocol reference looks wrong until someone acts on it.
//
//	@Summary		Render a spec's protocol reference
//	@Description	The messages, what each carries, and what every data element means — derived from the spec on each request. Defaults to the public variant, which omits fields the spec marks `scope: private`.
//	@Tags			specs
//	@Produce		html
//	@Param			name	path		string	true	"Spec name"
//	@Param			tag		path		string	true	"Version tag, or 'latest'"
//	@Param			scope	query		string	false	"public (default) or complete"
//	@Param			format	query		string	false	"html (default) or markdown"
//	@Success		200		{string}	string	"the rendered reference"
//	@Failure		400		{string}	string
//	@Failure		404		{string}	string
//	@Router			/specs/{name}/{tag}/doc [get]
func (s *Server) handleSpecDoc(w http.ResponseWriter, r *http.Request) {
	// Public by default, and complete only when asked for. This endpoint carries
	// no authentication of its own, and `scope: private` marks what a spec's
	// author decided not to publish.
	scope := protodoc.Scope(defaultQuery(r, "scope", string(protodoc.ScopePublic)))
	switch scope {
	case protodoc.ScopePublic, protodoc.ScopeComplete:
	default:
		http.Error(w, fmt.Sprintf("unknown scope %q; use public or complete", scope), http.StatusBadRequest)
		return
	}
	format := defaultQuery(r, "format", "html")
	switch format {
	case "html", "markdown":
	default:
		http.Error(w, fmt.Sprintf("unknown format %q; use html or markdown", format), http.StatusBadRequest)
		return
	}

	content, ok := s.resolveSpec(w, r)
	if !ok {
		return
	}

	// A stored spec that no longer resolves would render a reference to a
	// protocol nobody can serve. Saying so is more use than the document.
	if _, _, err := sdl.LoadSpecContent(content, ""); err != nil {
		http.Error(w, "the stored spec does not load, so there is nothing to document: "+err.Error(),
			http.StatusUnprocessableEntity)
		return
	}
	spec, err := sdl.ParseSemantic(content)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}

	doc := protodoc.Build(spec, protodoc.Options{Scope: scope, Source: content})
	body, contentType := protodoc.HTMLDoc(doc), "text/html; charset=utf-8"
	if format == "markdown" {
		body, contentType = protodoc.MarkdownDoc(doc), "text/markdown; charset=utf-8"
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

// resolveSpec fetches the spec a request names, or answers the request itself
// and reports that it did.
func (s *Server) resolveSpec(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if s.specs == nil {
		http.Error(w, "no spec store is configured", http.StatusServiceUnavailable)
		return nil, false
	}
	name, tag := r.PathValue("name"), r.PathValue("tag")
	if name == "" || tag == "" {
		http.Error(w, "a spec is named as name/tag", http.StatusBadRequest)
		return nil, false
	}
	urn := name + ":" + tag
	content, err := s.specs.Load(r.Context(), urn)
	if err != nil {
		http.Error(w, fmt.Sprintf("no spec %q in the store: %v", urn, err), http.StatusNotFound)
		return nil, false
	}
	return content, true
}

func defaultQuery(r *http.Request, key, fallback string) string {
	if v := strings.TrimSpace(r.URL.Query().Get(key)); v != "" {
		return v
	}
	return fallback
}
