// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"time"

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
	// A host that only ever echoes correlation fields cannot show that it read
	// anything. An issuer's authorizer receives a private field and acts on it,
	// and a test has to see which value arrived, so this reports one field back
	// in another: "48:42" answers with what DE 48 carried, in DE 42, and does
	// not echo DE 48 itself. Reporting it in a different field is the point:
	// echoing a private field would send it back to a counterparty whose
	// dialect need not declare it.
	// Answering late is the only way to reach the correlation store's expiry
	// path: an entry that outlives its TTL is gone when the reply arrives, and
	// what happens then is a property worth testing rather than assuming.
	schemeDelay = flag.Duration("scheme-delay", 0, "Wait this long before replying (drives correlation TTL expiry)")

	schemeMove = flag.String("scheme-move", "", "Report a request field back in another, as src:dst (e.g. 48:42)")
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
	moveSrc, moveDst, mErr := parseMove(*schemeMove)
	if mErr != nil {
		log.Fatalf("scheme: -scheme-move: %v", mErr)
	}

	log.Printf("scheme host listening on :%s (de39=%s sink=%v move=%q)", *port, *de39, *sinkFlg, *schemeMove)

	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go handleScheme(conn, spec, *de39, *sinkFlg, moveSrc, moveDst)
	}
}

// fitField pads a value to the destination field's declared width.
//
// A field carrying an outcome is rarely as wide as the field reporting it, and
// a fixed-length field refuses anything shorter. Alphanumeric fields declare no
// padder of their own, so without this the reply simply fails to pack and the
// host answers nothing at all.
func fitField(spec *iso8583.MessageSpec, num int, value string) string {
	f, ok := spec.Fields[num]
	if !ok || f.Spec() == nil {
		return value
	}
	// A variable-length field encodes its own size, so a short value is legal
	// there and padding it would change the value. Only fixed fields need this.
	width := f.Spec().Length
	if f.Spec().Pref != nil {
		if _, err := f.Spec().Pref.EncodeLength(width, len(value)); err == nil {
			if enc, _ := f.Spec().Pref.EncodeLength(width, width); len(enc) > 0 {
				return value
			}
		}
	}
	if width <= 0 || len(value) >= width {
		return value
	}
	return value + strings.Repeat(" ", width-len(value))
}

// parseMove reads "src:dst". Empty means no reporting, which is the default.
func parseMove(spec string) (int, int, error) {
	if spec == "" {
		return 0, 0, nil
	}
	srcStr, dstStr, found := strings.Cut(spec, ":")
	if !found {
		return 0, 0, fmt.Errorf("want src:dst, got %q", spec)
	}
	src, err := strconv.Atoi(srcStr)
	if err != nil {
		return 0, 0, fmt.Errorf("source field %q: %w", srcStr, err)
	}
	dst, err := strconv.Atoi(dstStr)
	if err != nil {
		return 0, 0, fmt.Errorf("destination field %q: %w", dstStr, err)
	}
	return src, dst, nil
}

func handleScheme(conn net.Conn, spec *iso8583.MessageSpec, de39 string, sink bool, moveSrc, moveDst int) {
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
		if moveSrc > 0 && moveDst > 0 {
			if v := req.GetField(moveSrc); v != nil {
				if str, e := v.String(); e == nil && str != "" {
					_ = resp.Field(moveDst, fitField(spec, moveDst, str))
				}
			}
		}
		_ = resp.Field(39, de39)

		if *schemeDelay > 0 {
			time.Sleep(*schemeDelay)
		}

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
