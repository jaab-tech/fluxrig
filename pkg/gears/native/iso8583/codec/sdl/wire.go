// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package sdl

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/moov-io/iso8583"
	"github.com/moov-io/iso8583/specs"
	"gopkg.in/yaml.v3"
)

// A spec keeps the two layers in one file, distinguished by key rather
// than by location: wire keys are Moov's own vocabulary, carried verbatim, and
// everything else is fluxrig's semantic layer. Stripping the semantic keys
// therefore derives a pure Moov document, which is what makes the wire half a
// consumed commodity rather than a reimplementation — and what the strip test
// asserts.
//
// `description` is NOT stripped, and that is the fix for a defect this list
// caused. It is Moov's word for an element's label, and a named base carries one
// for every field it declares. Deleting it here threw all of them away, which is
// why the semantic layer had to restate twenty-three labels that already
// existed: the same string in two places, drifting apart the first time one was
// corrected.
//
// The semantic layer says `name` for a label and `meaning` for the prose, so
// neither collides with Moov's word any more. `name` is still translated on the
// way out, because a spec that overrides a base's label has to say so in Moov's
// vocabulary.
var semanticFieldKeys = map[string]bool{
	"alias":       true,
	"crypto":      true,
	"meaning":     true,
	"format":      true,
	"log_mask":    true,
	"name":        true,
	"scope":       true,
	"sensitivity": true,
	"values_ref":  true,
	"validValues": true,
}

// stripSemantic returns the wire half of a field entry: Moov's keys, verbatim,
// with the label translated and composites rewritten.
func stripSemantic(in map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(in))
	for k, v := range in {
		if k == "subfields" {
			continue // handled below; the shape differs from Moov's
		}
		if !semanticFieldKeys[k] {
			out[k] = v
		}
	}
	if name, ok := in["name"].(string); ok && name != "" {
		out["description"] = name
	}

	subs, ok := in["subfields"].(map[string]any)
	if !ok {
		return out, nil
	}

	// A composite carries its parts' encoding on the parent, which is convenient
	// to author and is exactly what Moov rejects: a Composite spec must have a nil
	// Enc, because the encoding belongs to each part. Push it down rather than
	// dropping it -- deleting the key would compile and then read the parts with
	// the wrong encoding, which no test downstream would attribute to the
	// stripper.
	partEnc, _ := out["enc"].(string)
	delete(out, "enc")

	layout, _ := subs["layout"].(string)
	parts, _ := subs["parts"].([]any)

	// Two shapes reach here. fluxrig's authoring form describes a composite with
	// `layout` and a `parts` list; Moov's own form is a map of subfield entries
	// beside a `tag`. The second is the wire vocabulary this loader claims to
	// accept verbatim, so it passes through with only its semantic keys removed.
	if layout == "" && len(parts) == 0 {
		out["enc"] = partEnc // put it back: Moov-shaped composites carry their own
		if partEnc == "" {
			delete(out, "enc")
		}
		moovSubs := make(map[string]any, len(subs))
		for key, raw := range subs {
			sf, ok := raw.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("subfield %q is not a mapping", key)
			}
			w, err := stripSemantic(sf)
			if err != nil {
				return nil, fmt.Errorf("subfield %q: %w", key, err)
			}
			moovSubs[key] = w
		}
		out["subfields"] = moovSubs
		return out, nil
	}

	if len(parts) == 0 {
		return nil, fmt.Errorf("composite declares layout %q with no parts", layout)
	}

	moovSubs := make(map[string]any, len(parts))
	tag := map[string]any{"sort": "StringsByInt"}

	switch layout {
	case "tlv":
		// BER-TLV: each part is keyed by its tag. Preservation of undeclared tags
		// needs BOTH flags -- the store logic is nested inside the skip branch, so
		// setting store alone silently does nothing and unknown tags become a
		// decode error instead.
		// Three answers to a tag nobody declared, and they are genuinely three.
		// Preserving needs both flags: the storing logic sits inside the skipping
		// branch, so setting store alone does nothing and the tag becomes a decode
		// error instead — which is the opposite of what was asked for.
		switch policy, _ := subs["unknown_tags"].(string); policy {
		case "preserve":
			tag["skipUnknownTLVTags"] = true
			tag["storeUnknownTLVTags"] = true
		case "drop":
			// Read it and discard it: the message parses, and what nobody
			// declared does not travel any further.
			tag["skipUnknownTLVTags"] = true
		case "reject", "":
			// A tag outside the declared set fails the message. This is the
			// default, and it is the right default for an endpoint that owns the
			// protocol — not for a switch in the middle of one.
		}
		tag["enc"] = "BerTLVTag"
		for _, raw := range parts {
			part, ok := raw.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("composite part is not a mapping")
			}
			key, _ := part["tag"].(string)
			if key == "" {
				return nil, fmt.Errorf("tlv composite part has no tag")
			}
			moovSubs[key] = wirePart(part, partEnc, 0)
		}
	case "positional":
		// Ordered slices of a fixed-width field. The document expresses each part
		// as a character range; Moov reads subfields in key order, so the range
		// becomes an ordinal key and a length.
		for i, raw := range parts {
			part, ok := raw.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("composite part is not a mapping")
			}
			from, okF := toInt(part["from"])
			to, okT := toInt(part["to"])
			if !okF || !okT || to < from {
				return nil, fmt.Errorf("positional part %d has no usable from/to range", i+1)
			}
			moovSubs[fmt.Sprint(i+1)] = wirePart(part, partEnc, to-from+1)
		}
	default:
		return nil, fmt.Errorf("unsupported composite layout %q", layout)
	}

	out["tag"] = tag
	out["subfields"] = moovSubs
	return out, nil
}

// wirePart renders one composite part in Moov's vocabulary. Length and encoding
// fall back to the values derived from the parent, since the document lets a
// part omit what the composite already establishes.
func wirePart(part map[string]any, parentEnc string, derivedLen int) map[string]any {
	w := map[string]any{"type": "String", "prefix": "ASCII.Fixed"}
	for k, v := range part {
		switch k {
		case "type", "length", "enc", "prefix", "padding":
			w[k] = v
		case "name":
			if s, ok := v.(string); ok && s != "" {
				w["description"] = s
			}
		}
	}
	if _, ok := w["enc"]; !ok && parentEnc != "" {
		w["enc"] = parentEnc
	}
	if _, ok := w["length"]; !ok && derivedLen > 0 {
		w["length"] = derivedLen
	}
	return w
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	}
	return 0, false
}

// wireBases are the wire layers a spec may name instead of restating. Resolving
// against the linked library rather than a copied file is the point: the base
// cannot drift from upstream, and an update arrives with the dependency bump.
var wireBases = map[string]*iso8583.MessageSpec{
	"moov:spec87ascii": specs.Spec87ASCII,
	"moov:spec87hex":   specs.Spec87Hex,
}

// WireDocument derives a pure Moov spec document from a v2 fluxrig spec.
//
// Two shapes produce one: a spec that states its fields inline, and a spec that
// names a base and declares only its differences. The second is the reason the
// base can be consumed rather than copied, so the delta stays reviewable in one
// place instead of dissolving into a restated file.
// WireDocument renders the spec's wire layer as a Moov wire document.
//
// The layer can come from three places and they compose into one resolution:
// a named Moov base, a wire document on disk beside the spec, or the `wire.fields`
// block itself. Whatever the source produced, `wire.fields` is merged over it —
// so a dialect of a standard spec states only its differences, and a spec with no
// upstream base states everything, in the same place and the same vocabulary.
//
// baseDir is the directory holding the spec, and is what a file source resolves
// against.
// WireResolver fetches a wire document named as a store reference. A wire layer
// deployed as an artefact carries a version, and a reference that resolved one
// should be able to say which.
type WireResolver func(ref string) ([]byte, error)

// wireResolver is set per call rather than threaded through every signature: it
// is optional, and a spec that names a base or a file needs none.
var wireResolver WireResolver

// SetWireResolver installs the resolver used for store-referenced wire
// documents. It is process-wide because a Rack runs one store.
func SetWireResolver(r WireResolver) { wireResolver = r }

func WireDocument(data []byte, baseDir string) ([]byte, error) {
	var doc struct {
		Spec struct {
			Name string    `yaml:"name"`
			Wire wireBlock `yaml:"wire"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse spec document: %w", err)
	}
	if f := doc.Spec.Wire.Format; f != "" && f != "moov" {
		return nil, fmt.Errorf("unsupported wire format %q", f)
	}

	fields, baseName, err := baseWireFields(doc.Spec.Wire.Source, baseDir)
	if err != nil {
		return nil, err
	}

	for id, delta := range doc.Spec.Wire.Fields {
		// Merge before translating, not after. An override carries only what
		// differs, so a composite's delta names its parts and relies on the base
		// for length, prefix and the encoding its parts inherit. Translating the
		// delta alone would lose all three and produce a field Moov rejects for
		// reasons that point at the wrong place.
		merged := make(map[string]any, len(fields[id])+len(delta))
		for k, v := range fields[id] {
			merged[k] = v
		}
		for k, v := range delta {
			merged[k] = v
		}
		fields[id] = merged
	}

	if len(fields) == 0 {
		return nil, fmt.Errorf("spec names no wire source and declares no wire fields")
	}

	out := make(map[string]any, len(fields))
	for id, f := range fields {
		w, err := stripSemantic(f)
		if err != nil {
			return nil, fmt.Errorf("wire field %s: %w", id, err)
		}
		out[id] = w
	}

	name := doc.Spec.Name
	if name == "" {
		name = baseName
	}
	return yaml.Marshal(map[string]any{"name": name, "fields": out})
}

// wireBlock is the spec's `wire` section: where the wire layer comes from, and
// the fields that override it.
type wireBlock struct {
	Source string                    `yaml:"source"`
	Format string                    `yaml:"format"`
	Fields map[string]map[string]any `yaml:"fields"`
}

// baseWireFields resolves `wire.source`. An empty source is not an error: it
// means the spec carries its whole wire layer in `wire.fields`.
func baseWireFields(source, baseDir string) (map[string]map[string]any, string, error) {
	if source == "" {
		return map[string]map[string]any{}, "", nil
	}

	var raw []byte
	if isStoreRef(source) {
		if wireResolver == nil {
			return nil, "", fmt.Errorf(
				"wire source %q names a stored artefact, but no store is available to resolve it", source)
		}
		b, err := wireResolver(source)
		if err != nil {
			return nil, "", fmt.Errorf("resolve wire source %q from the store: %w", source, err)
		}
		raw = b
	} else if strings.HasPrefix(source, "moov:") {
		base, ok := wireBases[source]
		if !ok {
			return nil, "", fmt.Errorf("unknown wire source %q; known: moov:spec87ascii, moov:spec87hex", source)
		}
		b, err := specs.ExportYAML(base)
		if err != nil {
			return nil, "", fmt.Errorf("render wire base %q: %w", source, err)
		}
		raw = b
	} else {
		path, err := specRelative(source, baseDir)
		if err != nil {
			return nil, "", err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, "", fmt.Errorf("read wire source %q: %w", source, err)
		}
		raw = b
	}

	var doc struct {
		Name   string                    `yaml:"name"`
		Fields map[string]map[string]any `yaml:"fields"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, "", fmt.Errorf("parse wire source %q: %w", source, err)
	}
	if len(doc.Fields) == 0 {
		return nil, "", fmt.Errorf("wire source %q declares no fields", source)
	}
	if doc.Fields == nil {
		doc.Fields = map[string]map[string]any{}
	}
	return doc.Fields, doc.Name, nil
}

// isStoreRef reports whether a source names a stored artefact rather than a
// named base or a file: `name:tag`, or a bare content hash. A path has a
// separator or an extension; a named base carries the `moov:` scheme.
func isStoreRef(source string) bool {
	if strings.HasPrefix(source, "moov:") {
		return false
	}
	if strings.ContainsAny(source, `/\`) || strings.HasSuffix(source, ".yaml") || strings.HasSuffix(source, ".yml") {
		return false
	}
	if i := strings.LastIndex(source, ":"); i > 0 && i < len(source)-1 {
		return true
	}
	return len(source) >= 32 && isHexString(source)
}

func isHexString(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return len(s) > 0
}

// specRelative resolves a file wire source against the spec's own directory. A
// spec is deployed to Racks by the Mixer, so the path it names is untrusted
// input: an absolute path or one climbing out of the spec's directory would let
// a deployed scenario read files the operator never meant to expose.
func specRelative(rel, baseDir string) (string, error) {
	if baseDir == "" {
		return "", fmt.Errorf(
			"wire source %q is a path, and this spec was resolved from the store rather than from disk; "+
				"name a base with `moov:` or carry the wire layer in `wire.fields`", rel)
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("wire source %q must be relative to the spec", rel)
	}
	root := filepath.Clean(baseDir)
	full := filepath.Clean(filepath.Join(root, rel))
	if full != root && !strings.HasPrefix(full, root+string(filepath.Separator)) {
		return "", fmt.Errorf("wire source %q resolves outside the spec's directory", rel)
	}
	return full, nil
}

// load compiles a v2 document: the wire half through Moov, the semantic half
// into FieldMeta. Nothing here reimplements a packager.
func load(data []byte, baseDir, specHash string) (*iso8583.MessageSpec, *FieldMeta, error) {
	wire, err := WireDocument(data, baseDir)
	if err != nil {
		return nil, nil, err
	}
	moovSpec, err := specs.ImportYAML(wire)
	if err != nil {
		return nil, nil, fmt.Errorf("wire layer rejected by moov: %w", err)
	}

	var doc struct {
		Spec struct {
			ID       string `yaml:"id"`
			Version  string `yaml:"version"`
			Protocol string `yaml:"protocol"`
			Fields   map[int]struct {
				Alias       string `yaml:"alias"`
				LogMask     bool   `yaml:"log_mask"`
				Sensitivity string `yaml:"sensitivity"`
				// Composite parts are a list under `subfields.parts`, not a map:
				// TLV parts are keyed by tag, positional ones by their range, and
				// neither is a field number. Aliases on parts are optional and the
				// reference spec does not use them yet, so this reads what exists
				// and stays quiet about what does not.
				Subfields struct {
					Parts []struct {
						Tag   string `yaml:"tag"`
						Alias string `yaml:"alias"`
					} `yaml:"parts"`
				} `yaml:"subfields"`
			} `yaml:"fields"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, nil, fmt.Errorf("parse semantic layer: %w", err)
	}

	// Layer ownership, enforced rather than documented: a field entry under
	// `spec.fields` carries semantics only. Letting a wire key through there would
	// make the two layers fuse silently, and the failure would surface much later
	// as a field that parses differently than the wire document says it does.
	if err := rejectWireKeysInSemanticLayer(data); err != nil {
		return nil, nil, err
	}

	// Every cross-reference the semantic layer makes, checked once, here. A spec
	// that names something it never declared must fail the whole load: a Rack
	// that accepted it has already told the Mixer it is serving that protocol,
	// and discovering the gap per transaction is discovering it in production.
	var semantic specDoc
	if err := yaml.Unmarshal(data, &semantic); err != nil {
		return nil, nil, fmt.Errorf("parse semantic layer: %w", err)
	}
	if err := resolveReferences(&semantic); err != nil {
		return nil, nil, err
	}

	protocol := doc.Spec.Protocol
	if protocol == "" {
		protocol = "iso8583"
	}
	// A spec states a contract, and a contract with no version is one nobody can
	// refer to. Scenarios have refused an empty version since they existed; the
	// schema asked the same of specs and nothing enforced it, so a spec with no
	// version at all loaded clean.
	if err := validateSpecVersion(doc.Spec.ID, doc.Spec.Version); err != nil {
		return nil, nil, err
	}

	meta := &FieldMeta{
		SpecHash:    specHash,
		SpecID:      doc.Spec.ID,
		SpecVersion: doc.Spec.Version,
		Protocol:    protocol,
		Aliases:     make(map[int]string),
		SubAliases:  make(map[int]map[string]string),
		IDByAlias:   make(map[string]int),
		SecureIDs:   make(map[int]bool),
	}

	ids := make([]int, 0, len(doc.Spec.Fields))
	for id := range doc.Spec.Fields {
		ids = append(ids, id)
	}
	sort.Ints(ids)

	for _, id := range ids {
		f := doc.Spec.Fields[id]
		if f.Alias != "" {
			meta.Aliases[id] = f.Alias
			meta.IDByAlias[f.Alias] = id
		}
		// A field is secure if it says so directly, or if its PCI classification
		// makes it so. Deriving from `sensitivity` is the point of carrying the
		// classification: a spec that marks DE 2 as `pan` should not also have to
		// remember to set log_mask, and forgetting one of the two is exactly how a
		// PAN reaches a log.
		if f.LogMask || isSensitive(f.Sensitivity) {
			meta.SecureIDs[id] = true
		}
		if len(f.Subfields.Parts) > 0 {
			subs := make(map[string]string)
			for i, sf := range f.Subfields.Parts {
				if sf.Alias == "" {
					continue
				}
				key := sf.Tag
				if key == "" {
					key = fmt.Sprint(i + 1) // positional parts are keyed by ordinal
				}
				subs[key] = sf.Alias
			}
			if len(subs) > 0 {
				meta.SubAliases[id] = subs
			}
		}
	}
	return moovSpec, meta, nil
}

// isSensitive reports whether a PCI classification implies the value must never
// reach a log or an analytics store in the clear. `none` and an absent value do
// not; everything the schema enumerates does.
func isSensitive(class string) bool {
	switch class {
	case "pan", "chd", "sad", "pii":
		return true
	default:
		return false
	}
}

// wireOnlyKeys belong to the wire layer — a named base, a wire document, or the
// spec's own `wire.fields` — and never to a semantic field entry. The rule holds
// for every spec now that `wire.fields` can carry a whole wire layer: a spec with
// no upstream base has somewhere to put these, so there is no shape left that has
// to fuse the two.
var wireOnlyKeys = []string{"type", "length", "enc", "prefix", "padding", "tag", "description"}

// wireKeyHint says what to write instead, for the keys where "it belongs in the
// wire layer" is true and unhelpful. `description` is the whole reason this
// exists: someone arriving from Moov writes the key they know for a label, and
// without a hint the error tells them where it does not go and not where it does.
var wireKeyHint = map[string]string{
	"description": "the wire layer uses `description` for an element's label and already carries one " +
		"for every field a named base declares. Override it with `name`, or write the prose as `meaning`",
}

func rejectWireKeysInSemanticLayer(data []byte) error {
	var doc struct {
		Spec struct {
			Fields map[string]map[string]any `yaml:"fields"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parse spec document: %w", err)
	}
	ids := make([]int, 0, len(doc.Spec.Fields))
	byID := map[int]map[string]any{}
	for id, f := range doc.Spec.Fields {
		n, err := strconv.Atoi(id)
		if err != nil {
			return fmt.Errorf("field key %q is not a number", id)
		}
		ids = append(ids, n)
		byID[n] = f
	}
	sort.Ints(ids)

	for _, id := range ids {
		for _, k := range wireOnlyKeys {
			if _, found := byID[id][k]; found {
				if hint, ok := wireKeyHint[k]; ok {
					return fmt.Errorf("field %d declares %q under spec.fields: %s", id, k, hint)
				}
				return fmt.Errorf(
					"field %d declares the wire key %q under spec.fields; wire attributes belong in spec.wire.fields",
					id, k)
			}
		}
	}
	return nil
}
