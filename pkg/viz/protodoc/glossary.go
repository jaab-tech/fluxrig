// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package protodoc

import (
	"sort"
	"strings"
)

// The vocabulary this reference uses, defined where it is used.
//
// A term is explained beside the table it appears in, not in a preamble the
// reader passed twenty pages ago. These are the generator's own words — the
// usages, the provenance values, the layouts, the classifications — and they
// mean the same in every spec rendered by it. What a protocol means by its own
// elements is the spec's to say, in its overview.

// Term is one word the reference uses and what it means.
type Term struct {
	// Key is the word as it appears in the document, and the anchor it links to.
	Key string
	// Group is the question the term answers, which is what makes a list of
	// forty words readable.
	Group string
	Short string // one line, shown on hover
	Long  string // the entry in the help section
}

var glossary = map[string]Term{
	"mandatory": {Group: "Usage", Short: "The element must be present.",
		Long: "The element must be present in that message. A message without it is malformed, whatever else it carries."},
	"optional": {Group: "Usage", Short: "It may be present.",
		Long: "The element may be present. Saying so is not the same as leaving it unlisted: it records that someone considered it and decided either way is correct."},
	"forbidden": {Group: "Usage", Short: "It must not be present.",
		Long: "The element must not be present. A request carrying a response code is the usual example — the answer to a question nobody has answered yet."},
	"conditional": {Group: "Usage", Short: "Which of the above applies depends on the message's contents.",
		Long: "The element has more than one rule in this message, and which applies depends on what else the message carries. The rules are listed beside it under When, in the order they are evaluated: the first match wins."},

	"echo": {Group: "Response value", Short: "Carries the request's value; a difference is a fault.",
		Long: "The response must carry the same value the request did. It is the sharpest conformance check available: a trace number that comes back changed means correlation is broken, and the switch that changed it is the fault."},
	"new": {Group: "Response value", Short: "Originated by the responder.",
		Long: "The responder produced this value; there was nothing in the request to echo. A response code is the case — the issuer decides it, and comparing it to anything in the request is meaningless."},
	"modified": {Group: "Response value", Short: "Derived from the request's value and may legitimately differ.",
		Long: "The value comes from the request's but may differ, and the difference is the point. A partial approval returns less than the amount asked for, so this must not be compared for equality — only that it relates."},
	"varies": {Group: "Response value", Short: "The rules for this element disagree; see the element's own section.",
		Long: "This element's rules for the message do not agree on where its value comes from. The distinction is per case, and the element's own section carries it."},

	"tlv": {Group: "Composites", Short: "Parts carry their own tag and length.",
		Long: "Each part announces itself with a tag and a length, so a message may carry parts this document does not list. Those are preserved on the way through rather than dropped: a switch that discards what it did not recognise breaks the parties either side of it."},
	"positional": {Group: "Composites", Short: "Parts are fixed slices, read by offset.",
		Long: "The element is a fixed-width string cut into parts at fixed offsets. Nothing in the message says where one part ends, so the layout is the only thing that knows."},
	"bitmapped": {Group: "Composites", Short: "A bitmap says which parts are present.",
		Long: "A leading bitmap says which parts the element carries, the same way a message's bitmap says which elements it carries."},

	"pan": {Group: "Classification", Short: "Primary account number — masked wherever it is written.",
		Long: "The cardholder's account number. It is masked in logs and console output wherever it appears, and the masking follows the classification: it cannot be switched off by the spec that declared it. It is digits and it is not a number: a leading zero makes it a different card, so a rule compares the characters it carries."},
	"chd": {Group: "Classification", Short: "Cardholder data — masked.",
		Long: "Cardholder data under the PCI definition: masked in logs and console output."},
	"sad": {Group: "Classification", Short: "Sensitive authentication data — masked, never stored.",
		Long: "Sensitive authentication data — a PIN block, a cryptogram, track data. Masked in logs, and never retained after authorization."},
	"pii": {Group: "Classification", Short: "Personal data — masked.",
		Long: "Personal data about the cardholder rather than the card. Masked in logs and console output."},

	// The wire vocabulary is Moov's, carried verbatim. These say what each word
	// does to the bytes, which is what a reader looking at a wire block needs.
	"String": {Group: "Wire types", Short: "Characters, in the field's encoding.",
		Long: "The value is characters in the field's encoding. Most elements are this, including numeric ones that travel as digits rather than as packed numbers."},
	"Numeric": {Group: "Wire types", Short: "Digits, right-aligned and zero-padded.",
		Long: "Digits, right-aligned and padded with zeros to the declared length."},
	"Binary": {Group: "Wire types", Short: "Raw bytes.",
		Long: "Raw bytes, passed through without interpretation."},
	"Bitmap": {Group: "Wire types", Short: "The map of which elements are present.",
		Long: "The bitmap itself: one bit per element, saying which the message carries. A second bitmap follows when the first says so."},
	"Composite": {Group: "Wire types", Short: "An element made of parts.",
		Long: "An element built from parts rather than holding one value. How the parts are found is the layout — positional, tlv or bitmapped."},
	"Hex": {Group: "Wire types", Short: "Bytes written as hex characters.",
		Long: "Bytes written as pairs of hexadecimal characters, so one byte occupies two."},
	"Track2": {Group: "Wire types", Short: "Magnetic stripe track 2.",
		Long: "Track 2 of a magnetic stripe, with its own separator between the account number and the rest."},

	"ASCII": {Group: "Encodings", Short: "One byte per character.",
		Long: "Characters as ASCII, one byte each. The most common encoding in a modern interface."},
	"BCD": {Group: "Encodings", Short: "Two digits packed into each byte.",
		Long: "Binary-coded decimal: two digits share a byte, so a six-digit value occupies three. Halves the bytes on the wire and doubles the ways to get the length wrong."},
	"EBCDIC": {Group: "Encodings", Short: "IBM mainframe character encoding.",
		Long: "The character encoding of IBM mainframes, still in use wherever the host at one end of the link is one."},
	"BerTLVTag": {Group: "Encodings", Short: "A BER-TLV tag, one or two bytes.",
		Long: "A BER-TLV tag as EMV writes them: one byte, or two when the first says the tag continues."},

	"ASCII.Fixed": {Group: "Length prefixes", Short: "No prefix; the length is fixed.",
		Long: "There is no length on the wire. The element is exactly as long as the spec says, always."},
	"ASCII.LL": {Group: "Length prefixes", Short: "Two ASCII digits give the length.",
		Long: "Two ASCII digits precede the value and give its length, so the element can be up to 99 long."},
	"ASCII.LLL": {Group: "Length prefixes", Short: "Three ASCII digits give the length.",
		Long: "Three ASCII digits precede the value and give its length, up to 999."},
	"BerTLV": {Group: "Length prefixes", Short: "A BER length, one or more bytes.",
		Long: "A BER length: one byte for short values, or a byte saying how many length bytes follow for longer ones."},

	"amount": {Group: "Value kinds", Short: "A figure in the minor unit of its currency, compared as a number.",
		Long: "A figure in the minor unit of the currency named beside it: 1000 is 10.00 where the currency has two decimals, and 1000 where it has none. Dividing by a fixed hundred is the mistake this kind exists to prevent. A rule compares it as a number, so it matches whatever padding the element is written with."},
	"date": {Group: "Value kinds", Short: "A date, compared chronologically.",
		Long: "A date in the layout the element declares. Comparisons on it are chronological rather than lexical, so an expiry compares as a date and not as a string of digits."},
	"time": {Group: "Value kinds", Short: "A time, compared chronologically.",
		Long: "A time in the layout the element declares, compared chronologically."},
	"datetime": {Group: "Value kinds", Short: "A date and time, compared chronologically.",
		Long: "A combined date and time in the layout the element declares, compared chronologically."},
	"numeric": {Group: "Value kinds", Short: "A number, compared as one.",
		Long: "A value that is a number rather than a code: a trace number, a sequence, a count. A rule compares it as a number, so the padding it is written with does not matter. This is the difference from an element with no kind at all, which compares as the characters it carries -- and has to, because a response code of \"00\" is not \"0\"."},
}

// glossaryOrder is the order the help section reads in: general first, then the
// distinctions that only matter once the general ones are understood.
var glossaryOrder = []string{"Usage", "Response value", "Composites", "Value kinds", "Classification",
	"Wire types", "Encodings", "Length prefixes"}

// Glossary returns the terms grouped for the help section.
func Glossary() []TermGroup {
	byGroup := map[string][]Term{}
	for key, t := range glossary {
		t.Key = key
		byGroup[t.Group] = append(byGroup[t.Group], t)
	}
	out := make([]TermGroup, 0, len(byGroup))
	for _, g := range glossaryOrder {
		terms := byGroup[g]
		if len(terms) == 0 {
			continue
		}
		sort.Slice(terms, func(i, j int) bool { return terms[i].Key < terms[j].Key })
		out = append(out, TermGroup{Name: g, Terms: terms})
	}
	return out
}

// TermGroup is the terms that answer one question.
type TermGroup struct {
	Name  string
	Terms []Term
}

// Anchor is the group's own id, so a reader can be sent to it.
func (g TermGroup) Anchor() string {
	return "words-" + strings.ToLower(strings.ReplaceAll(g.Name, " ", "-"))
}

// define returns the one-line meaning of a word, if the reference defines it.
func define(word string) (Term, bool) {
	t, ok := glossary[word]
	t.Key = word
	return t, ok
}
