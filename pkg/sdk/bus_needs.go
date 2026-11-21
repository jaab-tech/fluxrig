// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package sdk

// BusNeed is something a gear does on the bus that the wire graph does not
// show: reading the connection events of another gear, or using a key-value
// bucket. A NATS that limits each Rack to what its scenario uses builds that
// Rack's permissions from the wires and from these.
type BusNeed struct {
	Kind BusNeedKind
	// Source is the gear whose connection events are read (BusReadEvents),
	// written as the scenario writes it: "gear" or "rack.gear".
	Source string
	// Bucket is the key-value bucket of BusReadBucket, BusWriteBucket and
	// BusOwnBucket.
	Bucket string
}

// BusNeedKind is what a gear does with the subject of a BusNeed.
type BusNeedKind string

const (
	// BusReadEvents reads the connection events of BusNeed.Source through a
	// durable consumer of this gear.
	BusReadEvents BusNeedKind = "read_events"
	// BusReadBucket reads keys of an existing bucket.
	BusReadBucket BusNeedKind = "read_bucket"
	// BusWriteBucket reads, writes and deletes keys of an existing bucket.
	BusWriteBucket BusNeedKind = "write_bucket"
	// BusOwnBucket creates the bucket and watches it, besides BusWriteBucket.
	BusOwnBucket BusNeedKind = "own_bucket"
)
