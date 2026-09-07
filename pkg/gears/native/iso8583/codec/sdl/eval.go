// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package sdl

import (
	"strconv"
	"strings"
)

// Subject is the one message a `when` expression is about. It is the whole world
// the language can see: no clock, no store, no other message. That is what makes
// evaluating an expression from an untrusted spec safe.
//
// The catalogue's Message is a message *type* -- what an 0100 is. This is a
// message, the one being evaluated, and the two needed different words.
type Subject interface {
	// MTI is the message type indicator, as four characters.
	MTI() string
	// Field returns the value at a path, and whether the message carries it.
	Field(de int, subs []string) (string, bool)
}

// value is what an operand evaluates to. A message is strings on the wire, so a
// value is a string, a boolean, or absent -- there is no number kind, because
// "0210" and "210" are different data elements and only comparison decides when
// to read one as a number.
type value struct {
	absent bool
	isBool bool
	b      bool
	s      string
	// numeric says this value came from an element the spec declared as a
	// number -- an amount, a date, a time. Equality on it reads the number
	// rather than the characters, which is what makes "000000001000" equal
	// 1000 and what a padded element needs.
	numeric bool
}

func boolValue(b bool) value  { return value{isBool: true, b: b} }
func strValue(s string) value { return value{s: s} }

var absentValue = value{absent: true}

// truth reads a value in a boolean position. Absent is false, and so is a bare
// string -- which the parser does not allow anyway, since a value is not a
// condition on its own.
func (v value) truth() bool { return v.isBool && v.b }

type node interface{ eval(Subject) value }

type orNode []node

func (n orNode) eval(m Subject) value {
	for _, c := range n {
		if c.eval(m).truth() {
			return boolValue(true)
		}
	}
	return boolValue(false)
}

type andNode []node

func (n andNode) eval(m Subject) value {
	for _, c := range n {
		if !c.eval(m).truth() {
			return boolValue(false)
		}
	}
	return boolValue(true)
}

type notNode struct{ inner node }

func (n notNode) eval(m Subject) value { return boolValue(!n.inner.eval(m).truth()) }

type cmpNode struct {
	lhs, rhs node
	op       string
}

func (n cmpNode) eval(m Subject) value {
	l, r := n.lhs.eval(m), n.rhs.eval(m)

	// A comparison against something the message does not carry is false, for
	// every operator. A rule about a field's value has nothing to say when the
	// field is not there, and `present()` is how absence is asked about.
	//
	// This makes negation asymmetric on purpose: `field(39) != '00'` is false
	// for an absent DE 39, while `!(field(39) == '00')` is true. Both readings
	// are defensible and no total logic has neither; the pinned choice is that a
	// comparison never fires on what is not there.
	if l.absent || r.absent {
		return boolValue(false)
	}

	switch n.op {
	case "==", "!=":
		var same bool
		switch {
		case l.isBool || r.isBool:
			same = l.isBool && r.isBool && l.b == r.b
		case l.numeric || r.numeric:
			// One side is an element the spec declared as a number, so the
			// comparison is on the number. An amount written "000000001000"
			// equals 1000, and a spec should not have to write its own padding
			// into every rule.
			//
			// Either side is enough: the literal in `field(4) == 1000` carries
			// no kind of its own, and the element is what says how it reads.
			lf, lok := asNumber(l)
			rf, rok := asNumber(r)
			same = lok && rok && lf == rf
		default:
			// String equality, not numeric: a response code of "00" is not "0",
			// and reading them as numbers would make them equal.
			same = l.s == r.s
		}
		if n.op == "==" {
			return boolValue(same)
		}
		return boolValue(!same)
	}

	// The ordering operators are the only place a message value is read as a
	// number. Anything that is not one compares false rather than erroring: the
	// language is total, and a spec cannot make a message fail to evaluate.
	lf, lok := asNumber(l)
	rf, rok := asNumber(r)
	if !lok || !rok {
		return boolValue(false)
	}
	switch n.op {
	case "<":
		return boolValue(lf < rf)
	case "<=":
		return boolValue(lf <= rf)
	case ">":
		return boolValue(lf > rf)
	case ">=":
		return boolValue(lf >= rf)
	}
	return boolValue(false)
}

func asNumber(v value) (float64, bool) {
	if v.isBool || v.absent {
		return 0, false
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v.s), 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// fieldNode reads one element. It is a pointer because how the element compares
// is decided after parsing, when a spec is at hand to say what kind of value it
// holds -- the expression alone cannot know that DE 4 is an amount.
type fieldNode struct {
	ref FieldRef
	// numeric makes equality read the value as a number, so an amount written
	// "000000001000" equals 1000. Off by default, which is right for a code:
	// a response code of "00" is not "0".
	numeric bool
}

func (n *fieldNode) eval(m Subject) value {
	s, ok := m.Field(n.ref.DE, n.ref.Subs)
	if !ok {
		return absentValue
	}
	v := strValue(s)
	v.numeric = n.numeric
	return v
}

type presentNode struct{ ref FieldRef }

func (n presentNode) eval(m Subject) value {
	_, ok := m.Field(n.ref.DE, n.ref.Subs)
	return boolValue(ok)
}

type mtiNode struct{}

func (mtiNode) eval(m Subject) value { return strValue(m.MTI()) }

type litNode struct{ v value }

func (n litNode) eval(Subject) value { return n.v }

// Eval answers the expression against one message.
//
// It cannot fail. The language is total, side-effect-free and deterministic, so
// a spec written by someone else cannot make a message error out or hang: an
// expression that reads something absent, or compares a name to a number, is
// false rather than broken.
func (e *WhenExpr) Eval(m Subject) bool {
	if e == nil || e.root == nil {
		return false
	}
	return e.root.eval(m).truth()
}

// comparesNumerically reports whether an element of this kind is compared as a
// number rather than as the characters it is written with.
//
// The kinds are the spec's own vocabulary, and the reference already documents
// what they mean: an amount is a figure, and a date, a time or a datetime is
// compared chronologically rather than lexically. For the fixed-width, zero
// padded layouts this protocol uses, chronological and numeric orderings are the
// same -- with one honest limit: a date that carries no year (MMDD) cannot be
// ordered across a year boundary by any reading of its digits.
//
// `pan` is deliberately not here. It is an identifier that happens to be digits,
// and a leading zero on one is a different card.
func comparesNumerically(kind string) bool {
	switch kind {
	case "amount", "date", "time", "datetime", "numeric":
		return true
	}
	return false
}

// walk visits every node in a tree.
func walk(n node, fn func(node)) {
	if n == nil {
		return
	}
	fn(n)
	switch t := n.(type) {
	case orNode:
		for _, c := range t {
			walk(c, fn)
		}
	case andNode:
		for _, c := range t {
			walk(c, fn)
		}
	case notNode:
		walk(t.inner, fn)
	case cmpNode:
		walk(t.lhs, fn)
		walk(t.rhs, fn)
	}
}

// BindKinds tells the expression how each element it reads compares.
//
// It is separate from parsing because parsing happens where there is no spec --
// the resolver checks every expression in a document before the document is
// known to be coherent. Until this is called, everything compares as text, which
// is the safe reading.
func (e *WhenExpr) BindKinds(kindOf func(FieldRef) string) {
	if e == nil {
		return
	}
	walk(e.root, func(n node) {
		if f, ok := n.(*fieldNode); ok {
			f.numeric = comparesNumerically(kindOf(f.ref))
		}
	})
}
