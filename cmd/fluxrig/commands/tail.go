// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

var tailCmd = &cobra.Command{
	Use:   "tail [node-name]",
	Short: "Tail live logs from a specific node in the mesh",
	Long:  `Subscribes to the telemetry plane and streams logs from the target node in real-time.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		nodeName := args[0]
		subject := fmt.Sprintf("flux.telemetry.%s.logs", nodeName)

		url := os.Getenv("FLUXRIG_BUS_URL")
		if url == "" {
			url = "nats://localhost:4222"
		}

		b := bus.NewNatsBus("flux-tail")
		if err := b.Connect(url, bus.ConnectOptions{
			Name: "flux-tail",
		}); err != nil {
			return fmt.Errorf("failed to connect to bus: %w", err)
		}
		defer b.Close()

		fmt.Printf(color.CyanString("Tailing logs for %s on %s...\n"), nodeName, subject)

		// Create a context that is canceled on SIGINT
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		sub, err := b.Subscribe(subject, func(ctx context.Context, msg *fluxmsg.FluxMsg) {
			// Extract log record from FluxMsg
			// Telemetry logs are usually map[string]interface{} in FluxMsg.Data
			data := msg.Data
			if data == nil {
				return
			}

			// Format and print
			printLogRecord(data)
		})

		if err != nil {
			return fmt.Errorf("failed to subscribe: %w", err)
		}
		defer func() { _ = sub.Unsubscribe() }()

		<-ctx.Done()
		fmt.Println("\nStopping log tail.")
		return nil
	},
}

func printLogRecord(data any) {
	// Robust printing
	m, ok := data.(map[string]any)
	if !ok {
		// Try to marshal to JSON if not a map
		b, _ := json.Marshal(data)
		fmt.Println(string(b))
		return
	}

	// Extract standard fields
	ts := m["time"].(string)
	level := m["level"].(string)
	msg := m["msg"].(string)

	// Colorize level
	levelStr := level
	switch level {
	case "DEBUG":
		levelStr = color.MagentaString(level)
	case "INFO":
		levelStr = color.BlueString(level)
	case "WARN":
		levelStr = color.YellowString(level)
	case "ERROR":
		levelStr = color.RedString(level)
	}

	fmt.Printf("[%s] %s: %s", color.HiBlackString(ts), levelStr, msg)

	// Print remaining attributes
	for k, v := range m {
		if k == "time" || k == "level" || k == "msg" {
			continue
		}
		fmt.Printf(" %s=%v", color.CyanString(k), v)
	}
	fmt.Println()
}

func init() {
	rootCmd.AddCommand(tailCmd)
}
