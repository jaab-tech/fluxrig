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
	if got := JoinKeys("a", "b", "c"); got != "a_b_c" {
		t.Errorf("JoinKeys(a,b,c) = %s, want a_b_c", got)
	}
	if got := JoinKeys("single"); got != "single" {
		t.Errorf("JoinKeys(single) = %s, want single", got)
	}
}
