// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"testing"
)

func TestPrintLogRecord(t *testing.T) {
	// 1. Map data
	data := map[string]any{
		"time":  "2026-04-27T10:00:00Z",
		"level": "INFO",
		"msg":   "test message",
		"foo":   "bar",
	}
	printLogRecord(data) // Visual verification or just coverage

	// 2. Non-map data
	printLogRecord("string data")

	// 3. Different levels
	levels := []string{"DEBUG", "WARN", "ERROR", "OTHER"}
	for _, l := range levels {
		data["level"] = l
		printLogRecord(data)
	}
}
