// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package mixer

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/jaab-tech/fluxrig/pkg/config"
	"github.com/jaab-tech/fluxrig/pkg/pki"
	"github.com/jaab-tech/fluxrig/pkg/snake"
)

// Repair exit codes. 0 means healthy or repaired; 1 means a stream needs
// --force and nothing was changed; 2 means the repair itself failed.
const (
	RepairOK = iota
	RepairNeedsForce
	RepairFailed
)

// messageSubjects is the subject set the message stream captures. It was
// written three times by hand; the repair path makes a fourth, so it lives
// here once, and the callers use it.
func messageSubjects(cfg *config.SnakeConfig) []string {
	if len(cfg.StreamSubjects) > 0 {
		return cfg.StreamSubjects
	}
	subjects := []string{
		"flux.msg.>",
		"flux.agent.>",
		"flux.rack.>",
		"flux.ctrl.>",
	}
	// If domain is not 'flux', add domain-prefixed subjects too for backward compatibility/isolation
	if cfg.Domain != "flux" && cfg.Domain != "" {
		subjects = append(subjects,
			fmt.Sprintf("%s.msg.>", cfg.Domain),
			fmt.Sprintf("%s.agent.>", cfg.Domain),
			fmt.Sprintf("%s.rack.>", cfg.Domain),
			fmt.Sprintf("%s.ctrl.>", cfg.Domain),
		)
	}
	return subjects
}

// expectedStreams returns the streams the Mixer manages, each with the
// subjects and retention it would provision. Repair diagnoses exactly these.
func expectedStreams(cfg *config.MixerConfig) []snake.ExpectedStream {
	var expected []snake.ExpectedStream

	if cfg.Snake.StreamName != "" {
		expected = append(expected, snake.ExpectedStream{
			Name:     cfg.Snake.StreamName,
			Subjects: messageSubjects(&cfg.Snake),
		})
	}

	if cfg.Observability.Enabled && cfg.Telemetry.StreamName != "" {
		expected = append(expected, snake.ExpectedStream{
			Name:     cfg.Telemetry.StreamName,
			Subjects: []string{cfg.Telemetry.BaseSubject + ".>"},
		})
	}

	if cfg.Snake.DLQEnabled {
		prefix := cfg.Snake.DLQSubject
		if prefix == "" {
			prefix = "flux.dlq"
		}
		var maxAge time.Duration
		if cfg.Snake.DLQMaxAge != "" {
			if parsed, err := time.ParseDuration(cfg.Snake.DLQMaxAge); err == nil {
				maxAge = parsed
			}
		}
		expected = append(expected, snake.ExpectedStream{
			Name:     "flux-dlq",
			Subjects: []string{strings.TrimSuffix(prefix, ".") + ".>"},
			MaxAge:   maxAge,
		})
	}

	return expected
}

// Repair starts the embedded Snake without provisioning, diagnoses every
// managed stream, prints the report, and repairs it. Drift and absence are
// always fixed; an unreadable stream is recreated only when force is true,
// and the result reports what was discarded. It never mints a cluster key or
// an identity: a repair runs against the existing store, read-only on who the
// Mixer is.
func Repair(ctx context.Context, cfg *config.MixerConfig, log *slog.Logger, force bool) int {
	// Identity is loaded, never generated. A repair on a store whose key is
	// missing fails here with the same actionable error a normal boot gives,
	// instead of silently re-rooting authority mid-repair.
	keyPath := filepath.Join(cfg.Store.Dir, cfg.Store.ClusterKeyFile)
	clusterKey, err := pki.LoadClusterKey(keyPath)
	if err != nil {
		log.Error("repair cannot load the cluster key", "path", keyPath, "error", err)
		return RepairFailed
	}

	storeKey, storeOldKey, _, err := resolveStoreKeys(&cfg.Snake, clusterKey)
	if err != nil {
		log.Error("repair cannot resolve the store key", "error", err)
		return RepairFailed
	}

	snakeStoreDir := cfg.Snake.StoreDir
	if snakeStoreDir == "" {
		snakeStoreDir = filepath.Join(cfg.Store.Dir, "snake")
	}

	snakeCfg := snake.Config{
		Port:        cfg.Snake.Port,
		ClusterName: cfg.Snake.ClusterName,
		StoreDir:    snakeStoreDir,
		StreamName:  "", // No provisioning in repair mode: diagnosis comes first.
		TLSCert:     cfg.Snake.TLSCertFile,
		TLSKey:      cfg.Snake.TLSKeyFile,
		TLSCA:       cfg.Snake.TLSCAFile,
		TLSVerify:   cfg.Snake.TLSVerify,
		AllowNonTLS: cfg.Snake.AllowNonTLS,
		LogLevel:    cfg.Logging.Level,
		StoreKey:    storeKey,
		StoreOldKey: storeOldKey,
		StoreCipher: cfg.Snake.StoreCipher,
	}
	srv, err := snake.NewServer(ctx, snakeCfg)
	if err != nil {
		log.Error("repair cannot start the embedded server on the store", "dir", snakeStoreDir, "error", err)
		return RepairFailed
	}
	defer srv.Shutdown()

	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		log.Error("repair cannot connect to the embedded server", "error", err)
		return RepairFailed
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		log.Error("repair cannot open JetStream", "error", err)
		return RepairFailed
	}

	expected := expectedStreams(cfg)
	report, err := snake.Diagnose(ctx, js, expected)
	if err != nil {
		log.Error("repair diagnosis failed", "error", err)
		return RepairFailed
	}

	for _, d := range report.Streams {
		switch d.State {
		case snake.StreamHealthy:
			log.Info("stream healthy", "stream", d.Name, "detail", d.Detail)
		case snake.StreamDrift:
			log.Warn("stream drifted", "stream", d.Name, "detail", d.Detail)
		case snake.StreamAbsent:
			log.Warn("stream absent", "stream", d.Name, "detail", d.Detail)
		case snake.StreamUnreadable:
			log.Error("stream unreadable", "stream", d.Name, "detail", d.Detail)
		}
	}

	if report.NeedsForce() && !force {
		log.Error("repair needs --force: recreating an unreadable stream discards its messages, and nothing was changed")
		return RepairNeedsForce
	}

	result, err := snake.Repair(ctx, js, report, expected, force)
	if err != nil {
		log.Error("repair failed", "error", err)
		return RepairFailed
	}
	for _, n := range result.Updated {
		log.Info("stream config updated", "stream", n)
	}
	for _, n := range result.Created {
		log.Info("stream created", "stream", n)
	}
	for _, n := range result.Recreated {
		log.Info("stream recreated", "stream", n, "discarded_messages", result.Discarded[n])
	}
	return RepairOK
}
