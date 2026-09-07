// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package protodoc

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/gears/native/iso8583/codec/sdl"
)

// A reference outlives the deployment it described, and a file in a repository
// carries no deployment at all. Without the version a reader cannot tell which
// contract they are holding -- the HTML said so from the start and the markdown
// did not, which a suite comparing the two variants found.
func TestMarkdownNamesTheContractItDocuments(t *testing.T) {
	raw, err := os.ReadFile(refSpec)
	require.NoError(t, err)
	spec, err := sdl.ParseSemantic(raw)
	require.NoError(t, err)
	require.NotEmpty(t, spec.Version)

	for _, scope := range []Scope{ScopePublic, ScopeComplete} {
		out := Markdown(spec, Options{Scope: scope, Source: raw})
		require.Contains(t, out, "Version "+spec.Version, "scope %s", scope)
		// Near the top, where a reader looking for it will be.
		require.Less(t, strings.Index(out, "Version "+spec.Version), 600, "scope %s", scope)
	}
}

// tableProblems returns a description of every malformed table in a markdown
// document. A table whose separator does not match its header is not a table:
// most renderers give up and show the pipes.
func tableProblems(md string) []string {
	var problems []string
	sep := regexp.MustCompile(`^\|(\s*:?-{3,}:?\s*\|)+$`)
	// A pipe escaped for markdown is content, not a delimiter.
	cells := func(line string) int {
		return strings.Count(strings.ReplaceAll(line, `\|`, "\x00"), "|")
	}

	lines := strings.Split(md, "\n")
	for i := 0; i < len(lines); i++ {
		if !strings.HasPrefix(lines[i], "|") {
			continue
		}
		start, block := i+1, []string(nil)
		for i < len(lines) && strings.HasPrefix(lines[i], "|") {
			block = append(block, lines[i])
			i++
		}
		if len(block) < 2 {
			problems = append(problems, fmt.Sprintf("line %d: a table with no separator row", start))
			continue
		}
		if !sep.MatchString(strings.TrimSpace(block[1])) {
			problems = append(problems, fmt.Sprintf("line %d: separator %q is not one", start+1, block[1]))
			continue
		}
		want := cells(block[0])
		for n, row := range block {
			if got := cells(row); got != want {
				problems = append(problems, fmt.Sprintf(
					"line %d: %d columns where the header has %d: %q", start+n, got-1, want-1, row))
			}
		}
	}
	return problems
}

// Every table the generator emits has to be one. This was found by reading the
// output: the per-message tables closed the header with a pipe and the separator
// without one, so the separator carried a cell fewer than the header and not a
// single message table rendered.
func TestEveryTableIsWellFormed(t *testing.T) {
	raw, err := os.ReadFile(refSpec)
	require.NoError(t, err)
	spec, err := sdl.ParseSemantic(raw)
	require.NoError(t, err)

	for _, scope := range []Scope{ScopePublic, ScopeComplete} {
		md := Markdown(spec, Options{Scope: scope, Source: raw})
		// Enough tables to be worth checking, or the check proves nothing.
		require.Greater(t, strings.Count(md, "\n| "), 100, "scope %s renders almost no tables", scope)
		if problems := tableProblems(md); len(problems) > 0 {
			t.Errorf("scope %s: %d malformed tables:\n  %s",
				scope, len(problems), strings.Join(problems[:min(len(problems), 8)], "\n  "))
		}
	}
}
