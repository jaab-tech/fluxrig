// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package logger

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// withCheckDigit returns prefix followed by the digit that makes it pass Luhn.
func withCheckDigit(prefix string) string {
	for d := 0; d <= 9; d++ {
		if luhnValid([]byte(prefix + strconv.Itoa(d))) {
			return prefix + strconv.Itoa(d)
		}
	}
	return prefix
}

func TestMaskPAN_KeepsSixAndFour(t *testing.T) {
	assert.Equal(t, "411111******1111", MaskPANString("4111111111111111"))
	assert.Equal(t, "378282*****0005", MaskPANString("378282246310005"), "15 digits, Amex")
	nineteen := withCheckDigit("630400000000000000")
	got := MaskPANString(nineteen)
	assert.Len(t, got, 19)
	assert.Equal(t, nineteen[:6]+strings.Repeat("*", 9)+nineteen[15:], got, "19 digits")
}

// An ISO 8583 message in ASCII puts the number in the middle of a run of digits:
// the tail of the bitmap, the length prefix, the number, then the amount.
func TestMaskPAN_FindsANumberInsideALongerRun(t *testing.T) {
	msg := "01007234054128C28805164111111111111111000000010000000001"
	got := MaskPANString(msg)

	assert.NotContains(t, got, "4111111111111111")
	assert.NotContains(t, got, "1111111111", "no long run of the number's digits survives")
	assert.Len(t, got, len(msg), "masking never changes the length")
	assert.True(t, strings.HasPrefix(got, "0100"), "text around the number is left alone")
	// Another run of digits that happens to pass the check can be masked as well, and
	// then more than the middle of the real number is hidden. That is the safe side.
}

func TestMaskPAN_LeavesOtherDigitsAlone(t *testing.T) {
	for _, s := range []string{
		"4111111111111112",        // 16 digits that fail Luhn
		"12345678",                // too short
		"amount=000000010000",     // 12 digits
		"",                        // nothing
		"no digits here at all\n", // text
	} {
		assert.Equal(t, s, MaskPANString(s), "input %q", s)
	}
}

func TestMaskPAN_DoesNotChangeItsInput(t *testing.T) {
	in := []byte("pan=4111111111111111;")
	before := string(in)
	out := MaskPAN(in)

	assert.Equal(t, before, string(in))
	assert.NotEqual(t, before, string(out))
}

func TestMaskPAN_BinaryBytesPassThrough(t *testing.T) {
	in := []byte{0x00, 0xFF, 0x41, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11}
	assert.Equal(t, in, MaskPAN(in), "a packed number is not recognised, and nothing else changes")
}
