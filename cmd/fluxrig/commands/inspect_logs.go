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

package commands

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/jaab-tech/fluxrig/pkg/telemetry/wal"
	"github.com/spf13/cobra"
	"github.com/vmihailenco/msgpack/v5"
)

var inspectLogsCmd = &cobra.Command{
	Use:   "inspect-logs [file]",
	Short: "Inspect binary WAL log files",
	Long:  `Decodes and prints binary MsgPack log files (rack.wal) to stdout as JSON.`,
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		path := args[0]
		if err := inspectLogs(path); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	},
}

func init() {
	rootCmd.AddCommand(inspectLogsCmd)
}

func inspectLogs(path string) error {
	// tidwall/wal requires a directory
	// If path is a file, warn user or try to find parent?
	// For now assume path is the WAL directory.

	w, err := wal.Open(path, nil)
	if err != nil {
		return fmt.Errorf("failed to open WAL at %s: %w", path, err)
	}
	defer func() { _ = w.Close() }()

	first, err := w.FirstIndex()
	if err != nil {
		return fmt.Errorf("failed to get first index: %w", err)
	}
	last, err := w.LastIndex()
	if err != nil {
		return fmt.Errorf("failed to get last index: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Inspecting WAL %s (Indexes %d to %d)\n", path, first, last)

	count := 0
	for i := first; i <= last; i++ {
		payload, err := w.Read(i)
		if err != nil {
			if err == wal.ErrNotFound {
				continue
			}
			fmt.Fprintf(os.Stderr, "Error reading index %d: %v\n", i, err)
			continue
		}

		// Decode Payload (FluxMsg Envelope)
		// We want to verify the ENVELOPE first.
		// Then extracting the Data map.
		// Note: Use map[string]interface{} to decode everything
		var rawMap map[string]interface{}
		if err := msgpack.Unmarshal(payload, &rawMap); err != nil {
			fmt.Fprintf(os.Stderr, "Error decoding MsgPack at index %d: %v\n", i, err)
			continue
		}

		// Dump raw JSON
		jsonBytes, _ := json.Marshal(rawMap)
		fmt.Printf("[%d] %s\n", i, string(jsonBytes))
		count++
	}

	fmt.Fprintf(os.Stderr, "Done. %d records processed.\n", count)
	return nil
}
