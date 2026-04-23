// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/fxamacker/cbor/v2"
	"github.com/jaab-tech/fluxrig/pkg/telemetry/wal"
	"github.com/spf13/cobra"
)

var inspectLogsCmd = &cobra.Command{
	Use:   "inspect-logs [file]",
	Short: "Inspect binary WAL log files",
	Long:  `Decodes and prints binary CBOR log files (rack.wal) to stdout as JSON.`,
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		path := args[0]
		if err := inspectLogs(path, cmd.OutOrStdout(), cmd.ErrOrStderr()); err != nil {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Error: %v\n", err)
			os.Exit(1)
		}
	},
}

func init() {
	rootCmd.AddCommand(inspectLogsCmd)
}

func inspectLogs(path string, out io.Writer, errOut io.Writer) error {
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

	_, _ = fmt.Fprintf(errOut, "Inspecting WAL %s (Indexes %d to %d)\n", path, first, last)

	count := 0
	for i := first; i <= last; i++ {
		payload, err := w.Read(i)
		if err != nil {
			if err == wal.ErrNotFound {
				continue
			}
			_, _ = fmt.Fprintf(errOut, "Error reading index %d: %v\n", i, err)
			continue
		}

		// Decode Payload (FluxMsg Envelope)
		var rawMap map[string]interface{}
		if err2 := cbor.Unmarshal(payload, &rawMap); err2 != nil {
			_, _ = fmt.Fprintf(errOut, "Error decoding CBOR at index %d: %v\n", i, err2)
			continue
		}

		// Robust stringification for JSON marshaling (CBOR nested maps have interface{} keys)
		cleanMap := stringifyKeys(rawMap)
		jsonBytes, err := json.Marshal(cleanMap)
		if err != nil {
			_, _ = fmt.Fprintf(out, "[%d] %v\n", i, cleanMap)
		} else {
			_, _ = fmt.Fprintf(out, "[%d] %s\n", i, string(jsonBytes))
		}
		count++
	}

	_, _ = fmt.Fprintf(errOut, "Done. %d records processed.\n", count)
	return nil
}

// stringifyKeys recursively converts map[interface{}]interface{} to map[string]interface{}
// which is required for encoding/json to successfully marshal maps derived from CBOR.
func stringifyKeys(v interface{}) interface{} {
	switch v := v.(type) {
	case map[interface{}]interface{}:
		res := make(map[string]interface{})
		for k, val := range v {
			res[fmt.Sprintf("%v", k)] = stringifyKeys(val)
		}
		return res
	case map[string]interface{}:
		res := make(map[string]interface{})
		for k, val := range v {
			res[k] = stringifyKeys(val)
		}
		return res
	case []interface{}:
		res := make([]interface{}, len(v))
		for i, val := range v {
			res[i] = stringifyKeys(val)
		}
		return res
	default:
		return v
	}
}
