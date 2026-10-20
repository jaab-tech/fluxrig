// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package codec

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

// The reference spec gives DE 3 of its base three positional parts. The field
// must still encode from any value its base accepts: a traffic generator
// sends a short processing code ("0", "00"), which the base pads to six
// digits. A compile that turned the field into a strict composite refused
// every value shorter than its parts.
func TestProcessingCodeEncodesFullAndShort(t *testing.T) {
	for v, want := range map[string]string{"500000": "500000", "000000": "000000", "0": "000000", "00": "000000", "5000": "005000"} {
		g := initGear(t, "../../../../../../../examples/specs/iso8583-v87-ascii.yaml",
			map[string]any{"direction": "encode", "on_error": "kill"})
		m := fluxmsg.New()
		m.Metadata["iso8583.mti"] = "0200"
		m.Data["proc_code"] = v
		m.Data["stan"] = "000001"
		out, err := g.Process(context.Background(), m)
		require.NoError(t, err, "proc_code %q", v)
		require.NotNil(t, out)
		require.True(t, strings.Contains(string(out.RawPayload), want+"000001"),
			"proc_code %q: the 0200 %q does not carry DE 3 %q", v, out.RawPayload, want)
	}
}
