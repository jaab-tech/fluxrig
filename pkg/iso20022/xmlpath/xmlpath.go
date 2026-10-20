// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

// Package xmlpath maps the Go types that XSD code generators produce to flat
// paths of XML element names: "CdtTrfTxInf.PmtId.InstrId" for an element,
// "CdtTrfTxInf.IntrBkSttlmAmt@Ccy" for an attribute. The path is built from the
// struct's xml tags, never from Go field names, so it is the schema's own name.
package xmlpath

import (
	"encoding"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// Flatten returns every leaf value of v, keyed by its path. Absent optional
// elements (nil pointers) and empty values are left out.
func Flatten(v any) (map[string]string, error) {
	out := map[string]string{}
	if err := flatten(reflect.ValueOf(v), "", out); err != nil {
		return nil, err
	}
	return out, nil
}

// Assign writes values into v, which must be a non-nil pointer to a struct. A
// path that the type does not have is an error, so a typo in a binding or a
// field of another message surfaces instead of being dropped.
func Assign(v any, values map[string]string) error {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("xmlpath: assign needs a non-nil pointer to a struct, got %T", v)
	}
	paths := make([]string, 0, len(values))
	for p := range values {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if err := assign(rv.Elem(), splitPath(p), values[p]); err != nil {
			return fmt.Errorf("xmlpath: %s: %w", p, err)
		}
	}
	return nil
}

// Paths lists every leaf path of the type of v, in order. A repeated element
// (a slice) is not addressable by a flat path and is left out.
func Paths(v any) []string {
	t := reflect.TypeOf(v)
	var out []string
	paths(t, "", map[reflect.Type]bool{}, &out)
	sort.Strings(out)
	return out
}

type member struct {
	index int
	name  string // element or attribute name; empty for chardata
	attr  bool
}

func members(t reflect.Type) []member {
	var out []member
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() || f.Name == "XMLName" {
			continue
		}
		tag := f.Tag.Get("xml")
		if tag == "-" || tag == "" {
			continue
		}
		parts := strings.Split(tag, ",")
		name := parts[0]
		if sp := strings.LastIndex(name, " "); sp >= 0 {
			name = name[sp+1:]
		}
		m := member{index: i, name: name}
		for _, opt := range parts[1:] {
			switch opt {
			case "attr":
				m.attr = true
			case "chardata":
				m.name = ""
			}
		}
		if m.name == "" && !hasOpt(parts[1:], "chardata") {
			continue // ",attr" with no name (namespace bags) or ",any"
		}
		out = append(out, m)
	}
	return out
}

func hasOpt(opts []string, want string) bool {
	for _, o := range opts {
		if o == want {
			return true
		}
	}
	return false
}

func join(prefix string, m member) string {
	switch {
	case m.name == "":
		return prefix
	case m.attr:
		return prefix + "@" + m.name
	case prefix == "":
		return m.name
	default:
		return prefix + "." + m.name
	}
}

var textMarshaler = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
var textUnmarshaler = reflect.TypeOf((*encoding.TextUnmarshaler)(nil)).Elem()

// leaf reports whether t is written as text rather than walked as a struct.
func leaf(t reflect.Type) bool {
	if t.Implements(textMarshaler) || reflect.PointerTo(t).Implements(textUnmarshaler) {
		return true
	}
	return t.Kind() != reflect.Struct
}

func flatten(v reflect.Value, prefix string, out map[string]string) error {
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	if v.Kind() == reflect.Slice {
		return nil
	}
	if leaf(v.Type()) {
		s, err := text(v)
		if err != nil {
			return err
		}
		if s != "" && prefix != "" {
			out[prefix] = s
		}
		return nil
	}
	for _, m := range members(v.Type()) {
		if err := flatten(v.Field(m.index), join(prefix, m), out); err != nil {
			return err
		}
	}
	return nil
}

func text(v reflect.Value) (string, error) {
	if v.Type().Implements(textMarshaler) {
		if v.IsZero() {
			return "", nil
		}
		b, err := v.Interface().(encoding.TextMarshaler).MarshalText()
		return string(b), err
	}
	switch v.Kind() {
	case reflect.String:
		return v.String(), nil
	case reflect.Float32, reflect.Float64:
		if v.Float() == 0 {
			return "", nil
		}
		return strconv.FormatFloat(v.Float(), 'f', -1, 64), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10), nil
	case reflect.Bool:
		return strconv.FormatBool(v.Bool()), nil
	}
	return "", fmt.Errorf("unsupported leaf type %s", v.Type())
}

func splitPath(p string) []string {
	var out []string
	for i, seg := range strings.Split(p, ".") {
		if at := strings.Index(seg, "@"); at >= 0 {
			if seg[:at] != "" {
				out = append(out, seg[:at])
			}
			out = append(out, "@"+seg[at+1:])
			continue
		}
		if seg == "" && i == 0 {
			continue
		}
		out = append(out, seg)
	}
	return out
}

func assign(v reflect.Value, segs []string, value string) error {
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		v = v.Elem()
	}
	if leaf(v.Type()) {
		if len(segs) != 0 {
			return fmt.Errorf("%q goes below a value", strings.Join(segs, "."))
		}
		return setText(v, value)
	}
	ms := members(v.Type())
	if len(segs) == 0 {
		for _, m := range ms { // the element's own text
			if m.name == "" {
				return assign(v.Field(m.index), nil, value)
			}
		}
		return fmt.Errorf("element has no text value")
	}
	want, attr := segs[0], strings.HasPrefix(segs[0], "@")
	want = strings.TrimPrefix(want, "@")
	for _, m := range ms {
		if m.name == want && m.attr == attr {
			f := v.Field(m.index)
			if f.Kind() == reflect.Slice {
				return fmt.Errorf("%q repeats; repeated elements have no flat path", want)
			}
			return assign(f, segs[1:], value)
		}
	}
	return fmt.Errorf("no element %q", segs[0])
}

func setText(v reflect.Value, s string) error {
	if reflect.PointerTo(v.Type()).Implements(textUnmarshaler) {
		return v.Addr().Interface().(encoding.TextUnmarshaler).UnmarshalText([]byte(s))
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString(s)
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return fmt.Errorf("%q is not a number", s)
		}
		v.SetFloat(f)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return fmt.Errorf("%q is not an integer", s)
		}
		v.SetInt(n)
	case reflect.Bool:
		b, err := strconv.ParseBool(s)
		if err != nil {
			return fmt.Errorf("%q is not a boolean", s)
		}
		v.SetBool(b)
	default:
		return fmt.Errorf("unsupported leaf type %s", v.Type())
	}
	return nil
}

func paths(t reflect.Type, prefix string, seen map[reflect.Type]bool, out *[]string) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() == reflect.Slice {
		return
	}
	if leaf(t) {
		if prefix != "" {
			*out = append(*out, prefix)
		}
		return
	}
	if seen[t] { // recursive schema types end here
		return
	}
	seen[t] = true
	defer delete(seen, t)
	for _, m := range members(t) {
		paths(t.Field(m.index).Type, join(prefix, m), seen, out)
	}
}
