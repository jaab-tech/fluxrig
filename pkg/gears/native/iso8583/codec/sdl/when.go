// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package sdl

import (
	"fmt"
	"strings"
)

// The `when` expression language, parsed at load time.
//
// It is a small, total, side-effect-free boolean language over a single message.
// Nothing here evaluates: this is the half the loader needs, which is whether an
// expression is well formed and which parts of the message it reads. A spec that
// names a field it never declared, or that fails to parse, is rejected before any
// traffic is served — an expression is a claim about the catalog, and a claim
// nothing checks is how a spec loads clean and does nothing.
//
// Grammar (ADR 0047):
//
//	expr       = or_expr
//	or_expr    = and_expr { "||" and_expr }
//	and_expr   = not_expr { "&&" not_expr }
//	not_expr   = "!" not_expr | primary
//	primary    = "(" expr ")" | predicate | comparison | bool_lit
//	predicate  = "present" "(" path ")"
//	comparison = operand op operand
//	op         = "==" | "!=" | "<" | "<=" | ">" | ">="
//	operand    = field_ref | "mti" | string_lit | number_lit | bool_lit
//	field_ref  = "field" "(" path ")"
//	path       = de [ "." sub { "." sub } ]

// maxWhenDepth bounds nesting so a pathological spec cannot blow the parser's
// stack, and later the evaluator's. The limit is part of the contract, not a
// tuning knob.
const maxWhenDepth = 32

// FieldRef is one message location an expression reads: a data element, and the
// subfield path under it if any. Pos is the byte offset of the path in the
// source expression, so an error can point at it.
type FieldRef struct {
	DE   int
	Subs []string
	Pos  int
}

// String renders a reference the way the expression wrote it.
func (r FieldRef) String() string {
	if len(r.Subs) == 0 {
		return fmt.Sprint(r.DE)
	}
	return fmt.Sprintf("%d.%s", r.DE, strings.Join(r.Subs, "."))
}

// WhenExpr is a parsed expression. Refs lists every location it reads, in the
// order they appear, which is what the resolver checks against the catalog and
// what dependency cycles are computed from.
type WhenExpr struct {
	Source string
	Refs   []FieldRef
	// root is the parsed tree. The parser used to be a recogniser -- it proved
	// an expression was well formed and threw the shape away -- so nothing could
	// answer it against a message.
	root node
}

// ParseWhen parses an expression and reports what it reads. It does not
// evaluate: evaluation needs a message, and this runs while there is none.
func ParseWhen(src string) (*WhenExpr, error) {
	p := &parser{src: src}
	p.next()
	root, err := p.parseExpr(0)
	if err != nil {
		return nil, err
	}
	if p.err != nil {
		return nil, p.err
	}
	if p.tok.kind != tokEOF {
		return nil, p.errAt(p.tok.pos, "unexpected %s after the end of the expression", p.tok.describe())
	}
	return &WhenExpr{Source: src, Refs: p.refs, root: root}, nil
}

type tokKind int

const (
	tokEOF tokKind = iota
	tokIdent
	tokString
	tokNumber
	tokOp  // == != < <= > >=
	tokAnd // &&
	tokOr  // ||
	tokNot // !
	tokLParen
	tokRParen
)

type token struct {
	kind tokKind
	text string
	pos  int
}

func (t token) describe() string {
	if t.kind == tokEOF {
		return "end of expression"
	}
	return fmt.Sprintf("%q", t.text)
}

type parser struct {
	src  string
	pos  int
	tok  token
	refs []FieldRef
	err  error
}

// fail records a lexer error. The scanner has no error return of its own — it
// hands the parser an EOF token and the message travels here.
func (p *parser) fail(pos int, format string, args ...any) {
	_ = p.errAt(pos, format, args...)
}

func (p *parser) errAt(pos int, format string, args ...any) error {
	if p.err != nil {
		return p.err
	}
	// The caret column is 1-based so it reads like every other compiler.
	p.err = fmt.Errorf("%s (at position %d in %q)", fmt.Sprintf(format, args...), pos+1, p.src)
	return p.err
}

func (p *parser) parseExpr(depth int) (node, error) {
	if depth > maxWhenDepth {
		return nil, p.errAt(p.tok.pos, "expression nests deeper than %d levels", maxWhenDepth)
	}
	first, err := p.parseAnd(depth + 1)
	if err != nil {
		return nil, err
	}
	alts := orNode{first}
	for p.tok.kind == tokOr {
		p.next()
		next, errA := p.parseAnd(depth + 1)
		if errA != nil {
			return nil, errA
		}
		alts = append(alts, next)
	}
	if len(alts) == 1 {
		return alts[0], nil
	}
	return alts, nil
}

func (p *parser) parseAnd(depth int) (node, error) {
	if depth > maxWhenDepth {
		return nil, p.errAt(p.tok.pos, "expression nests deeper than %d levels", maxWhenDepth)
	}
	first, err := p.parseNot(depth + 1)
	if err != nil {
		return nil, err
	}
	all := andNode{first}
	for p.tok.kind == tokAnd {
		p.next()
		next, errA := p.parseNot(depth + 1)
		if errA != nil {
			return nil, errA
		}
		all = append(all, next)
	}
	if len(all) == 1 {
		return all[0], nil
	}
	return all, nil
}

func (p *parser) parseNot(depth int) (node, error) {
	if depth > maxWhenDepth {
		return nil, p.errAt(p.tok.pos, "expression nests deeper than %d levels", maxWhenDepth)
	}
	if p.tok.kind == tokNot {
		p.next()
		inner, err := p.parseNot(depth + 1)
		if err != nil {
			return nil, err
		}
		return notNode{inner: inner}, nil
	}
	return p.parsePrimary(depth + 1)
}

func (p *parser) parsePrimary(depth int) (node, error) {
	if depth > maxWhenDepth {
		return nil, p.errAt(p.tok.pos, "expression nests deeper than %d levels", maxWhenDepth)
	}
	if p.tok.kind == tokLParen {
		p.next()
		inner, err := p.parseExpr(depth + 1)
		if err != nil {
			return nil, err
		}
		if p.tok.kind != tokRParen {
			return nil, p.errAt(p.tok.pos, "expected ')', found %s", p.tok.describe())
		}
		p.next()
		return inner, nil
	}

	// `present(path)` is a condition on its own, and it is also a boolean value:
	// the reference spec writes `present(35) == false`, which the ADR gives as a
	// worked example while its EBNF lists only field/mti/literals as operands.
	// The prose and the shipped spec agree with each other, so they win.
	if p.tok.kind == tokIdent && p.tok.text == "present" {
		lhs, err := p.parseCall("present")
		if err != nil {
			return nil, err
		}
		if p.tok.kind != tokOp {
			return lhs, nil
		}
		op := p.tok.text
		p.next()
		rhs, err := p.parseOperand()
		if err != nil {
			return nil, err
		}
		return cmpNode{lhs: lhs, op: op, rhs: rhs}, nil
	}

	// A bare boolean literal stands alone; anything else opens a comparison.
	if p.tok.kind == tokIdent && (p.tok.text == "true" || p.tok.text == "false") {
		lhs := litNode{v: boolValue(p.tok.text == "true")}
		p.next()
		if p.tok.kind != tokOp {
			return lhs, nil
		}
		op := p.tok.text
		p.next()
		rhs, err := p.parseOperand()
		if err != nil {
			return nil, err
		}
		return cmpNode{lhs: lhs, op: op, rhs: rhs}, nil
	}

	first := p.tok
	lhs, err := p.parseOperand()
	if err != nil {
		return nil, err
	}
	if p.tok.kind != tokOp {
		return nil, p.errAt(first.pos, "%s is not a condition on its own; compare it with ==, !=, <, <=, > or >=", first.describe())
	}
	op := p.tok.text
	p.next()
	rhs, err := p.parseOperand()
	if err != nil {
		return nil, err
	}
	return cmpNode{lhs: lhs, op: op, rhs: rhs}, nil
}

// parseOperand consumes one operand. Callers that already read the left-hand
// side call it for the right-hand side alone.
func (p *parser) parseOperand() (node, error) {
	switch p.tok.kind {
	case tokIdent:
		switch p.tok.text {
		case "field":
			return p.parseCall("field")
		case "present":
			return p.parseCall("present")
		case "mti":
			p.next()
			return mtiNode{}, nil
		case "true", "false":
			n := litNode{v: boolValue(p.tok.text == "true")}
			p.next()
			return n, nil
		default:
			return nil, p.errAt(p.tok.pos, "unknown name %q; expected field(...), present(...), mti, a quoted string, a number, true or false", p.tok.text)
		}
	case tokString, tokNumber:
		// The token keeps the quotes it was written with, because that is what
		// an error message should show the author. The value does not.
		//
		// A number keeps the digits it was written with. Only an ordering
		// comparison reads it as a number, because "00" and "0" are different
		// values of a data element.
		n := litNode{v: strValue(unquote(p.tok.text))}
		p.next()
		return n, nil
	default:
		return nil, p.errAt(p.tok.pos, "expected a value, found %s", p.tok.describe())
	}
}

// parseCall reads `name(path)` and records the path it addresses.
func (p *parser) parseCall(name string) (node, error) {
	callPos := p.tok.pos
	p.next()
	if p.tok.kind != tokLParen {
		return nil, p.errAt(p.tok.pos, "expected '(' after %s", name)
	}
	p.next()
	if p.tok.kind != tokIdent && p.tok.kind != tokNumber {
		return nil, p.errAt(p.tok.pos, "expected a field path inside %s(...), found %s", name, p.tok.describe())
	}
	ref, err := parsePath(p.tok.text, p.tok.pos)
	if err != nil {
		return nil, p.errAt(p.tok.pos, "%v", err)
	}
	p.refs = append(p.refs, ref)
	p.next()
	if p.tok.kind != tokRParen {
		return nil, p.errAt(p.tok.pos, "expected ')' to close %s(...) opened at position %d, found %s", name, callPos+1, p.tok.describe())
	}
	p.next()
	if name == "present" {
		return presentNode{ref: ref}, nil
	}
	return &fieldNode{ref: ref}, nil
}

// parsePath splits `39`, `55.9F02` or `3.1` into a data element and its subfield
// path.
func parsePath(text string, pos int) (FieldRef, error) {
	parts := strings.Split(text, ".")
	de := 0
	if parts[0] == "" {
		return FieldRef{}, fmt.Errorf("a field path needs a data element number")
	}
	for _, r := range parts[0] {
		if r < '0' || r > '9' {
			return FieldRef{}, fmt.Errorf("%q is not a data element number", parts[0])
		}
		de = de*10 + int(r-'0')
	}
	ref := FieldRef{DE: de, Pos: pos}
	for _, s := range parts[1:] {
		if s == "" {
			return FieldRef{}, fmt.Errorf("a subfield path segment is empty in %q", text)
		}
		ref.Subs = append(ref.Subs, s)
	}
	return ref, nil
}

func (p *parser) next() {
	for p.pos < len(p.src) && (p.src[p.pos] == ' ' || p.src[p.pos] == '\t' || p.src[p.pos] == '\n') {
		p.pos++
	}
	if p.pos >= len(p.src) {
		p.tok = token{kind: tokEOF, pos: p.pos}
		return
	}
	start := p.pos
	c := p.src[p.pos]

	switch {
	case c == '(':
		p.pos++
		p.tok = token{tokLParen, "(", start}
	case c == ')':
		p.pos++
		p.tok = token{tokRParen, ")", start}
	case c == '\'':
		p.pos++
		for p.pos < len(p.src) && p.src[p.pos] != '\'' {
			p.pos++
		}
		if p.pos >= len(p.src) {
			// Report the opening quote: that is where the author's mistake is.
			p.tok = token{tokEOF, "", start}
			p.fail(start, "string literal is never closed")
			return
		}
		p.pos++
		p.tok = token{tokString, p.src[start:p.pos], start}
	case c == '&' || c == '|':
		if p.pos+1 < len(p.src) && p.src[p.pos+1] == c {
			p.pos += 2
			kind := tokAnd
			if c == '|' {
				kind = tokOr
			}
			p.tok = token{kind, p.src[start:p.pos], start}
			return
		}
		p.pos++
		p.tok = token{tokEOF, string(c), start}
		p.fail(start, "single %q; the operators are && and ||", string(c))
	case c == '!':
		if p.pos+1 < len(p.src) && p.src[p.pos+1] == '=' {
			p.pos += 2
			p.tok = token{tokOp, "!=", start}
			return
		}
		p.pos++
		p.tok = token{tokNot, "!", start}
	case c == '=' || c == '<' || c == '>':
		p.pos++
		if p.pos < len(p.src) && p.src[p.pos] == '=' {
			p.pos++
		} else if c == '=' {
			p.fail(start, "single '='; comparison is written ==")
			p.tok = token{tokEOF, "=", start}
			return
		}
		p.tok = token{tokOp, p.src[start:p.pos], start}
	case isPathRune(c):
		for p.pos < len(p.src) && isPathRune(p.src[p.pos]) {
			p.pos++
		}
		text := p.src[start:p.pos]
		kind := tokIdent
		if isAllDigits(text) {
			kind = tokNumber
		}
		p.tok = token{kind, text, start}
	default:
		p.pos++
		p.tok = token{tokEOF, string(c), start}
		p.fail(start, "unexpected character %q", string(c))
	}
}

// isPathRune covers identifiers, numbers and dotted field paths in one class:
// `field`, `9F02` and `55.9F02` all lex as a single token, and the parser tells
// them apart by position.
func isPathRune(c byte) bool {
	return c == '_' || c == '.' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func isAllDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}

// unquote strips the single quotes a string literal is written with.
func unquote(text string) string {
	if len(text) >= 2 && text[0] == '\'' && text[len(text)-1] == '\'' {
		return text[1 : len(text)-1]
	}
	return text
}
