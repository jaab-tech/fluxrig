// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package conductor

import (
	"fmt"
	"strings"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

// route is one entry of the routing table: a predicate over structured fields
// and the destination tree that serves matching requests. Routes are evaluated
// in order; the first match wins. A route without a match clause matches
// everything and serves as the catch-all.
type route struct {
	name        string
	matchField  string
	matchPrefix string
	tree        destNode
	// leaves and needsLoad are precomputed at parse time so the request path
	// can snapshot in-flight counts in a single engine-lock acquisition (only
	// when the tree actually balances by load), instead of one lock per leaf.
	leaves    []string
	needsLoad bool
}

// matches applies the route predicate to a structured message. The match field
// is resolved with dotted-path semantics (a codec alias like "card.pan" or a
// field path like "iso8583.field.2").
func (r *route) matches(msg *fluxmsg.FluxMsg) bool {
	if r.matchField == "" {
		return true // catch-all
	}
	if msg == nil || msg.Data == nil {
		return false
	}
	v, ok := msg.Get(r.matchField)
	if !ok {
		return false
	}
	return strings.HasPrefix(fieldString(v), r.matchPrefix)
}

// fieldString renders a structured field value for matching and key building.
func fieldString(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case []byte:
		return string(val)
	default:
		return fmt.Sprint(val)
	}
}

// parseRoutes builds the routing table from the scenario config.
func parseRoutes(v any) ([]*route, error) {
	raw, ok := v.([]any)
	if !ok || len(raw) == 0 {
		return nil, fmt.Errorf("routes must be a non-empty list")
	}

	routes := make([]*route, 0, len(raw))
	for i, rv := range raw {
		rm, ok := rv.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("route %d: must be a map", i)
		}

		r := &route{}
		if n, hasName := rm["name"].(string); hasName {
			r.name = n
		} else {
			r.name = fmt.Sprintf("route-%d", i)
		}

		if mv, hasMatch := rm["match"]; hasMatch {
			mm, isMap := mv.(map[string]any)
			if !isMap {
				return nil, fmt.Errorf("route %q: match must be a map", r.name)
			}
			if f, hasField := mm["field"].(string); hasField {
				r.matchField = f
			}
			if p, hasPrefix := mm["prefix"].(string); hasPrefix {
				r.matchPrefix = p
			}
			if r.matchField == "" {
				return nil, fmt.Errorf("route %q: match needs a field", r.name)
			}
		}

		dv, ok := rm["destination"]
		if !ok {
			return nil, fmt.Errorf("route %q: destination is required", r.name)
		}
		tree, err := parseDest(dv)
		if err != nil {
			return nil, fmt.Errorf("route %q: %w", r.name, err)
		}
		r.tree = tree
		r.needsLoad = containsLeastLoaded(tree)
		if r.needsLoad {
			r.leaves = tree.leaves(nil)
		}
		routes = append(routes, r)
	}
	return routes, nil
}
