// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package protodoc

import (
	"fmt"
	"strings"
)

// Rendering YAML so it can be read.
//
// A thousand lines in one block is a block nobody reads. This colours the parts
// and folds by indentation, and it is written here rather than pulled from a
// highlighting library because the page fetches nothing: a reference opened from
// an attachment has to render on its own.
//
// It is a renderer, not a parser. The document has already been parsed and
// validated by the loader; what this needs is to tell a key from a value from a
// comment, and indentation from everything else.

type yamlLine struct {
	indent   int
	num      int // the line's number in the document it came from
	text     string
	children []*yamlLine
	// foldable is a line that opens a block: a key with nothing after the colon
	// and something indented under it.
	foldable bool
}

// renderYAML colours a document and folds it by indentation. openTo is how many
// levels start open; deeper ones are folded, because the point of folding is
// that the top of a large document fits on a screen.
// startLine is the number of the first line as it sits in the document this text
// came from. A fragment numbered from one is a fragment the reader then has to
// go and find in the file.
func renderYAML(text string, openTo, startLine int) string {
	// A zero start means the text belongs to no file, so it carries no numbers.
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	roots := buildYAMLTree(lines, startLine)
	var b strings.Builder
	for _, n := range roots {
		writeYAMLNode(&b, n, 0, openTo)
	}
	return b.String()
}

func buildYAMLTree(lines []string, startLine int) []*yamlLine {
	var roots []*yamlLine
	var stack []*yamlLine

	for i, raw := range lines {
		// A zero start means the text belongs to no file, so no line of it is
		// numbered — not the first, and not the ones after it either.
		num := 0
		if startLine > 0 {
			num = startLine + i
		}
		if strings.TrimSpace(raw) == "" {
			// A blank line is still a line. Dropping it would shift every number
			// after it, which is the one thing numbering must not do.
			blank := &yamlLine{indent: 1 << 30, num: num, text: ""}
			if len(stack) == 0 {
				roots = append(roots, blank)
			} else {
				stack[len(stack)-1].children = append(stack[len(stack)-1].children, blank)
			}
			continue
		}
		n := &yamlLine{indent: len(raw) - len(strings.TrimLeft(raw, " ")), num: num, text: raw}
		for len(stack) > 0 && stack[len(stack)-1].indent >= n.indent {
			stack = stack[:len(stack)-1]
		}
		if len(stack) == 0 {
			roots = append(roots, n)
		} else {
			parent := stack[len(stack)-1]
			parent.children = append(parent.children, n)
		}
		stack = append(stack, n)
	}
	markFoldable(roots)
	return roots
}

func markFoldable(nodes []*yamlLine) {
	for _, n := range nodes {
		n.foldable = len(n.children) > 0
		markFoldable(n.children)
	}
}

// blanks carry no indentation of their own, so they never open a block.

func writeYAMLNode(b *strings.Builder, n *yamlLine, depth, openTo int) {
	if !n.foldable {
		fmt.Fprintf(b, `<div class="yl">%s%s</div>`, gutter(n.num), highlightYAMLLine(n.text))
		return
	}
	open := ""
	if depth < openTo {
		open = " open"
	}
	fmt.Fprintf(b, `<details class="yf"%s><summary>%s%s</summary><div class="yc">`,
		open, gutter(n.num), highlightYAMLLine(n.text))
	for _, c := range n.children {
		writeYAMLNode(b, c, depth+1, openTo)
	}
	b.WriteString("</div></details>")
}

// highlightYAMLLine colours one line: its indentation, a list dash, a key, and
// whatever the value turns out to be.
func highlightYAMLLine(line string) string {
	indent := len(line) - len(strings.TrimLeft(line, " "))
	rest := line[indent:]
	var b strings.Builder
	b.WriteString(strings.Repeat(" ", indent))

	if strings.HasPrefix(rest, "#") {
		b.WriteString(span("yc-com", rest))
		return b.String()
	}
	if strings.HasPrefix(rest, "- ") {
		b.WriteString(span("yc-dash", "- "))
		rest = rest[2:]
	} else if rest == "-" {
		b.WriteString(span("yc-dash", "-"))
		return b.String()
	}

	// Trailing comments belong to the line, not to the value.
	body, comment := splitYAMLComment(rest)

	if key, value, ok := splitYAMLKey(body); ok {
		b.WriteString(span("yc-key", key))
		b.WriteString(span("yc-punct", ":"))
		if value != "" {
			b.WriteString(" ")
			b.WriteString(highlightYAMLValue(strings.TrimLeft(value, " ")))
		}
	} else {
		b.WriteString(highlightYAMLValue(body))
	}
	if comment != "" {
		b.WriteString(span("yc-com", comment))
	}
	return b.String()
}

// splitYAMLKey finds the colon that separates a key from its value, ignoring
// colons inside a quoted key — an MTI is written "0100", and a title can carry
// one too.
func splitYAMLKey(s string) (key, value string, ok bool) {
	inQuote := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inQuote != 0:
			if c == inQuote {
				inQuote = 0
			}
		case c == '"' || c == '\'':
			inQuote = c
		case c == ':':
			if i+1 < len(s) && s[i+1] != ' ' {
				continue // a colon inside a plain value, not a separator
			}
			return s[:i], s[i+1:], true
		}
	}
	return "", "", false
}

// splitYAMLComment separates a trailing comment, leaving one inside a quoted
// string alone.
func splitYAMLComment(s string) (body, comment string) {
	inQuote := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inQuote != 0:
			if c == inQuote {
				inQuote = 0
			}
		case c == '"' || c == '\'':
			inQuote = c
		case c == '#' && i > 0 && s[i-1] == ' ':
			return s[:i], s[i:]
		}
	}
	return s, ""
}

func highlightYAMLValue(v string) string {
	// A value the reference defines carries its meaning. This is done on the
	// token rather than by rewriting the rendered text: a replacement pass over
	// marked-up output eventually matches a word inside an attribute it wrote
	// itself, and shears the markup in half.
	if t, ok := define(strings.Trim(v, `"'`)); ok && isWireTerm(t.Group) {
		word := strings.Trim(v, `"'`)
		return fmt.Sprintf(`<a class="term yc-type" href="#help-%s" title="%s">%s</a>`,
			esc(word), esc(t.Short), esc(v))
	}
	switch {
	case v == "":
		return ""
	case strings.HasPrefix(v, "\"") || strings.HasPrefix(v, "'"):
		return span("yc-str", v)
	case strings.HasPrefix(v, "[") || strings.HasPrefix(v, "{"):
		return span("yc-flow", v)
	case v == "true" || v == "false" || v == "null" || v == "~":
		return span("yc-bool", v)
	case isNumeric(v):
		return span("yc-num", v)
	default:
		return span("yc-plain", v)
	}
}

func isNumeric(s string) bool {
	dots := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '.' {
			dots++
			continue
		}
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(s) > 0 && dots <= 1
}

// gutter is the line's number, unselectable so copying the block yields the
// YAML and not a column of digits down its left.
func gutter(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf(`<span class="yn" aria-hidden="true">%d</span>`, n)
}

// isWireTerm reports whether a group describes the bytes rather than the
// meaning. Only those appear as values in a wire block.
func isWireTerm(group string) bool {
	return group == "Wire types" || group == "Encodings" || group == "Length prefixes"
}

func span(class, text string) string {
	return `<span class="` + class + `">` + esc(text) + `</span>`
}
