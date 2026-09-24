// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/moov-io/iso8583"
	"github.com/moov-io/iso8583/encoding"
	"github.com/moov-io/iso8583/prefix"

	"github.com/jaab-tech/fluxrig/pkg/gears/native/iso8583/codec/sdl"
)

func runDecodeMode() {
	if *decodeMessage == "" {
		log.Fatalf("decode mode requires -message flag with hex-encoded ISO8583 message")
	}
	if *decodeSpec == "" {
		log.Fatalf("decode mode requires -spec flag with path to SDL spec file")
	}

	// Load spec
	content, err := os.ReadFile(*decodeSpec)
	if err != nil {
		log.Fatalf("Failed to read spec file: %v", err)
	}

	// Load moov spec
	moovSpec, _, err := sdl.LoadSpecContent(content, "")
	if err != nil {
		log.Fatalf("Failed to load moov spec: %v", err)
	}

	// Patch spec with ASCII encoding (like fluxrig decoder does).
	// Deliberately never assigns Pad: moov unpads on unpack whenever
	// Pad is set, which would strip leading zeros ("0110"->"110",
	// "00"->""). This tool only unpacks, so it needs no pack padding.
	for _, fld := range moovSpec.Fields {
		if fld == nil {
			continue
		}
		spec := fld.Spec()
		if spec == nil {
			continue
		}
		if spec.Enc == nil {
			spec.Enc = encoding.ASCII
		}
		if spec.Pref == nil {
			spec.Pref = prefix.ASCII.Fixed
		}
		for _, subField := range spec.Subfields {
			subSpec := subField.Spec()
			if subSpec != nil {
				if subSpec.Enc == nil {
					subSpec.Enc = encoding.ASCII
				}
				if subSpec.Pref == nil {
					subSpec.Pref = prefix.ASCII.Fixed
				}
			}
		}
	}

	// Decode hex message
	msgBytes, err := hex.DecodeString(*decodeMessage)
	if err != nil {
		log.Fatalf("Failed to decode hex message: %v", err)
	}

	// Remove length prefix if present (2 bytes)
	if len(msgBytes) >= 2 {
		length := int(msgBytes[0])<<8 | int(msgBytes[1])
		if length == len(msgBytes)-2 {
			msgBytes = msgBytes[2:]
		}
	}

	// Decode using moov-io/iso8583
	isoMsg := iso8583.NewMessage(moovSpec)
	if isoMsg == nil {
		log.Fatalf("Failed to create isoMsg from moovSpec")
	}

	// The codec gear recovers a panic inside Unpack (a malformed or adversarial
	// payload can trigger one in the moov-io library); this tool must too, or a
	// user pointing it at bad input gets a raw stack trace instead of the clean
	// error every other failure path here already reports.
	func() {
		defer func() {
			if r := recover(); r != nil {
				log.Fatalf("panic while unpacking message: %v", r)
			}
		}()
		if err := isoMsg.Unpack(msgBytes); err != nil {
			log.Fatalf("Failed to unpack message: %v", err)
		}
	}()

	// Extract fields
	result := make(map[string]string)

	// Get MTI
	mti, _ := isoMsg.GetString(0)
	if len(mti) > 0 {
		for len(mti) < 4 {
			mti = "0" + mti
		}
		result["iso8583.mti"] = mti
	}

	// Get bitmap
	bitmap := isoMsg.Bitmap()
	if bitmap != nil {
		for i := 0; i <= 128; i++ {
			if i > 0 && !bitmap.IsSet(i) {
				continue
			}
			val, err := isoMsg.GetString(i)
			if err != nil {
				continue
			}
			result[fmt.Sprintf("iso8583.field.%d", i)] = val
		}
	}

	// Output as JSON
	jsonOut, _ := json.Marshal(result)
	fmt.Println(string(jsonOut))
}
