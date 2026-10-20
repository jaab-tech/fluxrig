// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

// Package translation loads a translation spec and applies it. A translation
// spec is an independent document of the fluxrig spec family (kind:
// translation) that names two protocol specs and says which alias of one
// becomes which alias of the other, message by message. It never names a DE
// number or an element path, so it works for every dialect whose spec declares
// the same aliases.
package translation

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/iso20022/rtp"
)

// Resolver returns the bytes of a referenced asset (a protocol spec or the
// records of a lookup) and the directory that relative references inside it
// resolve against. A reference is a file path or a store artefact.
type Resolver func(ref string) ([]byte, string, error)

// Spec is a loaded translation spec.
type Spec struct {
	ID            string
	Version       string
	Hash          string
	ParticipantID string
	Protocols     map[string]Protocol
	Translations  map[string]*Translation
	tables        map[string]table
	lookups       map[string]*lookup
}

// Translation is one named translation between two messages.
type Translation struct {
	Name      string
	FromSpec  string
	From      string // source message
	ToSpec    string
	To        string // target message
	Bindings  []Binding
	fromProto Protocol
	toProto   Protocol
	parent    *Spec
}

// Binding writes one target alias.
type Binding struct {
	To       string    `yaml:"to"`
	From     stringish `yaml:"from"`
	Value    *string   `yaml:"value"`
	Lookup   string    `yaml:"lookup"`
	Generate string    `yaml:"generate"`
	Table    string    `yaml:"table"`
	Format   string    `yaml:"format"`
	Currency string    `yaml:"currency"`
}

// stringish is a YAML string or a list of strings.
type stringish []string

func (s *stringish) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		*s = []string{n.Value}
		return nil
	case yaml.SequenceNode:
		var l []string
		if err := n.Decode(&l); err != nil {
			return err
		}
		*s = l
		return nil
	}
	return fmt.Errorf("line %d: want a string or a list of strings", n.Line)
}

type table struct {
	values map[string]string
	def    *string
}

type lookup struct {
	keySpec, keyAlias string
	fields            []string
	source            string
	records           map[string]map[string]string
	hash              string
}

type document struct {
	Spec struct {
		Kind          string            `yaml:"kind"`
		ID            string            `yaml:"id"`
		Version       string            `yaml:"version"`
		Specs         map[string]string `yaml:"specs"`
		ParticipantID string            `yaml:"participant_id"`
		Lookups       map[string]struct {
			Key    string   `yaml:"key"`
			Fields []string `yaml:"fields"`
			Source string   `yaml:"source"`
		} `yaml:"lookups"`
		Tables       map[string]map[string]string `yaml:"tables"`
		Translations map[string]struct {
			From     string    `yaml:"from"`
			To       string    `yaml:"to"`
			Bindings []Binding `yaml:"bindings"`
		} `yaml:"translations"`
	} `yaml:"spec"`
}

var (
	versionRe     = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	participantRe = regexp.MustCompile(`^[A-Z0-9]{11}$`)
)

// Load parses a translation spec, loads the protocol specs it names through
// resolve, and checks the translation against them. Every problem is reported,
// each naming the symbol that is wrong.
func Load(content []byte, resolve Resolver) (*Spec, error) {
	var d document
	dec := yaml.NewDecoder(bytes.NewReader(content))
	dec.KnownFields(true)
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("translation spec: %w", err)
	}
	sum := sha256.Sum256(content)
	s := &Spec{
		ID:            d.Spec.ID,
		Version:       d.Spec.Version,
		Hash:          hex.EncodeToString(sum[:]),
		ParticipantID: d.Spec.ParticipantID,
		Protocols:     map[string]Protocol{},
		Translations:  map[string]*Translation{},
		tables:        map[string]table{},
		lookups:       map[string]*lookup{},
	}
	var errs problems
	if d.Spec.Kind != "translation" {
		errs.add("spec.kind %q: a translation spec has kind translation", d.Spec.Kind)
	}
	if s.ID == "" {
		errs.add("spec.id is empty")
	}
	if !versionRe.MatchString(s.Version) {
		errs.add("spec.version %q is not MAJOR.MINOR.PATCH", s.Version)
	}
	if s.ParticipantID != "" && !participantRe.MatchString(s.ParticipantID) {
		errs.add("participant_id %q: want 11 upper-case letters or digits", s.ParticipantID)
	}
	if len(d.Spec.Specs) == 0 {
		errs.add("specs is empty: a translation names the protocol specs it connects")
	}
	for _, name := range sortedKeys(d.Spec.Specs) {
		ref := d.Spec.Specs[name]
		raw, dir, err := resolve(ref)
		if err != nil {
			errs.add("specs %s: %q does not resolve: %v", name, ref, err)
			continue
		}
		p, err := LoadProtocol(raw, dir)
		if err != nil {
			errs.add("specs %s: %q: %v", name, ref, err)
			continue
		}
		s.Protocols[name] = p
	}
	for name, vals := range d.Spec.Tables {
		t := table{values: map[string]string{}}
		for k, v := range vals {
			if k == "default" {
				v := v
				t.def = &v
				continue
			}
			t.values[k] = v
		}
		s.tables[name] = t
	}
	for _, name := range sortedKeys(d.Spec.Lookups) {
		l := d.Spec.Lookups[name]
		spec, alias, ok := strings.Cut(l.Key, ":")
		if !ok {
			errs.add("lookups %s: key %q: want <spec>:<alias>", name, l.Key)
			continue
		}
		if l.Source == "" {
			errs.add("lookups %s: no source", name)
		}
		if len(l.Fields) == 0 {
			errs.add("lookups %s: no fields", name)
		}
		s.lookups[name] = &lookup{keySpec: spec, keyAlias: alias, fields: l.Fields, source: l.Source}
	}
	for _, name := range sortedKeys(d.Spec.Translations) {
		raw := d.Spec.Translations[name]
		t := &Translation{Name: name, Bindings: raw.Bindings, parent: s}
		t.FromSpec, t.From = splitRef(raw.From)
		t.ToSpec, t.To = splitRef(raw.To)
		t.fromProto, t.toProto = s.Protocols[t.FromSpec], s.Protocols[t.ToSpec]
		s.checkTranslation(t, &errs)
		s.Translations[name] = t
	}
	s.checkPairing(&errs)
	if err := errs.err(); err != nil {
		return nil, err
	}
	return s, nil
}

// LoadLookups reads the records of every lookup through resolve, the same
// resolver that reads the protocol specs: a source is a file path or a store
// artefact (name:tag or a content hash). The records are deployment data,
// outside the spec's hash. Each lookup keeps the hash of its records, so a log
// can say which version answered.
func (s *Spec) LoadLookups(resolve Resolver) error {
	for _, name := range sortedKeys(s.lookups) {
		l := s.lookups[name]
		raw, _, err := resolve(l.source)
		if err != nil {
			return fmt.Errorf("lookup %s: %q does not resolve: %w", name, l.source, err)
		}
		var records map[string]map[string]string
		if err := yaml.Unmarshal(raw, &records); err != nil {
			return fmt.Errorf("lookup %s: %s: %w", name, l.source, err)
		}
		for key, rec := range records {
			for f := range rec {
				if !contains(l.fields, f) {
					return fmt.Errorf("lookup %s: record %q has field %q, which the lookup does not declare", name, key, f)
				}
			}
		}
		sum := sha256.Sum256(raw)
		l.records, l.hash = records, hex.EncodeToString(sum[:])
	}
	return nil
}

// LookupSources returns each lookup's source reference and the hash of its
// records.
func (s *Spec) LookupSources() map[string]string {
	out := map[string]string{}
	for _, l := range s.lookups {
		out[l.source] = l.hash
	}
	return out
}

func (s *Spec) checkTranslation(t *Translation, errs *problems) {
	where := "translations " + t.Name
	if t.fromProto == nil {
		errs.add("%s: from %q names no spec of specs", where, t.FromSpec+":"+t.From)
	} else if !t.fromProto.HasMessage(t.From) {
		errs.add("%s: from %q: %s has no message %s", where, t.FromSpec+":"+t.From, t.fromProto.ID(), t.From)
	}
	if t.toProto == nil {
		errs.add("%s: to %q names no spec of specs", where, t.ToSpec+":"+t.To)
	} else if !t.toProto.HasMessage(t.To) {
		errs.add("%s: to %q: %s has no message %s", where, t.ToSpec+":"+t.To, t.toProto.ID(), t.To)
	}
	if t.fromProto == nil || t.toProto == nil {
		return
	}
	seen := map[string]bool{}
	for i, b := range t.Bindings {
		bw := fmt.Sprintf("%s binding %d (to %s)", where, i+1, b.To)
		if b.To == "" {
			errs.add("%s: no to", bw)
			continue
		}
		if seen[b.To] {
			errs.add("%s: %s is written twice", bw, b.To)
		}
		seen[b.To] = true
		if !t.toProto.HasAlias(t.To, b.To) {
			errs.add("%s: %q is not an alias of %s in %s", bw, b.To, t.toProto.ID(), t.To)
		}
		kinds := 0
		if len(b.From) > 0 {
			kinds++
			for _, f := range b.From {
				if !t.fromProto.HasAlias(t.From, f) {
					errs.add("%s: from %q is not an alias of %s in %s", bw, f, t.fromProto.ID(), t.From)
				}
			}
		}
		if b.Value != nil {
			kinds++
		}
		if b.Lookup != "" {
			kinds++
			name, field, ok := strings.Cut(b.Lookup, ".")
			l := s.lookups[name]
			switch {
			case !ok || l == nil:
				errs.add("%s: lookup %q: want <lookup>.<field> of a declared lookup", bw, b.Lookup)
			case !contains(l.fields, field):
				errs.add("%s: lookup %q: lookup %s has no field %s", bw, b.Lookup, name, field)
			case l.keySpec != t.FromSpec || !t.fromProto.HasAlias(t.From, l.keyAlias):
				errs.add("%s: lookup %q: its key %s:%s is not an alias of the source %s", bw, b.Lookup, l.keySpec, l.keyAlias, t.FromSpec+":"+t.From)
			}
		}
		if b.Generate != "" {
			kinds++
			switch b.Generate {
			case "now", "today", "uuid":
			case "instr_id":
				if t.toProto.Kind() != "iso20022" || s.ParticipantID == "" {
					errs.add("%s: generate instr_id needs an ISO 20022 target and the spec's participant_id", bw)
				}
			default:
				errs.add("%s: generate %q: want instr_id, now, today or uuid", bw, b.Generate)
			}
		}
		if kinds != 1 {
			errs.add("%s: want exactly one of from, value, lookup, generate; got %d", bw, kinds)
		}
		if b.Table != "" {
			if _, ok := s.tables[b.Table]; !ok {
				errs.add("%s: table %q is not declared", bw, b.Table)
			}
		}
		switch b.Format {
		case "":
			if b.Currency != "" {
				errs.add("%s: currency without format amount", bw)
			}
		case "amount":
			if len(b.From) != 1 || b.Currency == "" {
				errs.add("%s: format amount needs one from and a currency", bw)
			} else if !t.fromProto.HasAlias(t.From, b.Currency) {
				errs.add("%s: currency %q is not an alias of %s in %s", bw, b.Currency, t.fromProto.ID(), t.From)
			}
			if _, ok := t.toProto.CurrencyAlias(t.To, b.To); !ok {
				errs.add("%s: format amount: the target spec binds no currency field to %s", bw, b.To)
			}
		default:
			errs.add("%s: format %q: want amount", bw, b.Format)
		}
	}
}

// checkPairing checks that a translation and its reverse agree with the
// catalogs: if a request translates to a request, its response translates
// back to the source's response.
func (s *Spec) checkPairing(errs *problems) {
	for _, a := range s.Translations {
		if a.fromProto == nil || a.toProto == nil {
			continue
		}
		srcPair, dstPair := a.fromProto.PairsWith(a.From), a.toProto.PairsWith(a.To)
		if srcPair == "" || dstPair == "" {
			continue
		}
		for _, b := range s.Translations {
			if b.FromSpec == a.ToSpec && b.ToSpec == a.FromSpec && b.From == dstPair && b.To != srcPair {
				errs.add("translations %s goes from %s to %s, but %s pairs with %s: a reply to %s goes back as %s, not %s",
					b.Name, b.From, b.To, a.From, srcPair, a.To, srcPair, b.To)
			}
		}
	}
}

// Select returns the translation for a message: by name, or, with "auto", the
// one whose source message the fluxMsg is.
func (s *Spec) Select(name string, msg *fluxmsg.FluxMsg) (*Translation, error) {
	if name != "" && name != "auto" {
		t, ok := s.Translations[name]
		if !ok {
			return nil, fmt.Errorf("translation %q is not in spec %s", name, s.ID)
		}
		return t, nil
	}
	var match []*Translation
	for _, t := range s.Translations {
		if t.fromProto.SourceMessage(msg) == t.From {
			match = append(match, t)
		}
	}
	if len(match) != 1 {
		return nil, fmt.Errorf("translation auto: %d translations of %s start from this message", len(match), s.ID)
	}
	return match[0], nil
}

// Clock returns the current time; tests replace it.
var Clock = time.Now

// Apply translates msg in place and returns the findings: the target fields the
// message requires and does not have, and a source spec that is not the one
// the translation names. An error means the translation could not run.
func (t *Translation) Apply(msg *fluxmsg.FluxMsg) ([]string, error) {
	if msg.Metadata == nil {
		msg.Metadata = map[string]string{}
	}
	if msg.Data == nil {
		msg.Data = map[string]any{}
	}
	var findings []string
	if id, v := msg.Metadata["codec.spec_id"], msg.Metadata["codec.spec_version"]; id != "" &&
		(id != t.fromProto.ID() || v != t.fromProto.Version()) {
		findings = append(findings, fmt.Sprintf("the message was decoded with %s %s; translation %s is for %s %s",
			id, v, t.Name, t.fromProto.ID(), t.fromProto.Version()))
	}
	// Every binding reads the message as it arrived; the writes come after.
	writes := map[string]string{}
	for _, b := range t.Bindings {
		v, ok, err := t.value(b, msg)
		if err != nil {
			return findings, fmt.Errorf("translation %s, binding to %s: %w", t.Name, b.To, err)
		}
		if !ok {
			continue
		}
		if b.Format == "amount" {
			amount, cur, err := convertAmount(v, readString(msg, b.Currency), t.toProto.Kind())
			if err != nil {
				return findings, fmt.Errorf("translation %s, binding to %s: %w", t.Name, b.To, err)
			}
			v = amount
			if ca, ok := t.toProto.CurrencyAlias(t.To, b.To); ok {
				writes[ca] = cur
			}
		}
		writes[b.To] = v
	}
	for _, alias := range sortedKeys(writes) {
		if err := msg.Set(alias, writes[alias]); err != nil {
			return findings, fmt.Errorf("translation %s: write %s: %w", t.Name, alias, err)
		}
	}
	t.toProto.SetMessage(msg, t.To)
	if t.toProto.Kind() == "iso20022" && t.parent.ParticipantID != "" {
		msg.Metadata["iso20022.participant_id"] = t.parent.ParticipantID
	}
	msg.Metadata["translate.spec_id"] = t.parent.ID
	msg.Metadata["translate.spec_version"] = t.parent.Version
	msg.Metadata["translate.spec_hash"] = t.parent.Hash
	msg.Metadata["translate.name"] = t.Name
	for _, r := range t.toProto.Required(t.To) {
		if (r.Alias != "" && readString(msg, r.Alias) != "") || readString(msg, r.Path) != "" {
			continue
		}
		name := r.Path
		if r.Alias != "" {
			name = r.Alias
		}
		findings = append(findings, fmt.Sprintf("%s is required in %s and empty after translation %s", name, t.To, t.Name))
	}
	sort.Strings(findings)
	return findings, nil
}

func (t *Translation) value(b Binding, msg *fluxmsg.FluxMsg) (string, bool, error) {
	var v string
	switch {
	case b.Value != nil:
		v = *b.Value
	case len(b.From) > 0:
		var parts []string
		for _, f := range b.From {
			if s := readString(msg, f); s != "" {
				parts = append(parts, s)
			}
		}
		if len(parts) == 0 {
			return "", false, nil
		}
		v = strings.Join(parts, "/")
	case b.Lookup != "":
		name, field, _ := strings.Cut(b.Lookup, ".")
		l := t.parent.lookups[name]
		if l.records == nil {
			return "", false, fmt.Errorf("lookup %s is not loaded", name)
		}
		rec, ok := l.records[readString(msg, l.keyAlias)]
		if !ok || rec[field] == "" {
			return "", false, nil
		}
		v = rec[field]
	case b.Generate != "":
		now := Clock()
		switch b.Generate {
		case "instr_id":
			v = rtp.InstructionID(t.parent.ParticipantID, now)
		case "now":
			v = now.UTC().Format("2006-01-02T15:04:05")
		case "today":
			v = now.UTC().Format(time.DateOnly)
		case "uuid":
			v = newUUID()
		}
	}
	if b.Table != "" {
		tb := t.parent.tables[b.Table]
		out, ok := tb.values[v]
		switch {
		case ok:
			v = out
		case tb.def != nil:
			v = *tb.def
		default:
			return "", false, fmt.Errorf("table %s has no entry for %q and no default", b.Table, v)
		}
	}
	return v, true, nil
}

func readString(msg *fluxmsg.FluxMsg, key string) string {
	v, ok := msg.Get(key)
	if !ok || v == nil {
		return ""
	}
	switch v.(type) {
	case map[string]any, map[any]any:
		return ""
	}
	return fmt.Sprint(v)
}

func splitRef(ref string) (string, string) {
	spec, msg, _ := strings.Cut(ref, ":")
	return spec, msg
}

func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		n, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
		return strconv.FormatInt(n.Int64(), 16)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

type problems []string

func (p *problems) add(format string, args ...any) { *p = append(*p, fmt.Sprintf(format, args...)) }

func (p problems) err() error {
	if len(p) == 0 {
		return nil
	}
	return errors.New("translation spec: " + strings.Join(p, "; "))
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// FileResolver resolves a reference as a file path, relative to the working
// directory. A gear that has a store composes it with the store, so that the
// same reference may name a file or a store artefact.
func FileResolver(ref string) ([]byte, string, error) {
	raw, err := os.ReadFile(ref)
	if err != nil {
		return nil, "", err
	}
	return raw, filepath.Dir(ref), nil
}

// References returns the references a translation spec names: its protocol
// specs and its lookup sources. It parses only what it needs, so a Mixer can
// send a Rack every artefact a translation depends on without loading the
// protocol specs. ok is false when content is not a translation spec.
func References(content []byte) (refs []string, ok bool) {
	var d struct {
		Spec struct {
			Kind    string            `yaml:"kind"`
			Specs   map[string]string `yaml:"specs"`
			Lookups map[string]struct {
				Source string `yaml:"source"`
			} `yaml:"lookups"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(content, &d); err != nil || d.Spec.Kind != "translation" {
		return nil, false
	}
	for _, name := range sortedKeys(d.Spec.Specs) {
		refs = append(refs, d.Spec.Specs[name])
	}
	for _, name := range sortedKeys(d.Spec.Lookups) {
		if src := d.Spec.Lookups[name].Source; src != "" {
			refs = append(refs, src)
		}
	}
	return refs, true
}
