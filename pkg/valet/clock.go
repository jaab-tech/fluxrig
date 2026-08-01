// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package valet

import "time"

// Clock abstracts time for the engine so expiry and retention behavior is
// deterministic under test. Production code uses SystemClock.
type Clock interface {
	Now() time.Time
	// AfterFunc runs f on its own goroutine after d elapses, unless the
	// returned timer is stopped first.
	AfterFunc(d time.Duration, f func()) Timer
}

// Timer is the stoppable handle returned by Clock.AfterFunc.
type Timer interface {
	// Stop prevents the timer from firing. It reports whether it succeeded
	// (false when the timer already fired or was stopped).
	Stop() bool
}

// SystemClock returns the real wall-clock implementation.
func SystemClock() Clock { return systemClock{} }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

func (systemClock) AfterFunc(d time.Duration, f func()) Timer {
	return time.AfterFunc(d, f)
}
