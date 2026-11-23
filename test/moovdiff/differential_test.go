// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

//go:build moovdiff

// A differential of moov-io/iso8583 across a version bump: encoders, sorts,
// Track 2, and pack and unpack of the built-in specs and of those in this
// repository. Run it in a tree of each version and compare the two files:
// the cases are generated from fixed seeds, so line N is the same case.
//
//	MOOVDIFF_OUT=/tmp/new.tsv go test -tags moovdiff ./test/moovdiff/

package moovdiff

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/moov-io/iso8583"
	"github.com/moov-io/iso8583/encoding"
	"github.com/moov-io/iso8583/field"
	"github.com/moov-io/iso8583/prefix"
	moovsort "github.com/moov-io/iso8583/sort"
	"github.com/moov-io/iso8583/specs"

	"github.com/jaab-tech/fluxrig/pkg/gears/native/iso8583/codec/sdl"
)

var out *bufio.Writer

func short(s string) string {
	if len(s) > 120 {
		return fmt.Sprintf("%s..%x", s[:40], sha256.Sum256([]byte(s)))
	}
	return s
}

func rec(cat string, in string, f func() string) {
	r := func() (r string) {
		defer func() {
			if p := recover(); p != nil {
				r = fmt.Sprintf("panic: %v", p)
			}
		}()
		return f()
	}()
	fmt.Fprintf(out, "%s\t%q\t%q\n", cat, in, short(r))
}

const (
	digits = "0123456789"
	hexu   = "0123456789ABCDEF"
	mixed  = "0123456789abcdefABCDEFgxz =D?^/%\x00\xff"
)

func rs(r *rand.Rand, set string, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = set[r.Intn(len(set))]
	}
	return string(b)
}

func rb(r *rand.Rand, n int) []byte {
	b := make([]byte, n)
	r.Read(b)
	return b
}

func sample(r *rand.Rand, n int) []byte {
	switch r.Intn(4) {
	case 0:
		return []byte(rs(r, digits, n))
	case 1:
		return []byte(rs(r, hexu, n))
	case 2:
		return []byte(rs(r, mixed, n))
	}
	return rb(r, n)
}

func encoders(r *rand.Rand) {
	for name, e := range []encoding.Encoder{encoding.ASCII, encoding.BCD, encoding.LBCD, encoding.Binary,
		encoding.BytesToASCIIHex, encoding.ASCIIHexToBytes, encoding.EBCDIC, encoding.EBCDIC1047, encoding.BerTLVTag} {
		for i := 0; i < 30000; i++ {
			b := sample(r, r.Intn(34))
			rec(fmt.Sprintf("enc%d.encode", name), string(b), func() string {
				v, err := e.Encode(slices.Clone(b))
				return fmt.Sprintf("%x %v", v, err)
			})
			l := r.Intn(len(b)+3) - 1
			rec(fmt.Sprintf("enc%d.decode", name), fmt.Sprintf("%x/%d", b, l), func() string {
				v, n, err := e.Decode(slices.Clone(b), l)
				return fmt.Sprintf("%x %d %v", v, n, err)
			})
		}
	}
}

func sorts(r *rand.Rand) {
	fns := []moovsort.StringSlice{moovsort.Strings, moovsort.StringsByInt, moovsort.StringsByHex}
	for k, fn := range fns {
		for _, kind := range []string{"valid", "odd", "mixed"} {
			for i := 0; i < 20000; i++ {
				x := make([]string, r.Intn(9)+1)
				for j := range x {
					switch kind {
					case "valid":
						x[j] = rs(r, hexu, 2*(r.Intn(3)+1))
					case "odd":
						x[j] = rs(r, hexu, r.Intn(6)+1)
					default:
						x[j] = rs(r, "0123456789abcdefABgz", r.Intn(6)+1)
					}
				}
				rec(fmt.Sprintf("sort%d.%s", k, kind), strings.Join(x, ","), func() string {
					y := slices.Clone(x)
					fn(y)
					return strings.Join(y, ",")
				})
			}
		}
	}
}

func track2(r *rand.Rand) {
	for i := 0; i < 200000; i++ {
		var s string
		if r.Intn(5) == 0 {
			s = rs(r, mixed, r.Intn(38))
		} else {
			s = rs(r, digits, r.Intn(21)) + rs(r, "=D=D^", 1) + rs(r, digits, r.Intn(8)+1) + rs(r, "0123456789?D=", r.Intn(6))
		}
		rec("track2", s, func() string {
			spec := &field.Spec{Length: 37, Enc: encoding.ASCII, Pref: prefix.ASCII.LL}
			f := field.NewTrack2(spec)
			if err := f.SetBytes([]byte(s)); err != nil {
				return "err " + err.Error()
			}
			p, err := f.Pack()
			return fmt.Sprintf("%x %v", p, err)
		})
	}
}

func value(r *rand.Rand, f field.Field) []byte {
	n := f.Spec().Length
	if n <= 0 || n > 60 {
		n = 24
	}
	if r.Intn(3) == 0 {
		n = r.Intn(n) + 1
	}
	switch f.(type) {
	case *field.Track2:
		return []byte(rs(r, digits, 16) + "=" + rs(r, digits, 7+r.Intn(8)))
	case *field.Composite:
		tlv := [][]byte{{0x9f, 0x02, 6}, {0x5f, 0x2a, 2}, {0x9f, 0x26, 8}, {0x82, 2}, {0x95, 5}, {0x9f, 0x36, 2}}
		var b []byte
		for _, j := range r.Perm(len(tlv))[:r.Intn(len(tlv))+1] {
			b = append(b, tlv[j]...)
			b = append(b, rb(r, int(tlv[j][len(tlv[j])-1]))...)
		}
		if r.Intn(2) == 0 {
			return []byte(rs(r, digits, n))
		}
		return b
	case *field.Binary:
		if r.Intn(2) == 0 {
			return rb(r, n)
		}
	}
	return []byte(rs(r, digits, n))
}

func dump(m *iso8583.Message) string {
	fs := m.GetFields()
	var ids []int
	for id := range fs {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var sb strings.Builder
	for _, id := range ids {
		s, err := fs[id].String()
		fmt.Fprintf(&sb, "%d=%s/%v|", id, s, err)
	}
	return sb.String()
}

func messages(r *rand.Rand, name string, spec *iso8583.MessageSpec, n int) {
	var ids []int
	for id := range spec.Fields {
		if id > 1 {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	mtis := []string{"0100", "0110", "0200", "0420", "0800"}
	for i := 0; i < n; i++ {
		m := iso8583.NewMessage(spec)
		mti := mtis[r.Intn(len(mtis))]
		if f, ok := spec.Fields[0]; ok && f.Spec().Length == 8 {
			mti = "0000" + mti
		}
		m.MTI(mti)
		in := mti
		for j := r.Intn(7) + 1; j > 0 && len(ids) > 0; j-- {
			id := ids[r.Intn(len(ids))]
			v := value(r, spec.Fields[id])
			in += fmt.Sprintf(" %d=%x", id, v)
			rec("set."+name, fmt.Sprintf("%d=%x", id, v), func() string { return fmt.Sprint(m.BinaryField(id, v)) })
		}
		var wire []byte
		rec("pack."+name, in, func() string {
			p, err := m.Pack()
			wire = p
			return fmt.Sprintf("%x %v", p, err)
		})
		if len(wire) == 0 {
			continue
		}
		bad := slices.Clone(wire)
		bad[r.Intn(len(bad))] = byte(r.Intn(256))
		for _, w := range [][]byte{wire, bad, wire[:r.Intn(len(wire))]} {
			rec("unpack."+name, fmt.Sprintf("%x", w), func() string {
				u := iso8583.NewMessage(spec)
				if err := u.Unpack(w); err != nil {
					return "err " + err.Error()
				}
				return dump(u)
			})
		}
	}
}

func TestDifferential(t *testing.T) {
	f, err := os.Create(os.Getenv("MOOVDIFF_OUT"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out = bufio.NewWriter(f)
	defer out.Flush()
	encoders(rand.New(rand.NewSource(1)))
	sorts(rand.New(rand.NewSource(2)))
	track2(rand.New(rand.NewSource(3)))
	messages(rand.New(rand.NewSource(4)), "spec87ascii", specs.Spec87ASCII, 20000)
	messages(rand.New(rand.NewSource(5)), "spec87hex", specs.Spec87Hex, 20000)
	var files []string
	for _, g := range []string{"../../examples/specs/*.yaml", "../../pkg/gears/native/iso8583/codec/sdl/testdata/*.yaml",
		"../../test/e2e/segments/group_a/iso8583_family/specs_10/*.yaml", "../../test/robot/suites/*/specs/*.yaml"} {
		m, _ := filepath.Glob(g)
		files = append(files, m...)
	}
	slices.Sort(files)
	loaded := 0
	for i, p := range files {
		spec, _, err := sdl.LoadSpec(p)
		rec("load", p, func() string { return fmt.Sprint(err) })
		if err != nil || spec == nil {
			continue
		}
		loaded++
		messages(rand.New(rand.NewSource(int64(100+i))), p, spec, 5000)
	}
	t.Logf("RESULT loaded %d of %d spec files", loaded, len(files))
}
