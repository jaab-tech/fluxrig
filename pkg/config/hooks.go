// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"reflect"

	"github.com/go-viper/mapstructure/v2"
	"github.com/google/uuid"
	"github.com/knadh/koanf/v2"
)

// UUIDHook provides a mapstructure decode hook to handle UUIDs from strings or numbers.
// Numbers are converted into a deterministic zero-padded UUID (Static Namespace).
func UUIDHook() mapstructure.DecodeHookFunc {
	return func(f reflect.Type, t reflect.Type, data any) (any, error) {
		if t != reflect.TypeOf(uuid.UUID{}) {
			return data, nil
		}

		switch v := data.(type) {
		case string:
			if v == "" || v == "0" {
				return uuid.Nil, nil
			}
			id, err := uuid.Parse(v)
			if err != nil {
				return nil, err
			}
			// Enforce UUID v7 for all manually configured identities
			if id != uuid.Nil && id.Version() != 7 {
				return nil, fmt.Errorf("invalid UUID version %d: only UUID v7 is supported for fluxrig identities", id.Version())
			}
			return id, nil

		default:
			return data, nil
		}
	}
}

// UnmarshalWithHooks wraps koanf's Unmarshal to include our custom hooks.
func (c *RackConfig) Unmarshal(k *koanf.Koanf) error {
	return k.UnmarshalWithConf("", c, koanf.UnmarshalConf{
		DecoderConfig: &mapstructure.DecoderConfig{
			DecodeHook: mapstructure.ComposeDecodeHookFunc(
				UUIDHook(),
				mapstructure.StringToTimeDurationHookFunc(),
				mapstructure.StringToSliceHookFunc(","),
			),
			Metadata:         nil,
			Result:           c,
			WeaklyTypedInput: true,
			TagName:          "koanf",
		},
	})
}

// UnmarshalMixerWithHooks is for Mixer.
func (c *MixerConfig) Unmarshal(k *koanf.Koanf) error {
	return k.UnmarshalWithConf("", c, koanf.UnmarshalConf{
		DecoderConfig: &mapstructure.DecoderConfig{
			DecodeHook: mapstructure.ComposeDecodeHookFunc(
				UUIDHook(),
				mapstructure.StringToTimeDurationHookFunc(),
				mapstructure.StringToSliceHookFunc(","),
			),
			Metadata:         nil,
			Result:           c,
			WeaklyTypedInput: true,
			TagName:          "koanf",
		},
	})
}
