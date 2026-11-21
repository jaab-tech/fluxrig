// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"errors"
	"net/http"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/registry"
	"github.com/jaab-tech/fluxrig/pkg/registry/forward"
)

// retryable reports whether a registry or control-write error means the
// cluster could not answer yet: no Mixer holds the write lease, or JetStream
// is electing a leader or has no stream that answers. Such a request did not
// fail; the caller may retry it.
func retryable(err error) bool {
	return errors.Is(err, forward.ErrForbiddenNoHolder) ||
		errors.Is(err, registry.ErrNotLeaseHolder) ||
		errors.Is(err, forward.ErrUnavailable) ||
		bus.Unavailable(err)
}

// errorStatus is the status for a registry or control-write error: 503 when
// the caller may retry, fallback otherwise.
func errorStatus(err error, fallback int) int {
	if retryable(err) {
		return http.StatusServiceUnavailable
	}
	return fallback
}
