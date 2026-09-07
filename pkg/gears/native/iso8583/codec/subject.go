// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package codec

import (
	"fmt"
	"strings"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

// subject reads a decoded fluxMsg the way a `when` expression addresses it.
//
// The codec already writes every element under `iso8583.field.<n>` and every
// subfield under `iso8583.field.<n>.<tag>`, which is the same shape a path in
// the language has. Nothing is re-parsed to answer a rule.
type subject struct {
	mti string
	msg *fluxmsg.FluxMsg
}

func (s subject) MTI() string { return s.mti }

func (s subject) Field(de int, subs []string) (string, bool) {
	key := fmt.Sprintf("iso8583.field.%d", de)
	if len(subs) > 0 {
		key += "." + strings.Join(subs, ".")
	}
	v, ok := s.msg.Get(key)
	if !ok {
		return "", false
	}
	// A binary element -- a PIN block, a MAC, an EMV tag -- is held as bytes so
	// it survives the bus. A rule comparing one compares the characters it
	// carries, which is what a spec writing `field(52) == '...'` means.
	if b, isBytes := toBytes(v); isBytes {
		return string(b), true
	}
	if str, isStr := v.(string); isStr {
		return str, true
	}
	// Present, and not something a comparison can read. Absent would be the
	// wrong answer: `present(n)` must still say it is there.
	return fmt.Sprint(v), true
}
