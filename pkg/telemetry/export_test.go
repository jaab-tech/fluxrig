package telemetry

// ResetGlobalsForTest is a test helper to reset the global state.
// This prevents double-shutdown issues when tests run sequentially.
func ResetGlobalsForTest() {
	currentShutdown = nil
	currentBus = nil
	currentConfig = Config{}
}
