// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"
)

// A shared telemetry store needs what it writes to, and the lease: the Mixer
// that holds it is the only one that writes.
func TestLoadMixer_TelemetryStorage(t *testing.T) {
	for _, c := range []struct {
		name, body, want string
	}{
		{"local by default", "", ""},
		{"s3", "[registry]\nlease_enabled = true\n[observability.storage]\ntype = \"s3\"\n[observability.storage.s3]\nendpoint = \"s3:9000\"\nbucket = \"b\"\n", ""},
		{"nats", "[registry]\nlease_enabled = true\n[observability.storage]\ntype = \"nats\"\n", ""},
		{"otlp", "[registry]\nlease_enabled = true\n[observability.storage]\ntype = \"otlp\"\n[observability.storage.otlp]\nendpoint = \"https://c:4318\"\n", ""},
		{"s3 without a bucket", "[registry]\nlease_enabled = true\n[observability.storage]\ntype = \"s3\"\n[observability.storage.s3]\nendpoint = \"s3:9000\"\n", "s3.endpoint and bucket"},
		{"otlp without an endpoint", "[registry]\nlease_enabled = true\n[observability.storage]\ntype = \"otlp\"\n", "otlp.endpoint"},
		{"shared without the lease", "[observability.storage]\ntype = \"nats\"\n", "registry.lease_enabled"},
		{"unknown", "[observability.storage]\ntype = \"disk\"\n", "local, s3, nats or otlp"},
	} {
		_, err := LoadMixer(writeTempTOML(t, c.body))
		if c.want == "" {
			if err != nil {
				t.Errorf("%s: want it to load, got %v", c.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want an error naming %q, got %v", c.name, c.want, err)
		}
	}
}
