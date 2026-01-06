package simple_tcp

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
