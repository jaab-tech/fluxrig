// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jaab-tech/fluxrig/pkg/wasm/catalog"
)

// fakeCatalog answers every call with err.
type fakeCatalog struct{ err error }

func (f fakeCatalog) Import(context.Context, []byte, bool) (catalog.WasmMetadata, error) {
	return catalog.WasmMetadata{Hash: "h"}, f.err
}
func (f fakeCatalog) List(context.Context) ([]catalog.WasmMetadata, error) { return nil, f.err }
func (f fakeCatalog) ListKeys(context.Context) ([]catalog.TrustedKey, error) {
	return nil, f.err
}
func (f fakeCatalog) TrustKey(context.Context, string, []byte) error { return f.err }
func (f fakeCatalog) RevokeKey(context.Context, string) error        { return f.err }

// An import the store did not take answers 503, not 200: nothing was
// imported, and the caller can try again. A refused module answers 400.
func TestWasmImport_StatusSaysWhatHappened(t *testing.T) {
	for _, c := range []struct {
		err  error
		want int
	}{
		{nil, http.StatusOK},
		{fmt.Errorf("%w: no response from stream", catalog.ErrUnavailable), http.StatusServiceUnavailable},
		{fmt.Errorf("sandbox validation failed"), http.StatusBadRequest},
	} {
		s := &Server{wasmCatalog: fakeCatalog{err: c.err}}
		rec := httptest.NewRecorder()
		s.handleWasmImport(rec, httptest.NewRequest(http.MethodPost, "/api/v1/wasm/import", bytes.NewReader([]byte("wasm"))))
		assert.Equal(t, c.want, rec.Code, "%v", c.err)
	}
}

func TestWasmKeys_Status(t *testing.T) {
	for _, c := range []struct {
		err  error
		want int
	}{
		{nil, http.StatusNoContent},
		{fmt.Errorf("%w: bad name", catalog.ErrInvalidKey), http.StatusBadRequest},
		{fmt.Errorf("%w: down", catalog.ErrUnavailable), http.StatusServiceUnavailable},
	} {
		s := &Server{wasmCatalog: fakeCatalog{err: c.err}}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/v1/wasm/keys/vendor", bytes.NewReader(make([]byte, 32)))
		req.SetPathValue("vendor", "vendor")
		s.handleWasmKeyTrust(rec, req)
		assert.Equal(t, c.want, rec.Code, "trust %v", c.err)
		rec = httptest.NewRecorder()
		s.handleWasmKeyRevoke(rec, req)
		assert.Equal(t, c.want, rec.Code, "revoke %v", c.err)
	}
}
