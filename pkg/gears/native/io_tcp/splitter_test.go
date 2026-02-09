// Copyright 2025 JAAB Tech SAS, Uruguay
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package io_tcp

import (
	"bufio"
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMakeSplitter(t *testing.T) {
	tests := []struct {
		name     string
		cfg      Config
		input    string
		expected []string
	}{
		{
			name:     "Suffix - Newline (Default)",
			cfg:      Config{Delimiter: "\n", DelimiterPosition: "suffix"},
			input:    "foo\nbar\n",
			expected: []string{"foo", "bar"},
		},
		{
			name:     "Suffix - Keep Delimiter",
			cfg:      Config{Delimiter: "\n", DelimiterPosition: "suffix", DelimiterInclude: true},
			input:    "foo\nbar\n",
			expected: []string{"foo\n", "bar\n"},
		},
		{
			name:  "Suffix - Partial Last",
			cfg:   Config{Delimiter: "\n", DelimiterPosition: "suffix"},
			input: "foo\nbar", // 'bar' has no delim, should be waiting? Or EOF?
			// bufio.Scanner at EOF with non-empty buffer calls Final Token logic.
			// Our logic returns remaining data.
			expected: []string{"foo", "bar"},
		},
		{
			name:     "Prefix - Log Tag",
			cfg:      Config{Delimiter: "<log", DelimiterPosition: "prefix"},
			input:    "<log A <log B <log C",
			expected: []string{" A ", " B ", " C"}, // Preserves spaces
		},
		{
			name:     "Prefix - Include Delimiter",
			cfg:      Config{Delimiter: "<log", DelimiterPosition: "prefix", DelimiterInclude: true},
			input:    "<log A <log B <log C",
			expected: []string{"<log A ", "<log B ", "<log C"},
		},
		{
			name:     "Prefix - Junk at Start",
			cfg:      Config{Delimiter: "<log", DelimiterPosition: "prefix"},
			input:    "junk data before <log VALID",
			expected: []string{" VALID"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			split := MakeSplitter(&tt.cfg)
			scanner := bufio.NewScanner(bytes.NewBufferString(tt.input))
			scanner.Split(split)

			var tokens []string
			for scanner.Scan() {
				tokens = append(tokens, scanner.Text())
			}
			assert.Equal(t, tt.expected, tokens)
		})
	}
}
