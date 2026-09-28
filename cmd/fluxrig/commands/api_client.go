// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"io"
	"net/http"
	"os"

	"github.com/spf13/cobra"
)

// addAPITokenFlag registers --api-token on cmd, consistent with how every
// Mixer-API-calling command already carries its own local --api-url rather
// than sharing one through a parent.
func addAPITokenFlag(cmd *cobra.Command, persistent bool) {
	const usage = "Bearer token for the Mixer API (or set FLUXRIG_API_TOKEN)"
	if persistent {
		cmd.PersistentFlags().String("api-token", "", usage)
	} else {
		cmd.Flags().String("api-token", "", usage)
	}
}

// resolveAPIToken returns the token to send with a Mixer API request: cmd's
// own --api-token flag if set, otherwise FLUXRIG_API_TOKEN. Empty means send
// no Authorization header at all, which is correct against a Mixer running
// with api.auth_disabled_dangerously and gets a clear 401 against any other:
// the Mixer's management API requires a token on every route but /health.
func resolveAPIToken(cmd *cobra.Command) string {
	if token, _ := cmd.Flags().GetString("api-token"); token != "" {
		return token
	}
	return os.Getenv("FLUXRIG_API_TOKEN")
}

// newAPIRequest builds a Mixer API request with the resolved bearer token
// attached. The one place every CLI command sends its credential, so a
// future auth change is made once instead of at each call site.
func newAPIRequest(cmd *cobra.Command, method, url string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	if token := resolveAPIToken(cmd); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req, nil
}
