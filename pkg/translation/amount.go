// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package translation

import (
	"fmt"
	"strings"
)

// currency is one ISO 4217 currency: its alphabetic and numeric codes and the
// number of digits after the decimal point.
type currency struct {
	alpha, numeric string
	exponent       int
}

// currencies is the ISO 4217 subset the amount conversion knows. A currency
// outside it is an error, never a guess at its exponent.
var currencies = []currency{
	{"ARS", "032", 2}, {"AUD", "036", 2}, {"BHD", "048", 3}, {"BRL", "986", 2},
	{"CAD", "124", 2}, {"CHF", "756", 2}, {"CLP", "152", 0}, {"CNY", "156", 2},
	{"COP", "170", 2}, {"CZK", "203", 2}, {"DKK", "208", 2}, {"EUR", "978", 2},
	{"GBP", "826", 2}, {"HKD", "344", 2}, {"INR", "356", 2}, {"JOD", "400", 3},
	{"JPY", "392", 0}, {"KRW", "410", 0}, {"KWD", "414", 3}, {"MXN", "484", 2},
	{"NOK", "578", 2}, {"NZD", "554", 2}, {"OMR", "512", 3}, {"PEN", "604", 2},
	{"PLN", "985", 2}, {"PYG", "600", 0}, {"SEK", "752", 2}, {"SGD", "702", 2},
	{"TND", "788", 3}, {"USD", "840", 2}, {"UYU", "858", 2}, {"ZAR", "710", 2},
}

func findCurrency(code string) (currency, bool) {
	code = strings.ToUpper(strings.TrimSpace(code))
	for _, c := range currencies {
		if c.alpha == code || c.numeric == code {
			return c, true
		}
	}
	return currency{}, false
}

// iso8583AmountDigits is the length of an ISO 8583 amount field (n 12).
const iso8583AmountDigits = 12

// convertAmount converts an amount and its currency to the form of the target
// family. To ISO 20022: minor units become a decimal amount, and the currency
// becomes alphabetic. To ISO 8583: a decimal amount becomes zero-padded minor
// units, and the currency becomes numeric. The currency may arrive in either
// form.
func convertAmount(amount, cur, targetKind string) (string, string, error) {
	c, ok := findCurrency(cur)
	if !ok {
		return "", "", fmt.Errorf("currency %q is not an ISO 4217 code that the amount conversion knows", cur)
	}
	amount = strings.TrimSpace(amount)
	if targetKind == "iso20022" {
		if !digitsOnly(amount) {
			return "", "", fmt.Errorf("amount %q is not in minor units", amount)
		}
		return minorToDecimal(amount, c.exponent), c.alpha, nil
	}
	minor, err := decimalToMinor(amount, c.exponent)
	if err != nil {
		return "", "", err
	}
	if len(minor) > iso8583AmountDigits {
		return "", "", fmt.Errorf("amount %q has more than %d digits in minor units", amount, iso8583AmountDigits)
	}
	return strings.Repeat("0", iso8583AmountDigits-len(minor)) + minor, c.numeric, nil
}

func minorToDecimal(minor string, exp int) string {
	minor = strings.TrimLeft(minor, "0")
	if len(minor) <= exp {
		minor = strings.Repeat("0", exp-len(minor)+1) + minor
	}
	if exp == 0 {
		return minor
	}
	return minor[:len(minor)-exp] + "." + minor[len(minor)-exp:]
}

func decimalToMinor(dec string, exp int) (string, error) {
	whole, frac, _ := strings.Cut(dec, ".")
	if whole == "" {
		whole = "0"
	}
	if !digitsOnly(whole) || (frac != "" && !digitsOnly(frac)) {
		return "", fmt.Errorf("amount %q is not a decimal amount", dec)
	}
	if t := strings.TrimRight(frac, "0"); len(t) > exp {
		return "", fmt.Errorf("amount %q has more decimals than the currency allows (%d)", dec, exp)
	}
	frac += strings.Repeat("0", exp)
	minor := strings.TrimLeft(whole+frac[:exp], "0")
	if minor == "" {
		minor = "0"
	}
	return minor, nil
}

func digitsOnly(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
