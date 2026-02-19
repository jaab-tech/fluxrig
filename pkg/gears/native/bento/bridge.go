// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package bento

import (
	"fmt"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/warpstreamlabs/bento/public/service"
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
	// This ensures they are preserved even if the payload changes
	m.MetaSet("flux_id", fmt.Sprintf("%d", fm.FluxID))
	m.MetaSet("trace_id", fm.TraceID)
	if fm.RefFluxID != 0 {
		m.MetaSet("ref_flux_id", fmt.Sprintf("%d", fm.RefFluxID))
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
			// We treat flux_id as immutable read-only here typically,
			// but if we are bridging back, we might want to restore it.
			// For now, we leave new FluxID generation to the Rack unless explicitly needed?
			// Actually, for "Processor" mode, preserving ID is good.
			// fmt.Sscanf(v, "%d", &fm.FluxID) // Optional: Restore ID?
		case "trace_id":
			fm.TraceID = v
		case "ref_flux_id":
			// fmt.Sscanf(v, "%d", &fm.RefFluxID)
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
			// But FluxMsg.Data is map[string]any.
			// Fallback: If root is array, we might need a convention.
			// For now, let's assume object root for FluxMsg compatibility.
			// If not object, we marshal to bytes and set RawPayload instead.
			bytes, _ := bm.AsBytes()
			fm.RawPayload = bytes
		}
	} else {
		// Fallback to Raw Bytes
		bytes, _ := bm.AsBytes()
		fm.RawPayload = bytes
	}

	// Explicitly set RawPayload if Data is present too, for consistency?
	// Usually FluxRig sets one or the other as primary.
	// If we have Data, we don't strictly need Payload unless for audit.
	// Let's ensure RawPayload is populated if Data is empty.
	if len(fm.Data) == 0 && len(fm.RawPayload) == 0 {
		bytes, _ := bm.AsBytes()
		fm.RawPayload = bytes
	}

	return fm, nil
}
