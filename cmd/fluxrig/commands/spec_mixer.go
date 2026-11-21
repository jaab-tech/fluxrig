// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/jaab-tech/fluxrig/pkg/manager"
	"github.com/jaab-tech/fluxrig/pkg/manager/cas"
)

// importSpecToMixer sends a spec file to a Mixer's POST /api/v1/specs. A
// document that declares no name is sent under its file name, as a local
// import names it.
func importSpecToMixer(cmd *cobra.Command, mixerURL, file, name, tag string) error {
	content, err := os.ReadFile(filepath.Clean(file))
	if err != nil {
		return fmt.Errorf("read %s: %w", file, err)
	}
	if name == "" && manager.ParseHeader(content).Name == "" {
		base := filepath.Base(file)
		name = strings.TrimSuffix(base, filepath.Ext(base))
	}
	timeoutStr, _ := cmd.Flags().GetString("timeout")
	timeout, err := time.ParseDuration(timeoutStr)
	if err != nil || timeout <= 0 {
		return fmt.Errorf("--timeout must be a positive duration, got %q", timeoutStr)
	}

	q := url.Values{}
	if name != "" {
		q.Set("name", name)
	}
	if tag != "" {
		q.Set("tag", tag)
	}
	target := strings.TrimSuffix(mixerURL, "/") + "/api/v1/specs"
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	req, err := newAPIRequest(cmd, http.MethodPost, target, bytes.NewReader(content))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-yaml")
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return fmt.Errorf("import into %s: %w", mixerURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("import into %s: %s: %s", mixerURL, resp.Status, strings.TrimSpace(string(body)))
	}
	var stored struct {
		Name string `json:"name"`
		Tag  string `json:"tag"`
		Hash string `json:"hash"`
	}
	if err := json.Unmarshal(body, &stored); err != nil {
		return fmt.Errorf("import into %s: read the answer: %w", mixerURL, err)
	}
	fmt.Printf("Imported %s -> %s (%s)\n", cas.Ref(stored.Name, stored.Tag, stored.Hash), stored.Hash, mixerURL)
	return nil
}
