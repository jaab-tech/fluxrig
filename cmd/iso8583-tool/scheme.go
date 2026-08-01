// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/binary"
	"flag"
	"io"
	"log"
	"net"

	"github.com/moov-io/iso8583"

	"github.com/jaab-tech/fluxrig/pkg/gears/native/iso8583/codec/sdl"
)

// runSchemeMode is a scheme-host simulator for payment-switch validation. It
// listens for authorization requests, and for each one replies with an
// authorization response: the MTI advanced to a response (0200 -> 0210), the
// correlation fields (STAN, transmission date/time) echoed, and DE39 set to a
// configured value. With -sink it accepts requests and never answers, to drive
// the switch's timeout path. The wire format comes from the same SDL spec the
// codec gears load, so alignment is guaranteed.
var (
	schemePort = flag.String("scheme-port", "10001", "Port to listen on")
	schemeSpec = flag.String("scheme-spec", "", "Path to the SDL spec (required)")
	schemeDE39 = flag.String("scheme-de39", "00", "Response code to stamp in DE39")
	schemeSink = flag.Bool("scheme-sink", false, "Accept requests but never reply (drives timeout)")
)

func runSchemeMode() {
	port, specF, de39, sinkFlg := schemePort, schemeSpec, schemeDE39, schemeSink

	if *specF == "" {
		log.Fatal("scheme: -scheme-spec is required")
	}
	spec, _, err := sdl.LoadSpec(*specF)
	if err != nil {
		log.Fatalf("scheme: load spec: %v", err)
	}

	ln, err := net.Listen("tcp", ":"+*port)
	if err != nil {
		log.Fatalf("scheme: listen: %v", err)
	}
	log.Printf("scheme host listening on :%s (de39=%s sink=%v)", *port, *de39, *sinkFlg)

	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go handleScheme(conn, spec, *de39, *sinkFlg)
	}
}

func handleScheme(conn net.Conn, spec *iso8583.MessageSpec, de39 string, sink bool) {
	defer func() { _ = conn.Close() }()
	for {
		payload, err := readFramed(conn)
		if err != nil {
			return
		}
		if sink {
			continue // accept and drop: the switch will time out
		}

		req := iso8583.NewMessage(spec)
		if uerr := req.Unpack(payload); uerr != nil {
			log.Printf("scheme: unpack: %v", uerr)
			continue
		}
		mti := fieldStr(req, 0)

		resp := iso8583.NewMessage(spec)
		_ = resp.Field(0, responseMTI(mti))
		// Echo the fields the switch correlates on, plus a couple of identity
		// fields, then stamp the response code.
		for _, f := range []int{2, 3, 4, 7, 11, 41, 49} {
			if v := req.GetField(f); v != nil {
				if s, e := v.String(); e == nil && s != "" {
					_ = resp.Field(f, s)
				}
			}
		}
		_ = resp.Field(39, de39)

		out, err := resp.Pack()
		if err != nil {
			log.Printf("scheme: pack: %v", err)
			continue
		}
		if _, err := conn.Write(frameFor(out)); err != nil {
			return
		}
	}
}

// responseMTI advances the message-function digit (index 2): 0200 -> 0210.
func responseMTI(mti string) string {
	if len(mti) != 4 {
		return mti
	}
	b := []byte(mti)
	if b[2] >= '0' && b[2] <= '8' {
		b[2]++
	}
	return string(b)
}

// readFramed reads one 2-byte big-endian length-prefixed frame.
func readFramed(conn net.Conn) ([]byte, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint16(hdr[:]))
	buf := make([]byte, n)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// frameFor prepends a 2-byte big-endian length prefix.
func frameFor(payload []byte) []byte {
	out := make([]byte, 2+len(payload))
	binary.BigEndian.PutUint16(out[:2], uint16(len(payload)))
	copy(out[2:], payload)
	return out
}
