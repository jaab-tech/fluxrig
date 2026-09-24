// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package logger

// The card number lengths the masking looks for, and how much of a number stays
// visible. A number is recognised by its length, by the digit it starts with and by
// the Luhn check, so a run of digits that only looks like one is masked too: over
// masking a trace is harmless, missing a number in it is not.
const (
	minPANLen      = 13
	maxPANLen      = 19
	maskKeepFirst  = 6
	maskKeepLast   = 4
	firstPANDigit  = '2' // networks start at 2 (Mastercard 2-series) ...
	lastFirstDigit = '6' // ... and end at 6 (Discover, UnionPay)
)

// MaskPAN returns a copy of b in which every card number written as ASCII digits
// keeps its first six and last four digits and has the rest replaced by '*'.
//
// It finds a number inside longer text or inside a longer run of digits, which is
// how an ISO 8583 message in ASCII carries one (a length prefix, then the number,
// then the next field). A number in a binary encoding, such as packed BCD, is not
// recognised: a payload is only ever logged at TRACE, and TRACE is for development.
func MaskPAN(b []byte) []byte {
	out := append([]byte(nil), b...)
	for i := 0; i < len(b); i++ {
		if b[i] < firstPANDigit || b[i] > lastFirstDigit {
			continue
		}
		for l := maxPANLen; l >= minPANLen; l-- {
			if i+l > len(b) {
				continue
			}
			w := b[i : i+l]
			if !allDigits(w) || !luhnValid(w) {
				continue
			}
			for k := i + maskKeepFirst; k < i+l-maskKeepLast; k++ {
				out[k] = '*'
			}
		}
	}
	return out
}

// MaskPANString is MaskPAN for a string.
func MaskPANString(s string) string { return string(MaskPAN([]byte(s))) }

func allDigits(b []byte) bool {
	for _, c := range b {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// luhnValid reports whether the digits pass the Luhn check.
func luhnValid(digits []byte) bool {
	sum := 0
	double := false
	for i := len(digits) - 1; i >= 0; i-- {
		d := int(digits[i] - '0')
		if double {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
	}
	return sum%10 == 0
}
