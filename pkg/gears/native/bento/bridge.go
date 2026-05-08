// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package bento

import (
	"encoding/json"

	"github.com/google/uuid"
	"github.com/warpstreamlabs/bento/public/service"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

// ToBentoMessage converts a FluxMsg to a Bento Service Message.
func ToBentoMessage(fm *fluxmsg.FluxMsg) *service.Message {
	// Create new Bento message
	// Default to the Raw payload if Data is empty, otherwise use Data.
	var m *service.Message
	if len(fm.Data) > 0 {
		m = service.NewMessage(nil)
		m.SetStructured(fm.Data)
	} else {
		m = service.NewMessage(fm.RawPayload)
	}

	// Map Metadata
	for k, v := range fm.Metadata {
		m.MetaSet(k, v)
	}

	// Bridge Critical Identity Fields to Metadata
	m.MetaSet("flux_id", fm.FluxID.String())
	m.MetaSet("trace_id", fm.TraceID)
	if fm.RefFluxID != uuid.Nil {
		m.MetaSet("ref_flux_id", fm.RefFluxID.String())
	}

	// NEW: Preserve Path for Telemetry Fidelity
	if len(fm.Path) > 0 {
		if pathBytes, err := json.Marshal(fm.Path); err == nil {
			m.MetaSet("flux_path", string(pathBytes))
		}
	}

	return m
}

// FromBentoMessage converts a Bento Service Message back to a FluxMsg.
// It attempts to preserve original identity if present in metadata.
func FromBentoMessage(bm *service.Message) (*fluxmsg.FluxMsg, error) {
	fm := fluxmsg.New()

	// 1. Recover Metadata & Identity
	_ = bm.MetaWalk(func(k, v string) error {
		switch k {
		case "flux_id":
			if id, err := uuid.Parse(v); err == nil {
				fm.FluxID = id
			}
		case "trace_id":
			fm.TraceID = v
		case "ref_flux_id":
			if id, err := uuid.Parse(v); err == nil {
				fm.RefFluxID = id
			}
		case "flux_path":
			var path []*fluxmsg.Hop
			if err := json.Unmarshal([]byte(v), &path); err == nil {
				fm.Path = path
			}
		default:
			fm.Metadata[k] = v
		}
		return nil
	})

	// 2. Recover Data (Payload)
	// Try structured first
	dataPart, err := bm.AsStructured()
	if err == nil {
		// If it's a map, assign to Data
		if asMap, ok := dataPart.(map[string]any); ok {
			fm.Data = asMap
		} else {
			// If it's a list or scalar, we put it in a wrapped key or rely on Raw
			bytes, _ := bm.AsBytes()
			fm.RawPayload = bytes
		}
	} else {
		// Fallback to Raw Bytes
		bytes, _ := bm.AsBytes()
		fm.RawPayload = bytes
	}

	// Explicitly set RawPayload if Data is present too, for consistency?
	if len(fm.Data) == 0 && len(fm.RawPayload) == 0 {
		bytes, _ := bm.AsBytes()
		fm.RawPayload = bytes
	}

	return fm, nil
}
