// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"sync"
	"time"
)

// MetricType defines the kind of metric stored.
type MetricType string

const (
	MetricTypeCounter MetricType = "counter"
	MetricTypeGauge   MetricType = "gauge"
)

// EntityStats holds all metrics for a single entity (Router, Gear, etc).
type EntityStats struct {
	EntityID   uint64                 `json:"entity_id"`
	EntityName string                 `json:"entity_name"`
	EntityType string                 `json:"entity_type"`
	LastUpdate time.Time              `json:"last_update"`
	Metrics    map[string]MetricValue `json:"metrics"`
	mu         sync.RWMutex
}

// MetricValue represents a single metric's current state.
type MetricValue struct {
	Name      string     `json:"name"`
	Type      MetricType `json:"type"`
	Value     float64    `json:"value"` // Float to support gauges and counters
	Timestamp time.Time  `json:"timestamp"`
}

// MetricsCache provides thread-safe access to in-memory metrics.
// It is designed for high-concurrency read/write (Many Racks -> One Mixer).
type MetricsCache struct {
	stats sync.Map // map[uint64]*EntityStats
}

// NewMetricsCache creates a new in-memory cache.
func NewMetricsCache() *MetricsCache {
	return &MetricsCache{}
}

// UpdateCounter atomically increments a counter for an entity.
func (c *MetricsCache) UpdateCounter(entityID uint64, name string, delta int64, entityName, entityType string) {
	s := c.getOrCreateEntity(entityID, entityName, entityType)
	s.mu.Lock()
	defer s.mu.Unlock()

	val, exists := s.Metrics[name]
	if !exists {
		val = MetricValue{
			Name: name,
			Type: MetricTypeCounter,
		}
	}
	val.Value += float64(delta)
	val.Timestamp = time.Now().UTC()
	s.Metrics[name] = val
	s.LastUpdate = val.Timestamp
}

// SetGauge atomically sets a gauge value for an entity.
func (c *MetricsCache) SetGauge(entityID uint64, name string, value float64, entityName, entityType string) {
	s := c.getOrCreateEntity(entityID, entityName, entityType)
	s.mu.Lock()
	defer s.mu.Unlock()

	val := MetricValue{
		Name:      name,
		Type:      MetricTypeGauge,
		Value:     value,
		Timestamp: time.Now().UTC(),
	}
	s.Metrics[name] = val
	s.LastUpdate = val.Timestamp
}

// GetStats returns a snapshot of metrics for a specific entity.
func (c *MetricsCache) GetStats(entityID uint64) *EntityStats {
	v, ok := c.stats.Load(entityID)
	if !ok {
		return nil
	}
	s := v.(*EntityStats)

	// Create a read-only snapshot
	s.mu.RLock()
	defer s.mu.RUnlock()

	snapshot := &EntityStats{
		EntityID:   s.EntityID,
		EntityName: s.EntityName,
		EntityType: s.EntityType,
		LastUpdate: s.LastUpdate,
		Metrics:    make(map[string]MetricValue, len(s.Metrics)),
	}
	for k, v := range s.Metrics {
		snapshot.Metrics[k] = v
	}
	return snapshot
}

// GetAllStats returns a snapshot of all entities.
func (c *MetricsCache) GetAllStats() []*EntityStats {
	var results []*EntityStats
	c.stats.Range(func(key, value interface{}) bool {
		s := value.(*EntityStats)
		s.mu.RLock()
		snapshot := &EntityStats{
			EntityID:   s.EntityID,
			EntityName: s.EntityName,
			EntityType: s.EntityType,
			LastUpdate: s.LastUpdate,
			Metrics:    make(map[string]MetricValue, len(s.Metrics)),
		}
		for k, v := range s.Metrics {
			snapshot.Metrics[k] = v
		}
		s.mu.RUnlock()
		results = append(results, snapshot)
		return true
	})
	return results
}

// internal helper
func (c *MetricsCache) getOrCreateEntity(id uint64, name, eType string) *EntityStats {
	v, ok := c.stats.Load(id)
	if ok {
		existing := v.(*EntityStats)
		// Update name if it was "pending" or empty and we now have a real name
		if name != "" && name != "pending" && (existing.EntityName == "" || existing.EntityName == "pending") {
			existing.mu.Lock()
			existing.EntityName = name
			if eType != "" {
				existing.EntityType = eType
			}
			existing.mu.Unlock()
		}
		return existing
	}

	// Double-check locking not strictly needed with sync.Map LoadOrStore
	newStats := &EntityStats{
		EntityID:   id,
		EntityName: name,
		EntityType: eType,
		Metrics:    make(map[string]MetricValue),
	}
	actual, _ := c.stats.LoadOrStore(id, newStats)
	return actual.(*EntityStats)
}

// Atomic helpers if we wanted lock-free updates (future opt),
// currently using mutex per entity is simpler for specific metric maps.
