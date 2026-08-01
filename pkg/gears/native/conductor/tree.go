// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package conductor

import (
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
)

// A destination tree selects one output port per request. Interior nodes are
// strategies (failover, round_robin, least_loaded) that compose freely; leaves
// are output ports. A leaf is just a port: whether it reaches a local uplink
// leg or a peer conductor on another rack is decided by the wiring, not here.
//
// Scenario form:
//
//	destination:
//	  failover:
//	    - least_loaded: ["out_scheme_a", "out_scheme_a2"]
//	    - "out_west"
//
// Selection consults two callbacks: availability (may this port receive
// traffic right now?) and load (open tickets routed to this port), so the
// tree stays a pure decision structure with no sensing of its own.

// selection provides the runtime signals a pick consults.
type selection struct {
	available func(port string) bool
	load      func(port string) int
}

// destNode is one node of a destination tree.
type destNode interface {
	// pick returns the selected leaf port, or "" when nothing beneath this
	// node is available.
	pick(s selection) string
	// leaves appends every leaf port beneath this node.
	leaves(out []string) []string
}

// leafNode is a single output port.
type leafNode string

func (l leafNode) pick(s selection) string {
	if s.available(string(l)) {
		return string(l)
	}
	return ""
}

func (l leafNode) leaves(out []string) []string { return append(out, string(l)) }

type strategyKind uint8

const (
	strategyFailover strategyKind = iota
	strategyRoundRobin
	strategyLeastLoaded
)

func (k strategyKind) String() string {
	switch k {
	case strategyFailover:
		return "failover"
	case strategyRoundRobin:
		return "round_robin"
	case strategyLeastLoaded:
		return "least_loaded"
	default:
		return "unknown"
	}
}

// strategyNode composes children under one selection strategy.
type strategyNode struct {
	kind     strategyKind
	children []destNode
	// cached is every leaf port beneath this node, computed once at parse
	// time. The tree is immutable after Init, so availableUnder/subtreeLoad
	// read this instead of rebuilding the list on every request.
	cached []string
	rr     atomic.Uint64 // rotation for round_robin and least_loaded ties
}

func (n *strategyNode) leaves(out []string) []string {
	if n.cached != nil {
		return append(out, n.cached...)
	}
	for _, c := range n.children {
		out = c.leaves(out)
	}
	return out
}

// containsLeastLoaded reports whether any node in the tree balances by load,
// so the request path can skip the in-flight snapshot when it is not needed.
func containsLeastLoaded(n destNode) bool {
	sn, ok := n.(*strategyNode)
	if !ok {
		return false
	}
	if sn.kind == strategyLeastLoaded {
		return true
	}
	for _, c := range sn.children {
		if containsLeastLoaded(c) {
			return true
		}
	}
	return false
}

// subtreeLoad is the open-ticket count across every leaf beneath a node,
// which is what balancing across subtrees must compare. It walks the precomputed
// leaf set without allocating, so least-loaded selection stays allocation-free
// on the request path.
func subtreeLoad(n destNode, s selection) int {
	total := 0
	switch t := n.(type) {
	case leafNode:
		total += s.load(string(t))
	case *strategyNode:
		for _, port := range t.cached {
			total += s.load(port)
		}
	}
	return total
}

func (n *strategyNode) pick(s selection) string {
	switch n.kind {
	case strategyFailover:
		// Strict order: the first child with an available leaf wins.
		for _, c := range n.children {
			if port := c.pick(s); port != "" {
				return port
			}
		}
		return ""

	case strategyRoundRobin:
		// Rotate across children, skipping those with nothing available.
		count := uint64(len(n.children))
		start := n.rr.Add(1) - 1
		for i := uint64(0); i < count; i++ {
			c := n.children[(start+i)%count]
			if port := c.pick(s); port != "" {
				return port
			}
		}
		return ""

	case strategyLeastLoaded:
		// Choose the available child with the fewest open tickets beneath it;
		// break ties by rotation so healthy equals share fairly.
		type cand struct {
			idx  int
			load int
		}
		var cands []cand
		for i, c := range n.children {
			if !availableUnder(c, s) {
				continue
			}
			cands = append(cands, cand{idx: i, load: subtreeLoad(c, s)})
		}
		if len(cands) == 0 {
			return ""
		}
		minLoad := cands[0].load
		for _, c := range cands[1:] {
			if c.load < minLoad {
				minLoad = c.load
			}
		}
		var tied []int
		for _, c := range cands {
			if c.load == minLoad {
				tied = append(tied, c.idx)
			}
		}
		pickIdx := tied[int((n.rr.Add(1)-1)%uint64(len(tied)))]
		if port := n.children[pickIdx].pick(s); port != "" {
			return port
		}
		// The chosen child raced to unavailable; fall back to any candidate.
		for _, c := range cands {
			if port := n.children[c.idx].pick(s); port != "" {
				return port
			}
		}
		return ""

	default:
		return ""
	}
}

// availableUnder reports whether any leaf beneath the node may receive traffic.
// It short-circuits over the precomputed leaf set without allocating.
func availableUnder(n destNode, s selection) bool {
	switch t := n.(type) {
	case leafNode:
		return s.available(string(t))
	case *strategyNode:
		for _, port := range t.cached {
			if s.available(port) {
				return true
			}
		}
	}
	return false
}

// parseDest builds a destination tree from its scenario representation:
// a string leaf, or a single-key map {strategy: [children...]}.
func parseDest(v any) (destNode, error) {
	switch val := v.(type) {
	case string:
		if !strings.HasPrefix(val, "out") {
			return nil, fmt.Errorf("destination leaf %q must be an output port (out_<name>)", val)
		}
		return leafNode(val), nil

	case map[string]any:
		if len(val) != 1 {
			return nil, fmt.Errorf("destination node must have exactly one strategy key, got %d", len(val))
		}
		// Deterministic iteration (single key, but keep it tidy).
		keys := make([]string, 0, 1)
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		name := keys[0]

		var kind strategyKind
		switch name {
		case "failover":
			kind = strategyFailover
		case "round_robin":
			kind = strategyRoundRobin
		case "least_loaded":
			kind = strategyLeastLoaded
		default:
			return nil, fmt.Errorf("unknown destination strategy %q (want failover, round_robin or least_loaded)", name)
		}

		rawChildren, ok := val[name].([]any)
		if !ok || len(rawChildren) == 0 {
			return nil, fmt.Errorf("strategy %q needs a non-empty list of children", name)
		}
		node := &strategyNode{kind: kind}
		for i, rc := range rawChildren {
			child, err := parseDest(rc)
			if err != nil {
				return nil, fmt.Errorf("%s child %d: %w", name, i, err)
			}
			node.children = append(node.children, child)
		}
		// Cache the leaf set now; the tree is immutable after parse.
		node.cached = node.leaves(nil)
		return node, nil

	default:
		return nil, fmt.Errorf("destination must be a port string or a strategy map, got %T", v)
	}
}
