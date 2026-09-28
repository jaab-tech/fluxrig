// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package sdk

import (
	"testing"

	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

func TestGetValue(t *testing.T) {
	msg := fluxmsg.New()
	id1 := uuid.New()
	id2 := uuid.New()
	msg.FluxID = id1
	msg.TraceID = "test-trace-67890"
	msg.SrcGearID = id2
	msg.RawPayload = []byte("hello world")
	msg.Metadata = map[string]string{
		"iso8583.mti": "1200",
		"rack.id":     "5",
	}
	msg.Data = map[string]any{
		"amount": 100,
		"user": map[string]any{
			"id":   "user-1",
			"name": "John Doe",
		},
	}

	tests := []struct {
		path  string
		want  any
		found bool
	}{
		{"payload", "hello world", true},
		{"flux_id", id1, true},
		{"trace_id", "test-trace-67890", true},
		{"src_id", id2, true},
		{"meta.iso8583.mti", "1200", true},
		{"meta.rack.id", "5", true},
		{"data.amount", 100, true},
		{"data.user.id", "user-1", true},
		{"data.user.name", "John Doe", true},
		{"data.none", nil, false},
		{"meta.none", "", false},
		{"invalid", nil, false},
		// A bare path, with neither the "data." nor the "meta." prefix, is the
		// dialect FluxMsg.Get already uses everywhere else in this codebase
		// (the conductor, the iso8583 codec) for a path like
		// "iso8583.field.2". Before this fix, GetValue accepted only the
		// "data."-prefixed form and returned not-found for these two,
		// silently, even though the identical string (minus "data.") already
		// resolved through msg.Get.
		{"amount", 100, true},
		{"user.id", "user-1", true},
	}

	for _, tt := range tests {
		got, found := GetValue(msg, tt.path)
		if found != tt.found {
			t.Errorf("GetValue(%s) found = %v, want %v", tt.path, found, tt.found)
		}
		if got != tt.want {
			t.Errorf("GetValue(%s) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestJoinKeys(t *testing.T) {
	if got := JoinKeys("a", "b", "c"); got != "1:a1:b1:c" {
		t.Errorf("JoinKeys(a,b,c) = %s, want 1:a1:b1:c", got)
	}
	if got := JoinKeys("single"); got != "6:single" {
		t.Errorf("JoinKeys(single) = %s, want 6:single", got)
	}
}

// TestJoinKeys_NoSeparatorCollision is a regression test for the finding that
// motivated the netstring-style rewrite: a fixed separator, however chosen,
// collides whenever a part's own content can contain it, and a structured
// field can be arbitrary binary with no byte excluded. "a_b"+"c" and "a"+"b_c"
// must never join to the same string.
func TestJoinKeys_NoSeparatorCollision(t *testing.T) {
	a := JoinKeys("a_b", "c")
	b := JoinKeys("a", "b_c")
	if a == b {
		t.Errorf("JoinKeys(%q) collided with JoinKeys(%q): both gave %q", []string{"a_b", "c"}, []string{"a", "b_c"}, a)
	}
}
