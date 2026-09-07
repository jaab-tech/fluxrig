// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package sdl

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// SourceIndex is a spec's text, cut into the parts a reader asks about.
//
// The fragments are lifted from the document as written, not re-serialised from
// what was parsed. A regenerated fragment is a claim about the source rather
// than the source itself: it loses the comments, reorders what the author
// ordered, and quietly differs from the file on disk in ways nobody can see.
// Anyone checking where a rule came from is checking the file, so the file is
// what they get.
type SourceIndex struct {
	Whole    string
	Fields   map[int]Fragment
	Messages map[string]Fragment
	Enums    map[string]Fragment
}

// Fragment is a slice of the document and where it sits in it. The line matters
// as much as the text: a reader checking a rule against the file wants the line
// to open it at, and a fragment numbered from one is a fragment they then have
// to go and find.
type Fragment struct {
	Text string
	// Line is 1-based, and counts from the first line of the fragment as it
	// appears in the document — including a comment written above the key.
	Line int
}

// IndexSource cuts a spec document into its parts.
func IndexSource(data []byte) (*SourceIndex, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse spec document: %w", err)
	}
	if len(root.Content) == 0 {
		return nil, fmt.Errorf("spec document is empty")
	}
	lines := strings.Split(string(data), "\n")
	cut := &cutter{lines: lines, keys: collectKeys(root.Content[0])}

	idx := &SourceIndex{
		Whole:    strings.TrimRight(string(data), "\n"),
		Fields:   map[int]Fragment{},
		Messages: map[string]Fragment{},
		Enums:    map[string]Fragment{},
	}
	spec := child(root.Content[0], "spec")
	if spec == nil {
		return idx, nil
	}

	if fields := child(spec, "fields"); fields != nil {
		for i := 0; i+1 < len(fields.Content); i += 2 {
			var de int
			if _, err := fmt.Sscanf(fields.Content[i].Value, "%d", &de); err != nil {
				continue
			}
			idx.Fields[de] = cut.block(fields.Content[i])
		}
	}
	if messages := child(spec, "messages"); messages != nil {
		if catalog := child(messages, "catalog"); catalog != nil {
			for i := 0; i+1 < len(catalog.Content); i += 2 {
				idx.Messages[catalog.Content[i].Value] = cut.block(catalog.Content[i])
			}
		}
	}
	if enums := child(spec, "enums"); enums != nil {
		for i := 0; i+1 < len(enums.Content); i += 2 {
			idx.Enums[enums.Content[i].Value] = cut.block(enums.Content[i])
		}
	}
	return idx, nil
}

func child(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// keyPos is where a mapping key sits. Column matters as much as line: a block
// ends where the next key at the same or shallower depth begins.
type keyPos struct{ line, column int }

func collectKeys(n *yaml.Node) []keyPos {
	var out []keyPos
	var walk func(*yaml.Node)
	walk = func(node *yaml.Node) {
		if node == nil {
			return
		}
		if node.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(node.Content); i += 2 {
				out = append(out, keyPos{node.Content[i].Line, node.Content[i].Column})
				walk(node.Content[i+1])
			}
			return
		}
		for _, c := range node.Content {
			walk(c)
		}
	}
	walk(n)
	sort.Slice(out, func(i, j int) bool { return out[i].line < out[j].line })
	return out
}

type cutter struct {
	lines []string
	keys  []keyPos
}

// block returns the text of one mapping entry, from its key to the line before
// whatever follows it at the same depth, and the line it starts on.
func (c *cutter) block(key *yaml.Node) Fragment {
	start := key.Line - 1 // 1-based to 0-based
	if start < 0 || start >= len(c.lines) {
		return Fragment{}
	}
	// A comment sitting directly above a key was written about it, and cutting
	// at the key alone leaves the reader the rule without the reason for it.
	for start > 0 {
		prev := strings.TrimSpace(c.lines[start-1])
		if strings.HasPrefix(prev, "#") {
			start--
			continue
		}
		break
	}

	end := len(c.lines) // exclusive
	for _, k := range c.keys {
		if k.line > key.Line && k.column <= key.Column {
			end = k.line - 1
			break
		}
	}
	// Trailing blank lines and any comment block introducing what comes next
	// belong to the next entry, not this one.
	for end > start {
		t := strings.TrimSpace(c.lines[end-1])
		if t == "" || strings.HasPrefix(t, "#") {
			end--
			continue
		}
		break
	}
	if end <= start {
		return Fragment{}
	}
	return Fragment{Text: dedent(c.lines[start:end]), Line: start + 1}
}

// dedent removes the indentation the fragment shares, so a field lifted from six
// levels down reads on its own rather than as a stripe down the right.
func dedent(lines []string) string {
	width := -1
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		n := len(l) - len(strings.TrimLeft(l, " "))
		if width < 0 || n < width {
			width = n
		}
	}
	if width <= 0 {
		return strings.Join(lines, "\n")
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		if len(l) >= width {
			out[i] = l[width:]
			continue
		}
		out[i] = strings.TrimLeft(l, " ")
	}
	return strings.Join(out, "\n")
}

// WireFields renders each field's wire layer as it ends up: the named base and
// the spec's own overrides, resolved.
//
// A spec that names `moov:spec87ascii` and changes the padding on one element
// carries only the change, so the fragment it wrote says almost nothing about
// how the bytes are read. What a reader needs is the result — the type, the
// length, the encoding and the prefix that actually parse the field — and that
// exists only after the base and the delta are merged.
func WireFields(data []byte, baseDir string) (map[int]string, error) {
	wire, err := WireDocument(data, baseDir)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Fields map[string]yaml.Node `yaml:"fields"`
	}
	if err := yaml.Unmarshal(wire, &doc); err != nil {
		return nil, fmt.Errorf("parse resolved wire document: %w", err)
	}
	out := make(map[int]string, len(doc.Fields))
	for key, node := range doc.Fields {
		var de int
		if _, err := fmt.Sscanf(key, "%d", &de); err != nil {
			continue
		}
		rendered, err := yaml.Marshal(node)
		if err != nil {
			continue
		}
		out[de] = strings.TrimRight(string(rendered), "\n")
	}
	return out, nil
}

// WireLabels returns each element's label from the resolved wire layer.
//
// `description` is moov's word for an element's label, and a named base already
// carries one for every field it declares. Restating it in the semantic layer
// would be the same string in two places, drifting apart the first time one is
// corrected, so the semantic `name` is an override and this is what it overrides.
func WireLabels(data []byte, baseDir string) (map[int]string, error) {
	wire, err := WireDocument(data, baseDir)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Fields map[string]struct {
			Description string `yaml:"description"`
		} `yaml:"fields"`
	}
	if err := yaml.Unmarshal(wire, &doc); err != nil {
		return nil, fmt.Errorf("parse resolved wire document: %w", err)
	}
	out := make(map[int]string, len(doc.Fields))
	for key, f := range doc.Fields {
		var de int
		if _, err := fmt.Sscanf(key, "%d", &de); err != nil {
			continue
		}
		if f.Description != "" {
			out[de] = f.Description
		}
	}
	return out, nil
}
