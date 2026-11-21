// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/minio/minio-go/v7/pkg/encrypt"
	"github.com/nats-io/nats.go/jetstream"
)

// Remote is a store of telemetry files every Mixer reads: the one copy of the
// telemetry when several Mixers share it. Keys are slash-separated paths, such
// as logs/2026/11/04/13/logs_00000000000000000042.parquet.
type Remote interface {
	// Put stores the file at path under key, replacing what key held, and
	// returns a tag that changes whenever the content does.
	Put(ctx context.Context, key, path string) (tag string, err error)
	// List returns the objects whose key starts with prefix.
	List(ctx context.Context, prefix string) ([]RemoteObject, error)
	// Get writes the object at key to path.
	Get(ctx context.Context, key, path string) error
	// Delete removes the object at key.
	Delete(ctx context.Context, key string) error
}

// RemoteObject is one object of a Remote.
type RemoteObject struct {
	Key string
	Tag string
}

// S3Remote is a bucket of an S3-compatible object store.
type S3Remote struct {
	client *minio.Client
	bucket string
	prefix string
}

// S3Options configures NewS3Remote.
type S3Options struct {
	Endpoint, Bucket, Prefix, Region string
	AccessKey, SecretKey             string
	Plaintext                        bool
	CAFile                           string
}

// NewS3Remote connects to the bucket, and creates it when it does not exist.
// Empty keys take AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY.
func NewS3Remote(ctx context.Context, o S3Options) (*S3Remote, error) {
	creds := credentials.NewEnvAWS()
	if o.AccessKey != "" || o.SecretKey != "" {
		creds = credentials.NewStaticV4(o.AccessKey, o.SecretKey, "")
	}
	opts := &minio.Options{Creds: creds, Secure: !o.Plaintext, Region: o.Region}
	if o.CAFile != "" {
		pem, err := os.ReadFile(o.CAFile)
		if err != nil {
			return nil, fmt.Errorf("ingest: s3 ca file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("ingest: s3 ca file %s holds no certificate", o.CAFile)
		}
		opts.Transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	}
	client, err := minio.New(o.Endpoint, opts)
	if err != nil {
		return nil, fmt.Errorf("ingest: s3 client: %w", err)
	}
	exists, err := client.BucketExists(ctx, o.Bucket)
	if err != nil {
		return nil, fmt.Errorf("ingest: s3 bucket %s: %w", o.Bucket, err)
	}
	if !exists {
		if err = client.MakeBucket(ctx, o.Bucket, minio.MakeBucketOptions{Region: o.Region}); err != nil {
			return nil, fmt.Errorf("ingest: create s3 bucket %s: %w", o.Bucket, err)
		}
	}
	prefix := strings.Trim(o.Prefix, "/")
	if prefix != "" {
		prefix += "/"
	}
	return &S3Remote{client: client, bucket: o.Bucket, prefix: prefix}, nil
}

func (r *S3Remote) Put(ctx context.Context, key, path string) (string, error) {
	info, err := r.client.FPutObject(ctx, r.bucket, r.prefix+key, path, minio.PutObjectOptions{
		ContentType:          "application/vnd.apache.parquet",
		ServerSideEncryption: encrypt.NewSSE(),
	})
	if err != nil {
		return "", err
	}
	return info.ETag, nil
}

func (r *S3Remote) List(ctx context.Context, prefix string) ([]RemoteObject, error) {
	var out []RemoteObject
	for obj := range r.client.ListObjects(ctx, r.bucket, minio.ListObjectsOptions{Prefix: r.prefix + prefix, Recursive: true}) {
		if obj.Err != nil {
			return nil, obj.Err
		}
		out = append(out, RemoteObject{Key: strings.TrimPrefix(obj.Key, r.prefix), Tag: strings.Trim(obj.ETag, `"`)})
	}
	return out, nil
}

func (r *S3Remote) Get(ctx context.Context, key, path string) error {
	return r.client.FGetObject(ctx, r.bucket, r.prefix+key, path, minio.GetObjectOptions{})
}

func (r *S3Remote) Delete(ctx context.Context, key string) error {
	return r.client.RemoveObject(ctx, r.bucket, r.prefix+key, minio.RemoveObjectOptions{})
}

// NATSRemote is an object store bucket on the NATS of the Mixers. The object
// store lists only all of its objects at once, so a key-value bucket beside it
// indexes them by hour: a query reads the hours it needs, not the whole store.
type NATSRemote struct {
	store jetstream.ObjectStore
	index jetstream.KeyValue
}

// NewNATSRemote opens the bucket and its index, and creates them with
// replicas copies when they do not exist.
func NewNATSRemote(ctx context.Context, js jetstream.JetStream, bucket string, replicas int) (*NATSRemote, error) {
	store, err := js.CreateOrUpdateObjectStore(ctx, jetstream.ObjectStoreConfig{
		Bucket:      bucket,
		Description: "fluxrig telemetry: the Parquet files every Mixer reads",
		Storage:     jetstream.FileStorage,
		Replicas:    max(replicas, 1),
	})
	if err != nil {
		return nil, fmt.Errorf("ingest: object store %s: %w", bucket, err)
	}
	index, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:      bucket + "_index",
		Description: "fluxrig telemetry: the files of each hour in " + bucket,
		Storage:     jetstream.FileStorage,
		Replicas:    max(replicas, 1),
		History:     1,
	})
	if err != nil {
		return nil, fmt.Errorf("ingest: object store index %s_index: %w", bucket, err)
	}
	return &NATSRemote{store: store, index: index}, nil
}

// indexKey is the index entry of the hour a key or an hour prefix names:
// "logs/2026/11/04/13/x.parquet" and "logs/2026/11/04/13/" give
// "logs.2026.11.04.13". ok is false for a prefix that names no hour.
func indexKey(key string) (string, bool) {
	parts := strings.Split(key, "/")
	if len(parts) < 5 {
		return "", false
	}
	return strings.Join(parts[:5], "."), true
}

// indexUpdate changes the entry of key's hour with change, which gets the
// names and tags of that hour.
func (r *NATSRemote) indexUpdate(ctx context.Context, key string, change func(map[string]string)) error {
	ik, ok := indexKey(key)
	if !ok {
		return fmt.Errorf("ingest: %s names no hour", key)
	}
	entries := map[string]string{}
	var rev uint64
	cur, err := r.index.Get(ctx, ik)
	switch {
	case err == nil:
		if errJSON := json.Unmarshal(cur.Value(), &entries); errJSON != nil {
			return errJSON
		}
		rev = cur.Revision()
	case !errors.Is(err, jetstream.ErrKeyNotFound):
		return err
	}
	change(entries)
	data, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	if rev == 0 {
		_, err = r.index.Create(ctx, ik, data)
	} else {
		_, err = r.index.Update(ctx, ik, data, rev)
	}
	return err
}

func (r *NATSRemote) Put(ctx context.Context, key, path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	info, err := r.store.Put(ctx, jetstream.ObjectMeta{Name: key}, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	tag := natsTag(info)
	if err = r.indexUpdate(ctx, key, func(m map[string]string) { m[key] = tag }); err != nil {
		return "", fmt.Errorf("ingest: index %s: %w", key, err)
	}
	return tag, nil
}

func (r *NATSRemote) List(ctx context.Context, prefix string) ([]RemoteObject, error) {
	if ik, ok := indexKey(prefix); ok {
		cur, err := r.index.Get(ctx, ik)
		if errors.Is(err, jetstream.ErrKeyNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		entries := map[string]string{}
		if err = json.Unmarshal(cur.Value(), &entries); err != nil {
			return nil, err
		}
		out := make([]RemoteObject, 0, len(entries))
		for k, tag := range entries {
			if strings.HasPrefix(k, prefix) {
				out = append(out, RemoteObject{Key: k, Tag: tag})
			}
		}
		return out, nil
	}
	infos, err := r.store.List(ctx)
	if errors.Is(err, jetstream.ErrNoObjectsFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []RemoteObject
	for _, info := range infos {
		if !info.Deleted && strings.HasPrefix(info.Name, prefix) {
			out = append(out, RemoteObject{Key: info.Name, Tag: natsTag(info)})
		}
	}
	return out, nil
}

func (r *NATSRemote) Get(ctx context.Context, key, path string) error {
	res, err := r.store.Get(ctx, key)
	if err != nil {
		return err
	}
	defer func() { _ = res.Close() }()
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if _, err = io.Copy(f, res); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func (r *NATSRemote) Delete(ctx context.Context, key string) error {
	if err := r.store.Delete(ctx, key); err != nil && !errors.Is(err, jetstream.ErrObjectNotFound) {
		return err
	}
	return r.indexUpdate(ctx, key, func(m map[string]string) { delete(m, key) })
}

// natsTag is the digest of an object, which changes with its content.
func natsTag(info *jetstream.ObjectInfo) string {
	if info.Digest != "" {
		return info.Digest
	}
	return info.NUID
}
