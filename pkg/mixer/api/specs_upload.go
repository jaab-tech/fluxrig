// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/jaab-tech/fluxrig/pkg/gears/native/iso8583/codec/sdl"
	"github.com/jaab-tech/fluxrig/pkg/iso20022/rtp"
	iso20022spec "github.com/jaab-tech/fluxrig/pkg/iso20022/spec"
	"github.com/jaab-tech/fluxrig/pkg/manager"
	"github.com/jaab-tech/fluxrig/pkg/translation"
)

// The kinds of artefact the spec store holds.
const (
	ArtefactISO8583     = "iso8583"
	ArtefactISO20022    = "iso20022"
	ArtefactTranslation = "translation"
	ArtefactData        = "data"
)

// SpecUploaded is the answer to an upload: where the store filed the artefact.
type SpecUploaded struct {
	Name string `json:"name" example:"billpay-rtp"`
	Tag  string `json:"tag" example:"v1.0.0"`
	Hash string `json:"hash" example:"5dd8f432c33bc7a61f4cd49c8933e34a94f5586f60b878e99cdb51314ef6eacb"`
	URN  string `json:"urn" example:"billpay-rtp:v1.0.0"`
	// Kind is what the Mixer validated the artefact as: iso8583, iso20022,
	// translation, or data (lookup records, which have no spec header).
	Kind string `json:"kind" example:"translation"`
}

// handleSpecUpload stores an artefact in the Mixer's spec store, from where it
// travels to the Racks with every scenario that names it.
//
//	@Summary		Upload a spec or lookup data
//	@Description	Validates the artefact for its kind and files it under name:tag. A spec declares both; lookup data has no header and needs the name and tag query parameters. A translation spec must name store references, and the protocol specs and lookup records it names must already be in the store. A name:tag that already names other content is refused: change the version.
//	@Tags			specs
//	@Accept			application/yaml
//	@Produce		json
//	@Param			name	query		string	false	"Name to file under. Required for lookup data; overrides a spec's declared id."
//	@Param			tag		query		string	false	"Version tag (e.g. v1). Required for lookup data; overrides a spec's declared version."
//	@Success		201		{object}	SpecUploaded
//	@Failure		400		{string}	string	"the artefact does not validate"
//	@Failure		409		{string}	string	"name:tag already names other content"
//	@Failure		413		{string}	string	"larger than mixer.max_payload_size"
//	@Router			/specs [post]
func (s *Server) handleSpecUpload(w http.ResponseWriter, r *http.Request) {
	if s.specs == nil {
		http.Error(w, "no spec store is configured", http.StatusServiceUnavailable)
		return
	}
	// An artefact reaches the Racks inside the scenario payload, so it can be
	// no larger than a payload.
	limit := int64(s.cfg.Mixer.MaxPayloadSize)
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	content, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, fmt.Sprintf("the artefact is larger than mixer.max_payload_size (%d bytes)", limit), http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "read error: "+err.Error(), http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	tag := strings.TrimSpace(r.URL.Query().Get("tag"))

	kind, err := s.validateArtefact(r, content, name, tag)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	hash, finalName, finalTag, err := s.specs.ImportContent(r.Context(), content, name, tag)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, manager.ErrVersionExists) {
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(SpecUploaded{
		Name: finalName, Tag: finalTag, Hash: hash, URN: finalName + ":" + finalTag, Kind: kind,
	})
}

// validateArtefact tells the kind of an artefact from its header and checks it
// with the loader of that kind, so that a Rack never receives one it cannot
// load.
func (s *Server) validateArtefact(r *http.Request, content []byte, name, tag string) (string, error) {
	var head struct {
		Spec *struct {
			Kind string `yaml:"kind"`
			Wire struct {
				Source string `yaml:"source"`
			} `yaml:"wire"`
		} `yaml:"spec"`
	}
	if err := yaml.NewDecoder(bytes.NewReader(content)).Decode(&head); err != nil {
		return "", fmt.Errorf("not a YAML document: %w", err)
	}
	switch {
	case head.Spec == nil:
		// Lookup data: records keyed by their lookup key.
		if name == "" || tag == "" {
			return "", fmt.Errorf("the artefact has no spec header, so it is data: give its name and tag as query parameters")
		}
		var records map[string]map[string]string
		if err := yaml.Unmarshal(content, &records); err != nil {
			return "", fmt.Errorf("lookup data is a map of records, each a map of fields: %w", err)
		}
		return ArtefactData, nil
	case head.Spec.Kind == ArtefactTranslation:
		resolve := func(ref string) ([]byte, string, error) {
			if strings.ContainsAny(ref, `/\`) || strings.HasSuffix(ref, ".yaml") || strings.HasSuffix(ref, ".yml") {
				return nil, "", fmt.Errorf("a translation spec in the store names store references (name:tag), not files")
			}
			b, err := s.specs.Load(r.Context(), ref)
			if err != nil {
				return nil, "", fmt.Errorf("not in the store; upload it first: %w", err)
			}
			return b, "", nil
		}
		t, err := translation.Load(content, resolve)
		if err != nil {
			return "", err
		}
		if err := t.LoadLookups(resolve); err != nil {
			return "", err
		}
		return ArtefactTranslation, nil
	case head.Spec.Wire.Source == rtp.WireSource:
		if _, err := iso20022spec.Load(content); err != nil {
			return "", err
		}
		return ArtefactISO20022, nil
	default:
		if _, _, err := sdl.LoadSpecContent(content, ""); err != nil {
			return "", err
		}
		return ArtefactISO8583, nil
	}
}
