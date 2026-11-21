// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"
)

func TestLoadMixer_RegistryDefaults(t *testing.T) {
	cfg, err := LoadMixer("")
	if err != nil {
		t.Fatal(err)
	}
	want := RegistryConfig{
		Bucket: "fluxrig_registry", LivenessBucket: "fluxrig_liveness", Replicas: 1,
		MaxValueSize: 1 << 20, LivenessTTL: "90s", WriteRetries: 3,
		SweepInterval: "10m", OrphanMinAge: "5m",
		LeaseBucket: "fluxrig_lease", LeaseTTL: "30s", LeaseRenew: "10s",
		SpecBucket: "fluxrig_specs", SpecMaxSize: 1 << 20,
	}
	if cfg.Registry != want {
		t.Errorf("registry defaults: got %+v, want %+v", cfg.Registry, want)
	}
}

// Zero retries is a setting (fail on the first conflict), not an unset
// field: neither the loader nor WithDefaults may turn it into the default.
func TestLoadMixer_RegistryZeroRetriesKept(t *testing.T) {
	cfg, err := LoadMixer(writeTempTOML(t, "[registry]\nwrite_retries = 0\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Registry.WithDefaults().WriteRetries; got != 0 {
		t.Errorf("write_retries = 0 became %d", got)
	}
}

func TestLoadMixer_RegistryRejectsBadSettings(t *testing.T) {
	for name, body := range map[string]string{
		"replicas":        "replicas = 0",
		"max_value_size":  "max_value_size = 0",
		"write_retries":   "write_retries = -1",
		"liveness_ttl":    `liveness_ttl = "0s"`,
		"same bucket":     `liveness_bucket = "fluxrig_registry"`,
		"value too large": "max_value_size = 4294967296",
		"sweep_interval":  `sweep_interval = "0s"`,
		"orphan_min_age":  `orphan_min_age = "0s"`,
	} {
		_, err := LoadMixer(writeTempTOML(t, "[registry]\n"+body+"\n"))
		if err == nil || !strings.Contains(err.Error(), "registry.") {
			t.Errorf("%s: want a registry.* error, got %v", name, err)
		}
	}
}

// Every key the external mode needs must load. An unknown key fails the whole
// config, so a documented key with no tag behind it turns a correct setup into
// a refusal to start.
func TestLoadMixer_ExternalKeysLoad(t *testing.T) {
	cfg, err := LoadMixer(writeTempTOML(t, `
[snake]
external = true
url = "tls://nats-1:4222,tls://nats-2:4222,tls://nats-3:4222"
replicas = 3
form_timeout = "90s"
root_ca_file = "/tmp/ca.pem"
client_cert_file = "/tmp/c.pem"
client_key_file = "/tmp/k.pem"
credentials_file = "/tmp/mixer.creds"

[registry]
lease_enabled = true
`))
	if err != nil {
		t.Fatalf("an external config must load: %v", err)
	}
	s := cfg.Snake
	if !s.External || s.Replicas != 3 || s.FormTimeout != "90s" || s.ClientCertFile != "/tmp/c.pem" || s.CredentialsFile != "/tmp/mixer.creds" {
		t.Errorf("unexpected snake settings: %+v", s)
	}
}

func TestLoadMixer_FormTimeoutMustBePositive(t *testing.T) {
	_, err := LoadMixer(writeTempTOML(t, "[snake]\nform_timeout = \"0s\"\n"))
	if err == nil || !strings.Contains(err.Error(), "snake.form_timeout") {
		t.Errorf("want a form_timeout error, got %v", err)
	}
}

// The embedded server runs alone, so more than one copy of a stream needs an
// external NATS, and a config written for a cluster of embedded servers no
// longer loads.
func TestLoadMixer_EmbeddedRunsAlone(t *testing.T) {
	_, err := LoadMixer(writeTempTOML(t, "[snake]\nreplicas = 3\n"))
	if err == nil || !strings.Contains(err.Error(), "snake.external") {
		t.Errorf("3 replicas on the embedded server: want an error naming snake.external, got %v", err)
	}
	if _, err := LoadMixer(writeTempTOML(t, "[snake.cluster]\nroutes = [\"nats://x:6222\"]\n")); err == nil {
		t.Error("snake.cluster: want an error, the key no longer exists")
	}
}

// A renew interval above half the TTL leaves room for at most one renew
// before the lease lapses, so it is refused at load.
func TestLoadMixer_RegistryLeaseRenewAtMostHalfTTL(t *testing.T) {
	if _, err := LoadMixer(writeTempTOML(t, "[registry]\nlease_enabled = true\nlease_ttl = \"30s\"\nlease_renew = \"15s\"\n")); err != nil {
		t.Fatalf("renew = ttl/2 must load: %v", err)
	}
	_, err := LoadMixer(writeTempTOML(t, "[registry]\nlease_enabled = true\nlease_ttl = \"30s\"\nlease_renew = \"20s\"\n"))
	if err == nil || !strings.Contains(err.Error(), "at most half") {
		t.Errorf("renew > ttl/2: want an error, got %v", err)
	}
}

// The shared spec store needs its own bucket with the lease on, and a size cap
// JetStream can take as a max value size.
func TestLoadMixer_SpecBucketSettings(t *testing.T) {
	if _, err := LoadMixer(writeTempTOML(t, "[registry]\nlease_enabled = true\nspec_bucket = \"fluxrig_registry\"\n")); err == nil ||
		!strings.Contains(err.Error(), "registry.spec_bucket") {
		t.Errorf("spec bucket equal to the registry bucket: want a spec_bucket error, got %v", err)
	}
	if _, err := LoadMixer(writeTempTOML(t, "[registry]\nspec_max_size = -1\n")); err == nil ||
		!strings.Contains(err.Error(), "registry.spec_max_size") {
		t.Errorf("negative spec_max_size: want an error, got %v", err)
	}
	cfg, err := LoadMixer(writeTempTOML(t, "[registry]\nlease_enabled = true\nspec_bucket = \"specs\"\nspec_max_size = 65536\n"))
	if err != nil || cfg.Registry.SpecBucket != "specs" || cfg.Registry.SpecMaxSize != 65536 {
		t.Errorf("custom spec settings: %v, %+v", err, cfg.Registry)
	}
}
