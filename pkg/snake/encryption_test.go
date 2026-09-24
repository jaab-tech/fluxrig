// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package snake

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testMarker = "PAN=4111111111111111 STAN=000001"
	testKeyA   = "test-key-A-0123456789-0123456789-0123"
	testKeyB   = "test-key-B-0123456789-0123456789-0123"
)

func startSnake(t *testing.T, dir string, cfg Config) *Server {
	t.Helper()
	cfg.Port = -1
	cfg.ClusterName = "enc-test"
	cfg.StoreDir = dir
	cfg.StreamName = "flux-msg"
	cfg.StreamSubjects = []string{"flux.msg.>"}
	s, err := NewServer(context.Background(), cfg)
	require.NoError(t, err)
	return s
}

func jsOf(t *testing.T, s *Server) (jetstream.JetStream, func()) {
	t.Helper()
	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	return js, nc.Close
}

func publishMarker(t *testing.T, s *Server, n int) {
	t.Helper()
	js, closeNC := jsOf(t, s)
	defer closeNC()
	for i := 0; i < n; i++ {
		_, err := js.Publish(context.Background(), "flux.msg.rack.gear.out", []byte(testMarker))
		require.NoError(t, err)
	}
}

func streamMsgs(t *testing.T, s *Server) uint64 {
	t.Helper()
	js, closeNC := jsOf(t, s)
	defer closeNC()
	st, err := js.Stream(context.Background(), "flux-msg")
	require.NoError(t, err)
	info, err := st.Info(context.Background())
	require.NoError(t, err)
	return info.State.Msgs
}

// copiesOnDisk counts how often the marker is readable in any file under dir.
func copiesOnDisk(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	require.NoError(t, filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if data, errRead := os.ReadFile(path); errRead == nil {
			n += bytes.Count(data, []byte(testMarker))
		}
		return nil
	}))
	return n
}

// A message is readable in the store files without a key and not with one, under
// either cipher, and a client still gets it back in clear.
func TestServer_StoreEncryption(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want func(t *testing.T, copies int)
	}{
		{"no key", Config{}, func(t *testing.T, c int) { assert.Positive(t, c, "the control: without a key the marker is on disk") }},
		{"chacha", Config{StoreKey: testKeyA, StoreCipher: CipherChaCha}, func(t *testing.T, c int) { assert.Zero(t, c) }},
		{"default cipher", Config{StoreKey: testKeyA}, func(t *testing.T, c int) { assert.Zero(t, c) }},
		{"aes", Config{StoreKey: testKeyA, StoreCipher: CipherAES}, func(t *testing.T, c int) { assert.Zero(t, c) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			s := startSnake(t, dir, tc.cfg)
			publishMarker(t, s, 25)

			// A consumer gets the message in clear.
			js, closeNC := jsOf(t, s)
			cons, err := js.CreateOrUpdateConsumer(context.Background(), "flux-msg", jetstream.ConsumerConfig{AckPolicy: jetstream.AckNonePolicy})
			require.NoError(t, err)
			msg, err := cons.Next(jetstream.FetchMaxWait(5 * time.Second))
			require.NoError(t, err)
			assert.Equal(t, testMarker, string(msg.Data()))
			closeNC()

			s.Shutdown()
			tc.want(t, copiesOnDisk(t, dir))
		})
	}
}

func TestServer_RejectsAnUnknownCipher(t *testing.T) {
	_, err := NewServer(context.Background(), Config{Port: -1, ClusterName: "x", StoreDir: t.TempDir(), StoreKey: testKeyA, StoreCipher: "rot13"})
	assert.Error(t, err)
}

// A Mixer that already has an unencrypted store keeps its messages when the key is
// turned on, and none stays readable afterwards.
func TestServer_ConvertsAnUnencryptedStore(t *testing.T) {
	dir := t.TempDir()
	plain := startSnake(t, dir, Config{})
	publishMarker(t, plain, 40)
	plain.Shutdown()
	require.Positive(t, copiesOnDisk(t, dir), "the plain store holds the marker")

	encrypted := startSnake(t, dir, Config{StoreKey: testKeyA})
	assert.Equal(t, uint64(40), streamMsgs(t, encrypted), "no message is lost in the conversion")
	encrypted.Shutdown()

	assert.Zero(t, copiesOnDisk(t, dir), "after the conversion nothing is readable")
}

// Rotating the key needs the old one for the start that changes it.
func TestServer_KeyRotation(t *testing.T) {
	dir := t.TempDir()
	first := startSnake(t, dir, Config{StoreKey: testKeyA})
	publishMarker(t, first, 30)
	first.Shutdown()

	rotated := startSnake(t, dir, Config{StoreKey: testKeyB, StoreOldKey: testKeyA})
	assert.Equal(t, uint64(30), streamMsgs(t, rotated), "the old key lets the new one take over")
	rotated.Shutdown()

	again := startSnake(t, dir, Config{StoreKey: testKeyB})
	assert.Equal(t, uint64(30), streamMsgs(t, again), "afterwards the new key is enough")
	again.Shutdown()
	assert.Zero(t, copiesOnDisk(t, dir))
}

func TestServer_StreamLimits(t *testing.T) {
	dir := t.TempDir()
	s := startSnake(t, dir, Config{StreamMaxAge: 36 * time.Hour, StreamMaxBytes: 64 << 20})
	js, closeNC := jsOf(t, s)
	st, err := js.Stream(context.Background(), "flux-msg")
	require.NoError(t, err)
	info, err := st.Info(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 36*time.Hour, info.Config.MaxAge)
	assert.Equal(t, int64(64<<20), info.Config.MaxBytes)
	closeNC()
	s.Shutdown()

	// A stream that already exists takes the limits of the new start.
	again := startSnake(t, dir, Config{StreamMaxAge: 2 * time.Hour, StreamMaxBytes: 1 << 20})
	defer again.Shutdown()
	js2, closeNC2 := jsOf(t, again)
	defer closeNC2()
	st2, err := js2.Stream(context.Background(), "flux-msg")
	require.NoError(t, err)
	info2, err := st2.Info(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2*time.Hour, info2.Config.MaxAge)
	assert.Equal(t, int64(1<<20), info2.Config.MaxBytes)
}

// Opening an encrypted store without its key would show an empty stream. It is an
// error instead, and the right key opens it again.
func TestServer_RefusesAnEncryptedStoreWithoutItsKey(t *testing.T) {
	dir := t.TempDir()
	first := startSnake(t, dir, Config{StoreKey: testKeyA})
	publishMarker(t, first, 10)
	first.Shutdown()

	_, err := NewServer(context.Background(), Config{Port: -1, ClusterName: "enc-test", StoreDir: dir, StreamName: "flux-msg", StreamSubjects: []string{"flux.msg.>"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "was encrypted")

	back := startSnake(t, dir, Config{StoreKey: testKeyA})
	defer back.Shutdown()
	assert.Equal(t, uint64(10), streamMsgs(t, back), "the key still opens it")
}

// A wrong key fails the start rather than losing the messages.
func TestServer_WrongKeyFailsTheStart(t *testing.T) {
	dir := t.TempDir()
	first := startSnake(t, dir, Config{StoreKey: testKeyA})
	publishMarker(t, first, 10)
	first.Shutdown()

	_, err := NewServer(context.Background(), Config{Port: -1, ClusterName: "enc-test", StoreDir: dir, StreamName: "flux-msg", StreamSubjects: []string{"flux.msg.>"}, StoreKey: testKeyB})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not open with the current key", "the operator is told what happened and what to do")
	assert.Contains(t, err.Error(), "old key")
}

// The Coat Check gear parks context in a key-value bucket of the bus, which lives in
// this store. It stays unreadable on disk with a key, as a stream does, and is
// readable without one, so the check has something to detect.
func TestServer_KeyValueBucketsAreEncrypted(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want func(t *testing.T, copies int)
	}{
		{"no key", Config{}, func(t *testing.T, c int) { assert.Positive(t, c, "the control: without a key the value is on disk") }},
		{"with a key", Config{StoreKey: testKeyA}, func(t *testing.T, c int) { assert.Zero(t, c) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			s := startSnake(t, dir, tc.cfg)

			js, closeNC := jsOf(t, s)
			kv, err := js.CreateOrUpdateKeyValue(context.Background(), jetstream.KeyValueConfig{
				Bucket: "coatcheck", Storage: jetstream.FileStorage, History: 1, TTL: time.Hour,
			})
			require.NoError(t, err)
			for i := 0; i < 20; i++ {
				_, err = kv.Put(context.Background(), fmt.Sprintf("ticket-%d", i), []byte(testMarker))
				require.NoError(t, err)
			}
			entry, err := kv.Get(context.Background(), "ticket-3")
			require.NoError(t, err)
			assert.Equal(t, testMarker, string(entry.Value()), "the gear reads its context back in clear")
			closeNC()

			s.Shutdown()
			tc.want(t, copiesOnDisk(t, dir))
		})
	}
}
