// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func specImportFlags(t *testing.T) *cobra.Command {
	t.Helper()
	c := &cobra.Command{}
	c.Flags().String("timeout", "5s", "")
	addAPITokenFlag(c, false)
	require.NoError(t, c.Flags().Set("api-token", "secret"))
	return c
}

// --mixer posts the file to POST /api/v1/specs with the token, names a
// nameless document after its file, and surfaces a refusal with its reason.
func TestImportSpecToMixer(t *testing.T) {
	var gotPath, gotQuery, gotAuth string
	var gotBody []byte
	status, answer := http.StatusCreated, `{"name":"plain","tag":"v1.2.0","hash":"abc"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotAuth = r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(answer))
	}))
	defer srv.Close()

	file := filepath.Join(t.TempDir(), "plain.yaml")
	require.NoError(t, os.WriteFile(file, []byte("body: 1\n"), 0o600))

	require.NoError(t, importSpecToMixer(specImportFlags(t), srv.URL+"/", file, "", "1.2.0"))
	assert.Equal(t, "/api/v1/specs", gotPath)
	assert.Equal(t, "name=plain&tag=1.2.0", gotQuery, "a nameless document is named after its file")
	assert.Equal(t, "Bearer secret", gotAuth)
	assert.Equal(t, "body: 1\n", string(gotBody))

	status, answer = http.StatusConflict, "spec import failed: version already exists"
	err := importSpecToMixer(specImportFlags(t), srv.URL, file, "plain", "v1.2.0")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "409")
	assert.Contains(t, err.Error(), "version already exists")
}
