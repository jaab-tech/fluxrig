// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package translation

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"

	"gopkg.in/yaml.v3"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/gears/native/iso8583/codec/sdl"
	"github.com/jaab-tech/fluxrig/pkg/iso20022/rtp"
	iso20022spec "github.com/jaab-tech/fluxrig/pkg/iso20022/spec"
)

// Protocol is what a translation needs from a protocol spec: its messages, its
// aliases, the fields each message requires, and how a message names itself.
type Protocol interface {
	ID() string
	Version() string
	Kind() string // "iso8583" or "iso20022"
	HasMessage(message string) bool
	PairsWith(message string) string
	HasAlias(message, alias string) bool
	// CurrencyAlias returns the alias of the currency that goes with an
	// amount alias, when the spec binds one.
	CurrencyAlias(message, amountAlias string) (string, bool)
	// Required lists the fields a message requires: an alias when the field
	// has one, and the raw fluxMsg path.
	Required(message string) []Required
	// SourceMessage reads which message a decoded fluxMsg is.
	SourceMessage(msg *fluxmsg.FluxMsg) string
	// SetMessage makes a fluxMsg the given message of this protocol, for the
	// codec that encodes it.
	SetMessage(msg *fluxmsg.FluxMsg, message string)
}

// Required is one field a message requires.
type Required struct {
	Alias string
	Path  string
}

// LoadProtocol reads a protocol spec of either family.
func LoadProtocol(content []byte, baseDir string) (Protocol, error) {
	var head struct {
		Spec struct {
			Wire struct {
				Source string `yaml:"source"`
			} `yaml:"wire"`
		} `yaml:"spec"`
	}
	_ = yaml.NewDecoder(bytes.NewReader(content)).Decode(&head)
	if head.Spec.Wire.Source == rtp.WireSource {
		s, err := iso20022spec.Load(content)
		if err != nil {
			return nil, err
		}
		return iso20022Protocol{s: s}, nil
	}
	_, meta, err := sdl.LoadSpecContent(content, baseDir)
	if err != nil {
		return nil, err
	}
	sem, err := sdl.ParseSemantic(content)
	if err != nil {
		return nil, err
	}
	return iso8583Protocol{meta: meta, sem: sem}, nil
}

// iso20022Protocol adapts an ISO 20022 protocol spec.
type iso20022Protocol struct{ s *iso20022spec.Spec }

func (p iso20022Protocol) ID() string      { return p.s.ID }
func (p iso20022Protocol) Version() string { return p.s.Version }
func (p iso20022Protocol) Kind() string    { return "iso20022" }
func (p iso20022Protocol) HasMessage(m string) bool {
	_, ok := p.s.Catalog[m]
	return ok
}
func (p iso20022Protocol) PairsWith(m string) string { return p.s.Catalog[m].PairsWith }
func (p iso20022Protocol) HasAlias(m, a string) bool {
	_, ok := p.s.AliasPath(m, a)
	return ok
}
func (p iso20022Protocol) CurrencyAlias(m, amount string) (string, bool) {
	path, ok := p.s.AliasPath(m, amount)
	if !ok {
		return "", false
	}
	cur := p.s.Fields[path].Format.CurrencyField
	if cur == "" {
		return "", false
	}
	for a, ap := range p.s.Aliases(m) {
		if ap == cur {
			return a, true
		}
	}
	return "", false
}
func (p iso20022Protocol) Required(m string) []Required {
	var out []Required
	for path := range p.s.Fields {
		if p.s.Usage(m, path) != iso20022spec.UsageMandatory || !p.s.InMessage(m, path) {
			continue
		}
		r := Required{Path: "iso20022." + path}
		if a := p.s.Fields[path].Alias; a != "" {
			if ap, ok := p.s.AliasPath(m, a); ok && ap == path {
				r.Alias = a
			}
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
func (p iso20022Protocol) SourceMessage(msg *fluxmsg.FluxMsg) string {
	return msg.Metadata["iso20022.message"]
}
func (p iso20022Protocol) SetMessage(msg *fluxmsg.FluxMsg, m string) {
	msg.Metadata["iso20022.message"] = m
}

// iso8583Protocol adapts an ISO 8583 SDL spec. ISO 8583 aliases are named for
// the whole spec, not per message.
type iso8583Protocol struct {
	meta *sdl.FieldMeta
	sem  *sdl.Spec
}

func (p iso8583Protocol) ID() string      { return p.meta.SpecID }
func (p iso8583Protocol) Version() string { return p.meta.SpecVersion }
func (p iso8583Protocol) Kind() string    { return "iso8583" }
func (p iso8583Protocol) HasMessage(m string) bool {
	_, ok := p.sem.Messages.Catalog[m]
	return ok
}
func (p iso8583Protocol) PairsWith(m string) string { return p.sem.Messages.Catalog[m].PairsWith }
func (p iso8583Protocol) HasAlias(_, a string) bool {
	_, ok := p.meta.IDByAlias[a]
	return ok
}
func (p iso8583Protocol) CurrencyAlias(_, amount string) (string, bool) {
	de, ok := p.meta.IDByAlias[amount]
	if !ok {
		return "", false
	}
	cur := p.sem.Fields[de].Format.CurrencyField
	if cur == nil {
		return "", false
	}
	a, ok := p.meta.Aliases[*cur]
	return a, ok
}
func (p iso8583Protocol) Required(m string) []Required {
	var out []Required
	for de, f := range p.sem.Fields {
		if de == 0 {
			continue // the MTI is the message identity, set by SetMessage
		}
		for _, r := range f.Messages {
			if r.Usage != "mandatory" || r.When != "" || !mtiIn(r.MTI, m) {
				continue
			}
			out = append(out, Required{Alias: p.meta.Aliases[de], Path: "iso8583.field." + strconv.Itoa(de)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
func (p iso8583Protocol) SourceMessage(msg *fluxmsg.FluxMsg) string {
	return msg.Metadata["iso8583.mti"]
}
func (p iso8583Protocol) SetMessage(msg *fluxmsg.FluxMsg, m string) {
	// codec_iso8583 reads the MTI from this metadata key when it encodes.
	msg.Metadata["iso8583.mti"] = m
	if a, ok := p.meta.Aliases[0]; ok {
		_ = msg.Set(a, m)
	}
}

// mtiIn reports whether a matrix entry's mti (a string or a list) names m.
func mtiIn(v any, m string) bool {
	switch t := v.(type) {
	case string:
		return t == m
	case []any:
		for _, x := range t {
			if fmt.Sprint(x) == m {
				return true
			}
		}
	case []string:
		for _, x := range t {
			if x == m {
				return true
			}
		}
	}
	return false
}
