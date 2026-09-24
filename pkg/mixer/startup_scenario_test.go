// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package mixer

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/jaab-tech/fluxrig/pkg/controller"
)

// A Mixer starting with a --scenario that pins a deploy target reaches no
// Rack by definition: nothing has enrolled yet. That must not read as a
// startup failure, the same way it must not have read as a successful
// activation before controller.Activate started reporting it (7ce4b93).
func TestLogStartupActivateResult_NoRackYetIsNotAWarning(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))

	logStartupActivateResult(log, "checkout", fmt.Errorf("scenario %q is active but was not delivered: %w", "checkout", controller.ErrNoRackReached))

	out := buf.String()
	if strings.Contains(out, "level=WARN") {
		t.Errorf("the ordinary no-Rack-enrolled-yet case must not log at Warn, got: %s", out)
	}
	if strings.Contains(out, "Failed to activate") {
		t.Errorf("the ordinary no-Rack-enrolled-yet case must not say the activation failed, got: %s", out)
	}
	if !strings.Contains(out, "level=INFO") {
		t.Errorf("expected an Info-level line reporting the scenario is active, got: %s", out)
	}
}

// Any other Activate failure (a real one, such as ErrUnknownDeployTarget or a
// parse error) must still be a visible Warn: this path only carves out the
// startup-specific race, not activation failures generally.
func TestLogStartupActivateResult_OtherErrorsStillWarn(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))

	logStartupActivateResult(log, "checkout", errors.New("scenario not found: checkout"))

	out := buf.String()
	if !strings.Contains(out, "level=WARN") {
		t.Errorf("a real activation failure must still log at Warn, got: %s", out)
	}
	if !strings.Contains(out, "Failed to activate startup scenario") {
		t.Errorf("a real activation failure must still say so, got: %s", out)
	}
}
