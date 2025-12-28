package telemetry

// Config holds the configuration for the Telemetry provider.
type Config struct {
	// ServiceName identifies the application (e.g. "flux-rack", "flux-mixer").
	ServiceName string
	// ServiceVersion identifies the version.
	ServiceVersion string
	// EntityID is the unique fluxEntityID (uint64) of the component.
	EntityID uint64
	// EntityName is the human-readable name of the component (e.g. "rack-nyc-01").
	EntityName string

	// NatsURL is the endpoint for the NATS cluster (Option B: NATS Transport).
	NatsURL string

	// BatchInterval is the time to buffer spans/logs before sending (for NATS integrity).
	// Default: 5s
	BatchIntervalString string

	// BaseSubject is the NATS subject prefix.
	// Default: "flux.telemetry"
	BaseSubject string

	// MaxBatchSize is the number of items to buffer before forcing a flush.
	// Default: 512
	MaxBatchSize int
}
