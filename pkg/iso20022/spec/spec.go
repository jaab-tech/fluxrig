// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

// Package spec loads an ISO 20022 protocol spec: the document of the fluxrig
// spec family that says what the fields of an ISO 20022 message mean. Its
// fields are keyed by element path, and its wire source (a library that knows
// the XSD of each message) parses the messages, so the spec holds semantics
// only.
package spec

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/jaab-tech/fluxrig/pkg/iso20022/rtp"
)

// Usage of a field in one message.
const (
	UsageMandatory = "mandatory"
	UsageOptional  = "optional"
	UsageForbidden = "forbidden"
)

// Spec is a loaded ISO 20022 protocol spec.
type Spec struct {
	ID       string
	Name     string
	Version  string
	Overview string
	Wire     string // wire source
	Profile  string // profile the wire source implements
	Catalog  map[string]Message
	Enums    map[string]*ValueSet
	Fields   map[string]Field
	Checks   []Check
	Unlisted string // what a field with no matrix entry for a message is
	// Hash is the SHA-256 of the spec's bytes, the audit link between live
	// traffic and the exact spec that shaped it.
	Hash string

	aliases map[string]map[string]string // message -> alias -> path
}

// Message is one entry of the catalog.
type Message struct {
	Name      string `yaml:"name"`
	Flow      string `yaml:"flow"`
	PairsWith string `yaml:"pairs_with"`
	Meaning   string `yaml:"meaning"`
}

// ValueSet is a named set of codes.
type ValueSet struct {
	Closed bool             `yaml:"closed"`
	Values map[string]Value `yaml:"values"`
}

// Value is one code of a value set.
type Value struct {
	Name     string `yaml:"name"`
	Meaning  string `yaml:"meaning"`
	Category string `yaml:"category"`
}

// Field is the semantic definition of one element.
type Field struct {
	Alias       string        `yaml:"alias"`
	Meaning     string        `yaml:"meaning"`
	Note        string        `yaml:"note"`
	Scope       string        `yaml:"scope"`
	Sensitivity string        `yaml:"sensitivity"`
	LogMask     *bool         `yaml:"log_mask"`
	ValuesRef   string        `yaml:"values_ref"`
	Format      Format        `yaml:"format"`
	Messages    []MessageRule `yaml:"messages"`
}

// Format is the semantic kind of a field.
type Format struct {
	Kind          string `yaml:"kind"`
	CurrencyField string `yaml:"currency_field"`
}

// MessageRule is one entry of a field's per-message matrix.
type MessageRule struct {
	Message string `yaml:"message"`
	Usage   string `yaml:"usage"`
	Note    string `yaml:"note"`
}

// Check is a named assertion over the fields of a message.
type Check struct {
	Message  []string `yaml:"message"`
	Name     string   `yaml:"name"`
	Assert   string   `yaml:"assert"`
	Severity string   `yaml:"severity"`
	Note     string   `yaml:"note"`
}

// Masked reports whether the codec masks the field in logs.
func (f Field) Masked() bool {
	if f.LogMask != nil {
		return *f.LogMask
	}
	return sensitive(f.Sensitivity)
}

// AliasPath returns the path an alias names in a message.
func (s *Spec) AliasPath(message, alias string) (string, bool) {
	p, ok := s.aliases[message][alias]
	return p, ok
}

// Aliases returns the alias -> path map of a message.
func (s *Spec) Aliases(message string) map[string]string {
	out := make(map[string]string, len(s.aliases[message]))
	for a, p := range s.aliases[message] {
		out[a] = p
	}
	return out
}

// Usage returns how a message uses a field: the matrix entry, or Unlisted.
func (s *Spec) Usage(message, path string) string {
	for _, r := range s.Fields[path].Messages {
		if r.Message == message {
			return r.Usage
		}
	}
	return s.Unlisted
}

// InMessage reports whether a field belongs to a message: the matrix names the
// message, or the field has no matrix and the message has the path.
func (s *Spec) InMessage(message, path string) bool {
	f, ok := s.Fields[path]
	if !ok {
		return false
	}
	if len(f.Messages) > 0 {
		for _, r := range f.Messages {
			if r.Message == message && r.Usage != UsageForbidden {
				return true
			}
		}
		return false
	}
	paths, err := rtp.ElementPaths(message)
	return err == nil && slices.Contains(paths, path)
}

// document is the YAML shape of a spec.
type document struct {
	Spec struct {
		Kind     string `yaml:"kind"`
		ID       string `yaml:"id"`
		Name     string `yaml:"name"`
		Version  string `yaml:"version"`
		Overview string `yaml:"overview"`
		Wire     struct {
			Source string `yaml:"source"`
		} `yaml:"wire"`
		Messages struct {
			Catalog map[string]Message `yaml:"catalog"`
		} `yaml:"messages"`
		Enums            map[string]*ValueSet `yaml:"enums"`
		Fields           map[string]Field     `yaml:"fields"`
		Checks           []Check              `yaml:"checks"`
		ValidationPolicy struct {
			Unlisted string `yaml:"unlisted"`
		} `yaml:"validation_policy"`
	} `yaml:"spec"`
}

// LoadFile loads a spec from a file.
func LoadFile(path string) (*Spec, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("iso20022 spec: %w", err)
	}
	return Load(raw)
}

// Load parses and checks a spec. Every problem is reported, each naming the
// symbol that is wrong.
func Load(raw []byte) (*Spec, error) {
	var d document
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true) // a wire attribute or a misspelled key stops the load
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("iso20022 spec: %w", err)
	}
	sum := sha256.Sum256(raw)
	s := &Spec{
		ID:       d.Spec.ID,
		Name:     d.Spec.Name,
		Version:  d.Spec.Version,
		Overview: d.Spec.Overview,
		Wire:     d.Spec.Wire.Source,
		Catalog:  d.Spec.Messages.Catalog,
		Enums:    d.Spec.Enums,
		Fields:   d.Spec.Fields,
		Checks:   d.Spec.Checks,
		Unlisted: d.Spec.ValidationPolicy.Unlisted,
		Hash:     hex.EncodeToString(sum[:]),
	}
	if s.Unlisted == "" {
		s.Unlisted = UsageOptional
	}
	var errs problems
	if d.Spec.Kind != "" && d.Spec.Kind != "protocol" {
		errs.add("spec.kind %q: a protocol spec has no kind, or kind protocol", d.Spec.Kind)
	}
	s.checkHeader(&errs)
	if s.Wire == rtp.WireSource {
		s.Profile = rtp.Profile
	}
	s.checkCatalog(&errs)
	s.checkFields(&errs)
	s.checkChecks(&errs)
	if err := errs.err(); err != nil {
		return nil, err
	}
	return s, nil
}

var versionRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

func (s *Spec) checkHeader(errs *problems) {
	if s.ID == "" {
		errs.add("spec.id is empty")
	}
	if !versionRe.MatchString(s.Version) {
		errs.add("spec.version %q is not MAJOR.MINOR.PATCH", s.Version)
	}
	if s.Wire != rtp.WireSource {
		errs.add("spec.wire.source %q is not a wire source fluxrig has; known: %s", s.Wire, rtp.WireSource)
	}
	switch s.Unlisted {
	case UsageOptional, UsageForbidden:
	default:
		errs.add("validation_policy.unlisted %q: want optional or forbidden", s.Unlisted)
	}
}

func (s *Spec) checkCatalog(errs *problems) {
	if len(s.Catalog) == 0 {
		errs.add("messages.catalog is empty")
	}
	for _, id := range sortedKeys(s.Catalog) {
		m := s.Catalog[id]
		if _, ok := rtp.Lookup(id); !ok {
			errs.add("messages.catalog %q is not a message definition of %s; known: %s", id, s.Wire, strings.Join(rtp.Definitions(), ", "))
		}
		if m.PairsWith == "" {
			continue
		}
		p, ok := s.Catalog[m.PairsWith]
		switch {
		case !ok:
			errs.add("messages.catalog %q pairs_with %q, which is not in the catalog", id, m.PairsWith)
		case p.PairsWith != id:
			errs.add("messages.catalog %q pairs_with %q, but %q pairs_with %q", id, m.PairsWith, m.PairsWith, p.PairsWith)
		}
	}
}

func (s *Spec) checkFields(errs *problems) {
	paths := map[string][]string{} // message -> element paths
	for id := range s.Catalog {
		if ps, err := rtp.ElementPaths(id); err == nil {
			paths[id] = ps
		}
	}
	s.aliases = map[string]map[string]string{}
	for _, path := range sortedKeys(s.Fields) {
		f := s.Fields[path]
		msgs := s.messagesOf(path, f, paths, errs)
		if len(msgs) == 0 {
			errs.add("fields %q: no message of the catalog has this element", path)
		}
		if f.ValuesRef != "" {
			if _, ok := s.Enums[f.ValuesRef]; !ok {
				errs.add("fields %q: values_ref %q names no value set in enums", path, f.ValuesRef)
			}
		}
		if f.LogMask != nil && !*f.LogMask && sensitive(f.Sensitivity) {
			errs.add("fields %q: log_mask false on a %s field", path, f.Sensitivity)
		}
		switch f.Sensitivity {
		case "", "none", "pii", "pan", "chd", "sad":
		default:
			errs.add("fields %q: sensitivity %q: want pan, chd, sad, pii or none", path, f.Sensitivity)
		}
		switch f.Scope {
		case "", "public", "private":
		default:
			errs.add("fields %q: scope %q: want public or private", path, f.Scope)
		}
		switch f.Format.Kind {
		case "":
		case "amount":
			if f.Format.CurrencyField != "" {
				if _, ok := s.Fields[f.Format.CurrencyField]; !ok && !s.anyMessageHas(f.Format.CurrencyField, paths) {
					errs.add("fields %q: currency_field %q is not an element path", path, f.Format.CurrencyField)
				}
			}
		default:
			errs.add("fields %q: format.kind %q: want amount", path, f.Format.Kind)
		}
		if f.Alias == "" {
			continue
		}
		for _, m := range msgs {
			if s.aliases[m] == nil {
				s.aliases[m] = map[string]string{}
			}
			if other, dup := s.aliases[m][f.Alias]; dup {
				errs.add("fields %q: alias %q already names %q in %s", path, f.Alias, other, m)
				continue
			}
			s.aliases[m][f.Alias] = path
		}
	}
}

// messagesOf returns the catalog messages a field belongs to, and checks that
// each of them has the element.
func (s *Spec) messagesOf(path string, f Field, paths map[string][]string, errs *problems) []string {
	var out []string
	if len(f.Messages) == 0 {
		for _, id := range sortedKeys(s.Catalog) {
			if slices.Contains(paths[id], path) {
				out = append(out, id)
			}
		}
		return out
	}
	for _, r := range f.Messages {
		if _, ok := s.Catalog[r.Message]; !ok {
			errs.add("fields %q: messages names %q, which is not in the catalog", path, r.Message)
			continue
		}
		switch r.Usage {
		case UsageMandatory, UsageOptional, UsageForbidden:
		default:
			errs.add("fields %q: usage %q in %s: want mandatory, optional or forbidden", path, r.Usage, r.Message)
		}
		if !slices.Contains(paths[r.Message], path) {
			errs.add("fields %q: %s has no such element", path, r.Message)
			continue
		}
		if r.Usage != UsageForbidden {
			out = append(out, r.Message)
		}
	}
	return out
}

func (s *Spec) anyMessageHas(path string, paths map[string][]string) bool {
	for _, ps := range paths {
		if slices.Contains(ps, path) {
			return true
		}
	}
	return false
}

func (s *Spec) checkChecks(errs *problems) {
	for i, c := range s.Checks {
		label := c.Name
		if label == "" {
			label = fmt.Sprintf("#%d", i)
			errs.add("checks %s: no name", label)
		}
		if strings.TrimSpace(c.Assert) == "" {
			errs.add("checks %s: no assert", label)
		}
		switch c.Severity {
		case "reject", "warn":
		default:
			errs.add("checks %s: severity %q: want reject or warn", label, c.Severity)
		}
		for _, m := range c.Message {
			if _, ok := s.Catalog[m]; !ok {
				errs.add("checks %s: message %q is not in the catalog", label, m)
			}
		}
	}
}

func sensitive(class string) bool {
	switch class {
	case "pan", "chd", "sad", "pii":
		return true
	}
	return false
}

type problems []string

func (p *problems) add(format string, args ...any) { *p = append(*p, fmt.Sprintf(format, args...)) }

func (p problems) err() error {
	if len(p) == 0 {
		return nil
	}
	return errors.New("iso20022 spec: " + strings.Join(p, "; "))
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
