// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// exerciseRemote puts, replaces, lists, fetches and deletes through r.
func exerciseRemote(t *testing.T, r Remote) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	src := filepath.Join(dir, "f.parquet")
	key := "logs/2026/11/04/13/logs_00000000000000000001.parquet"
	other := "metrics/2026/11/04/13/metrics_00000000000000000001.parquet"

	require.NoError(t, os.WriteFile(src, []byte("v1"), 0o600))
	tag1, err := r.Put(ctx, key, src)
	require.NoError(t, err)
	_, err = r.Put(ctx, other, src)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(src, []byte("v2"), 0o600))
	tag2, err := r.Put(ctx, key, src)
	require.NoError(t, err)
	assert.NotEqual(t, tag1, tag2, "the tag changes with the content")

	objs, err := r.List(ctx, "logs/2026/11/04/13/")
	require.NoError(t, err)
	require.Len(t, objs, 1, "only the hour and kind asked for")
	assert.Equal(t, key, objs[0].Key)
	assert.Equal(t, tag2, objs[0].Tag)

	got := filepath.Join(dir, "got")
	require.NoError(t, r.Get(ctx, key, got))
	b, _ := os.ReadFile(got)
	assert.Equal(t, "v2", string(b))

	require.NoError(t, r.Delete(ctx, key))
	objs, err = r.List(ctx, "logs/2026/11/04/13/")
	require.NoError(t, err)
	assert.Empty(t, objs)
	objs, err = r.List(ctx, "logs/2026/11/04/14/")
	require.NoError(t, err)
	assert.Empty(t, objs, "an hour with nothing lists nothing")
}

func TestS3Remote(t *testing.T) {
	serverSideEncryption := make(chan string, 10)
	backend := gofakes3.New(s3mem.New()).Server()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, ".parquet") {
			serverSideEncryption <- r.Header.Get("X-Amz-Server-Side-Encryption")
		}
		backend.ServeHTTP(w, r)
	}))
	defer srv.Close()
	r, err := NewS3Remote(context.Background(), S3Options{
		Endpoint: strings.TrimPrefix(srv.URL, "http://"), Bucket: "telemetry", Prefix: "fluxrig",
		Region: "us-east-1", AccessKey: "k", SecretKey: "s", Plaintext: true,
	})
	require.NoError(t, err)
	exerciseRemote(t, r)
	require.Len(t, serverSideEncryption, 3)
	for i := 0; i < 3; i++ {
		assert.Equal(t, "AES256", <-serverSideEncryption, "each uploaded object must request SSE-S3")
	}
}

func TestNATSRemote(t *testing.T) {
	exerciseRemote(t, natsRemote(t, startTelemetryStream(t)))
}
