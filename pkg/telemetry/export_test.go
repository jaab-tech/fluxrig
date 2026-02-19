// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package telemetry

// ResetGlobalsForTest is a test helper to reset the global state.
// This prevents double-shutdown issues when tests run sequentially.
func ResetGlobalsForTest() {
	currentShutdown = nil
	currentMetrics = nil
	currentMP = nil
}
