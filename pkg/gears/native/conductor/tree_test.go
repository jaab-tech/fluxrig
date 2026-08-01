// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package conductor

import (
	"testing"
)

func allUp(string) bool { return true }
func noLoad(string) int { return 0 }

func mustParse(t *testing.T, v any) destNode {
	t.Helper()
	n, err := parseDest(v)
	if err != nil {
		t.Fatalf("parseDest: %v", err)
	}
	return n
}

func TestParseDestValidation(t *testing.T) {
	cases := []struct {
		name string
		in   any
	}{
		{"non-out leaf", "scheme_a"},
		{"unknown strategy", map[string]any{"weighted": []any{"out.a"}}},
		{"empty children", map[string]any{"failover": []any{}}},
		{"two keys", map[string]any{"failover": []any{"out.a"}, "round_robin": []any{"out.b"}}},
		{"wrong type", 42},
	}
	for _, tc := range cases {
		if _, err := parseDest(tc.in); err == nil {
			t.Errorf("%s: accepted invalid destination", tc.name)
		}
	}
}

func TestFailoverOrder(t *testing.T) {
	n := mustParse(t, map[string]any{"failover": []any{"out.a", "out.b"}})

	if got := n.pick(selection{available: allUp, load: noLoad}); got != "out.a" {
		t.Fatalf("pick = %q, want out.a (strict order)", got)
	}
	aDown := func(p string) bool { return p != "out.a" }
	if got := n.pick(selection{available: aDown, load: noLoad}); got != "out.b" {
		t.Fatalf("pick with a down = %q, want out.b", got)
	}
	if got := n.pick(selection{available: func(string) bool { return false }, load: noLoad}); got != "" {
		t.Fatalf("pick with all down = %q, want empty", got)
	}
}

func TestRoundRobinRotates(t *testing.T) {
	n := mustParse(t, map[string]any{"round_robin": []any{"out.a", "out.b"}})
	s := selection{available: allUp, load: noLoad}

	seen := map[string]int{}
	for i := 0; i < 4; i++ {
		seen[n.pick(s)]++
	}
	if seen["out.a"] != 2 || seen["out.b"] != 2 {
		t.Fatalf("rotation uneven: %v", seen)
	}

	bOnly := selection{available: func(p string) bool { return p == "out.b" }, load: noLoad}
	for i := 0; i < 3; i++ {
		if got := n.pick(bOnly); got != "out.b" {
			t.Fatalf("pick = %q, want out.b (a unavailable)", got)
		}
	}
}

func TestLeastLoadedPicksIdleAndRotatesTies(t *testing.T) {
	n := mustParse(t, map[string]any{"least_loaded": []any{"out.a", "out.b"}})

	loads := map[string]int{"out.a": 5, "out.b": 1}
	s := selection{available: allUp, load: func(p string) int { return loads[p] }}
	for i := 0; i < 3; i++ {
		if got := n.pick(s); got != "out.b" {
			t.Fatalf("pick = %q, want out.b (lower load)", got)
		}
	}

	// Ties rotate so equals share fairly.
	even := selection{available: allUp, load: noLoad}
	seen := map[string]int{}
	for i := 0; i < 4; i++ {
		seen[n.pick(even)]++
	}
	if seen["out.a"] == 0 || seen["out.b"] == 0 {
		t.Fatalf("tie rotation starved a child: %v", seen)
	}
}

func TestNestedTreeFailsOverWhenSubtreeDrains(t *testing.T) {
	n := mustParse(t, map[string]any{
		"failover": []any{
			map[string]any{"least_loaded": []any{"out.a1", "out.a2"}},
			"out.peer",
		},
	})

	// Healthy pool: traffic stays local.
	s := selection{available: allUp, load: noLoad}
	if got := n.pick(s); got != "out.a1" && got != "out.a2" {
		t.Fatalf("pick = %q, want a local pool member", got)
	}

	// Whole local subtree down: the next failover child takes over.
	peerOnly := selection{available: func(p string) bool { return p == "out.peer" }, load: noLoad}
	if got := n.pick(peerOnly); got != "out.peer" {
		t.Fatalf("pick = %q, want out.peer after pool drained", got)
	}

	// Subtree load is compared as a sum across its leaves.
	if got := subtreeLoad(mustParse(t, map[string]any{"least_loaded": []any{"out.a1", "out.a2"}}), selection{
		available: allUp,
		load:      func(p string) int { return 3 },
	}); got != 6 {
		t.Fatalf("subtreeLoad = %d, want 6", got)
	}
}
