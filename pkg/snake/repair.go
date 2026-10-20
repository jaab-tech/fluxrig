// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package snake

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// StreamState classifies one managed stream. Only drift and absent are
// repaired without the operator's explicit signature; unreadable needs --force.
type StreamState int

const (
	// StreamHealthy needs nothing.
	StreamHealthy StreamState = iota
	// StreamDrift exists but its config differs from what the Mixer expects.
	// Updating the config loses no messages.
	StreamDrift
	// StreamAbsent does not exist; creating it is safe.
	StreamAbsent
	// StreamUnreadable exists but its messages cannot be read. Recreating it
	// loses whatever is in it, so it is only repaired with force.
	StreamUnreadable
)

func (s StreamState) String() string {
	switch s {
	case StreamHealthy:
		return "healthy"
	case StreamDrift:
		return "drift"
	case StreamAbsent:
		return "absent"
	case StreamUnreadable:
		return "unreadable"
	default:
		return "unknown"
	}
}

// ExpectedStream is what the Mixer would provision for one stream.
type ExpectedStream struct {
	Name     string
	Subjects []string
	MaxAge   time.Duration
}

// StreamDiagnosis is the per-stream verdict.
type StreamDiagnosis struct {
	Name      string
	State     StreamState
	Detail    string
	Msgs      uint64
	FirstSeq  uint64
	LastSeq   uint64
	Oldest    time.Time
	DriftFrom []string // current subjects, when the subject set drifted
}

// Report is the full diagnosis over every managed stream.
type Report struct {
	Streams []StreamDiagnosis
}

// NeedsForce reports whether any stream needs --force to repair.
func (r Report) NeedsForce() bool {
	for _, s := range r.Streams {
		if s.State == StreamUnreadable {
			return true
		}
	}
	return false
}

// Diagnose classifies each expected stream. It reads but never writes: the
// operator sees the verdict before anything is changed.
func Diagnose(ctx context.Context, js jetstream.JetStream, expected []ExpectedStream) (Report, error) {
	var report Report
	for _, want := range expected {
		d := StreamDiagnosis{Name: want.Name}
		st, err := js.Stream(ctx, want.Name)
		if err != nil {
			d.State = StreamAbsent
			d.Detail = fmt.Sprintf("stream %q does not exist; creating it is safe", want.Name)
			report.Streams = append(report.Streams, d)
			continue
		}

		info, err := st.Info(ctx)
		if err != nil {
			d.State = StreamUnreadable
			d.Detail = fmt.Sprintf("stream %q exists but its info cannot be read: %v", want.Name, err)
			report.Streams = append(report.Streams, d)
			continue
		}
		d.Msgs = info.State.Msgs
		d.FirstSeq = info.State.FirstSeq
		d.LastSeq = info.State.LastSeq
		if !info.State.FirstTime.IsZero() {
			d.Oldest = info.State.FirstTime
		}

		// Config drift is the safe fix: updating the config loses nothing.
		if !sameSubjects(info.Config.Subjects, want.Subjects) {
			d.State = StreamDrift
			d.Detail = fmt.Sprintf("stream %q subjects differ: have %v, want %v", want.Name, info.Config.Subjects, want.Subjects)
			d.DriftFrom = info.Config.Subjects
			report.Streams = append(report.Streams, d)
			continue
		}

		// Readability probe: the first and last messages must decode. On an
		// empty stream there is nothing to probe.
		if info.State.Msgs > 0 {
			if err := probeReadable(ctx, st, info.State.FirstSeq); err != nil {
				d.State = StreamUnreadable
				d.Detail = fmt.Sprintf("stream %q holds %d messages but the first (seq %d) cannot be read: %v. Recreating it discards %d messages, oldest %s", want.Name, info.State.Msgs, info.State.FirstSeq, err, info.State.Msgs, ageOf(d.Oldest))
				report.Streams = append(report.Streams, d)
				continue
			}
			if info.State.LastSeq != info.State.FirstSeq {
				if err := probeReadable(ctx, st, info.State.LastSeq); err != nil {
					d.State = StreamUnreadable
					d.Detail = fmt.Sprintf("stream %q holds %d messages but the last (seq %d) cannot be read: %v. Recreating it discards %d messages, oldest %s", want.Name, info.State.Msgs, info.State.LastSeq, err, info.State.Msgs, ageOf(d.Oldest))
					report.Streams = append(report.Streams, d)
					continue
				}
			}
		}

		d.State = StreamHealthy
		d.Detail = fmt.Sprintf("stream %q is healthy: %d messages", want.Name, info.State.Msgs)
		report.Streams = append(report.Streams, d)
	}
	return report, nil
}

// probeReadable reads one message by sequence. A corrupt block surfaces here
// as an error rather than as a Mixer that refuses to boot.
func probeReadable(ctx context.Context, st jetstream.Stream, seq uint64) error {
	_, err := st.GetMsg(ctx, seq)
	return err
}

// sameSubjects compares subject sets ignoring order.
func sameSubjects(have, want []string) bool {
	if len(have) != len(want) {
		return false
	}
	set := make(map[string]bool, len(have))
	for _, s := range have {
		set[s] = true
	}
	for _, s := range want {
		if !set[s] {
			return false
		}
	}
	return true
}

func ageOf(t time.Time) string {
	if t.IsZero() {
		return "unknown age"
	}
	return fmt.Sprintf("from %s", t.UTC().Format(time.RFC3339))
}

// RepairResult names what was changed.
type RepairResult struct {
	Updated   []string          // drift streams whose config was updated
	Created   []string          // absent streams that were created
	Recreated []string          // unreadable streams deleted and recreated (--force only)
	Discarded map[string]uint64 // stream name to message count lost in a recreate
}

// Repair applies the diagnosis. Drift and absent are always fixed; unreadable
// is recreated only when force is true, and the result reports exactly what
// was discarded. It returns an error when a stream needs force and force is
// false, naming the streams, so the operator's next step is obvious.
func Repair(ctx context.Context, js jetstream.JetStream, report Report, expected []ExpectedStream, force bool) (RepairResult, error) {
	var result RepairResult
	result.Discarded = make(map[string]uint64)

	want := make(map[string]ExpectedStream, len(expected))
	for _, e := range expected {
		want[e.Name] = e
	}

	// Without force, an unreadable stream blocks the repair rather than being
	// skipped silently: a partial repair that leaves the killer in place would
	// read as success.
	var blocked []string
	if !force {
		for _, d := range report.Streams {
			if d.State == StreamUnreadable {
				blocked = append(blocked, d.Name)
			}
		}
		if len(blocked) > 0 {
			return result, fmt.Errorf("streams %s are unreadable and need --force to recreate (this discards their messages); nothing was changed", strings.Join(blocked, ", "))
		}
	}

	for _, d := range report.Streams {
		w := want[d.Name]
		switch d.State {
		case StreamHealthy:
			continue
		case StreamDrift:
			st, err := js.Stream(ctx, d.Name)
			if err != nil {
				return result, fmt.Errorf("repair drift on %q: %w", d.Name, err)
			}
			info, err := st.Info(ctx)
			if err != nil {
				return result, fmt.Errorf("repair drift on %q: %w", d.Name, err)
			}
			cfg := info.Config
			cfg.Subjects = w.Subjects
			if _, err := js.UpdateStream(ctx, cfg); err != nil {
				return result, fmt.Errorf("repair drift on %q: %w", d.Name, err)
			}
			result.Updated = append(result.Updated, d.Name)
		case StreamAbsent:
			if _, err := createManagedStream(ctx, js, w); err != nil {
				return result, fmt.Errorf("repair create %q: %w", d.Name, err)
			}
			result.Created = append(result.Created, d.Name)
		case StreamUnreadable:
			// force is true here: the guard above returned otherwise.
			if err := js.DeleteStream(ctx, d.Name); err != nil {
				return result, fmt.Errorf("repair recreate %q: delete failed: %w", d.Name, err)
			}
			if _, err := createManagedStream(ctx, js, w); err != nil {
				return result, fmt.Errorf("repair recreate %q: create failed: %w", d.Name, err)
			}
			result.Recreated = append(result.Recreated, d.Name)
			result.Discarded[d.Name] = d.Msgs
		}
	}
	return result, nil
}

// createManagedStream creates one managed stream the way provisioning does.
func createManagedStream(ctx context.Context, js jetstream.JetStream, w ExpectedStream) (jetstream.Stream, error) {
	return js.CreateStream(ctx, jetstream.StreamConfig{
		Name:      w.Name,
		Subjects:  w.Subjects,
		Storage:   jetstream.FileStorage,
		Retention: jetstream.LimitsPolicy,
		Replicas:  1,
		MaxAge:    w.MaxAge,
	})
}
