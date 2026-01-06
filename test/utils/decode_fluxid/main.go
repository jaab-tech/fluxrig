package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run decode_fluxid.go <hex_id>")
		os.Exit(1)
	}

	hexID := os.Args[1]
	// Remove 0x prefix if present
	hexID = strings.TrimPrefix(hexID, "0x")

	// Parse Hex
	idVal, err := strconv.ParseUint(hexID, 16, 64)
	if err != nil {
		fmt.Printf("Error parsing hex ID: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Hex ID: %s\n", hexID)
	fmt.Printf("Int ID: %d\n", idVal)

	// Decompose Sonyflake (Standard Settings)
	// Sonyflake default:
	// 39 bits time | 8 bits seq | 16 bits machine

	// Default Epoch: 2014-09-01 (Sonyflake default)
	// BUT FluxRig uses 2025-01-01 (as seen in pkg/idgen/idgen.go)

	// Reconstruct decomposition manually to apply custom Epoch
	// ID structure:
	// | 63 ... 24 | 23 ... 16 | 15 ... 0 |
	// | Timestamp | Sequence  | Machine  |
	// But sonyflake library documentation says:
	// "Basic Sonyflake ID structure:
	//  39 bits for time in units of 10 msec
	//   8 bits for a sequence number
	//  16 bits for a machine id"
	// Total 63 bits (MSB is sign/unused in signed int64, usually 0)
	machineID := uint16(idVal & 0xFFFF)     // #nosec G115
	sequence := uint8((idVal >> 16) & 0xFF) // #nosec G115
	timeUnit := (idVal >> 24) & 0x7FFFFFFFFF

	fmt.Printf("MachineID: %d (0x%x)\n", machineID, machineID)
	fmt.Printf("Sequence : %d\n", sequence)

	// FluxRig Epoch
	epoch := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	duration := time.Duration(timeUnit) * 10 * time.Millisecond // #nosec G115
	ts := epoch.Add(duration)

	fmt.Printf("Timestamp: %s\n", ts.Format(time.RFC3339Nano))
}
