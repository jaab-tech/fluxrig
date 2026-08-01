// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

//go:build nobento

package gears

// registerOptional is a no-op in `nobento` builds: the Bento gear and its
// dependency tree (protobuf, cue, avro, gojq, ...) are excluded from the binary.
//
// A scenario that declares `type: bento` fails loudly at activation with
// "unknown gear type: bento" rather than degrading silently, so an operator
// running the lean binary against a Bento scenario finds out immediately.
func registerOptional(_ *Factory) {}
