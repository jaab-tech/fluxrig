// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package io_tcp

import (
	"bufio"
	"bytes"
)

// MakeSplitter returns a bufio.SplitFunc based on configuration.
func MakeSplitter(cfg *Config) bufio.SplitFunc {
	delim := []byte(cfg.Delimiter)
	if len(delim) == 0 {
		return bufio.ScanLines // Default fallback
	}

	return func(data []byte, atEOF bool) (advance int, token []byte, err error) {
		if atEOF && len(data) == 0 {
			return 0, nil, nil
		}

		if cfg.DelimiterPosition == "prefix" {
			// PREFIX MODE: Delimiter is at the START of the message.
			// Format: <DELIM>CONTENT<DELIM>CONTENT...

			// 1. If we are at start and don't see delim, skip until we find one?
			// Or assume stream starts with it. Let's strict search.
			startIdx := bytes.Index(data, delim)
			if startIdx == -1 {
				// No delimiter found yet.
				if atEOF {
					// Return what's left as junk or valid tail?
					// Usually prefix mode implies nothing is valid without prefix. Discard.
					return len(data), nil, nil
				}
				return 0, nil, nil
			}

			// If startIdx > 0, we have junk before the first message. Discard it.
			if startIdx > 0 {
				return startIdx, nil, nil
			}

			// Now we are at a delimiter at index 0.
			// Find the NEXT delimiter.
			nextIdx := bytes.Index(data[len(delim):], delim)
			if nextIdx == -1 {
				// No second delimiter yet.
				if atEOF {
					// We have a start delimiter, and EOF. The rest is the last message.
					// Advance = everything.
					return len(data), returnToken(data, cfg.DelimiterInclude, len(delim), true), nil
				}
				// Wait for more data
				return 0, nil, nil
			}

			// We found the next delimiter at (len(delim) + nextIdx)
			// Token is data[0 : len(delim)+nextIdx]
			totalLen := len(delim) + nextIdx

			// We consume this token. Current pos moves by totalLen.
			return totalLen, returnToken(data[:totalLen], cfg.DelimiterInclude, len(delim), true), nil

		} else {
			// SUFFIX MODE (Default): CONTENT<DELIM>
			idx := bytes.Index(data, delim)
			if idx >= 0 {
				// Found it.
				// Token is data[0:idx].
				// Total consumed is idx + len(delim).
				totalLen := idx + len(delim)
				return totalLen, returnToken(data[:totalLen], cfg.DelimiterInclude, len(delim), false), nil
			}

			if atEOF {
				return len(data), data, nil
			}
			return 0, nil, nil
		}
	}
}

// returnToken constructs the final payload based on Include/Exclude logic
func returnToken(chunk []byte, include bool, delimLen int, isPrefix bool) []byte {
	if include {
		return chunk // Chunk includes the delimiter(s) as found in logic above
	}

	// Exclude logic
	if isPrefix {
		// Chunk is <DELIM>CONTENT. Return CONTENT.
		if len(chunk) >= delimLen {
			return chunk[delimLen:]
		}
	} else {
		// Chunk is CONTENT<DELIM>. Return CONTENT.
		if len(chunk) >= delimLen {
			return chunk[:len(chunk)-delimLen]
		}
	}
	return chunk
}
