// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/snake"
	"github.com/jaab-tech/fluxrig/pkg/store/duckdb"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func startTelemetryStream(t *testing.T) jetstream.JetStream {
	t.Helper()
	s, err := snake.NewServer(context.Background(), snake.Config{
		Port: -1, StoreDir: t.TempDir(), StreamName: "flux-telemetry", StreamSubjects: []string{"flux.telemetry.>"},
	})
	require.NoError(t, err)
	t.Cleanup(s.Shutdown)
	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	return js
}

// mixerStore is the DuckDB of one Mixer, with its own directory.
func mixerStore(t *testing.T) *duckdb.Store {
	t.Helper()
	store, err := duckdb.NewStore(quiet, filepath.Join(t.TempDir(), "flux.duckdb"))
	require.NoError(t, err)
	require.NoError(t, store.Migrate(context.Background()))
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func publishLines(t *testing.T, js jetstream.JetStream, from, to int) {
	t.Helper()
	for i := from; i <= to; i++ {
		fm := fluxmsg.New()
		fm.Metadata["type"] = TypeLog
		fm.Data = map[string]any{"body": fmt.Sprintf("line %d", i), "entity_name": "rack-t", "severity": "INFO",
			"timestamp": uint64(time.Now().UnixMicro())}
		data, err := cbor.Marshal(fm)
		require.NoError(t, err)
		_, err = js.Publish(context.Background(), "flux.telemetry.logs", data)
		require.NoError(t, err)
	}
}

// writer is one Mixer's writer of the shared store, holding the lease while
// held is set.
type writer struct {
	held   atomic.Bool
	sink   *SharedSink
	cancel context.CancelFunc
	done   chan struct{}
}

func startWriter(t *testing.T, js jetstream.JetStream, remote Remote, wrap func(Committer) Committer) *writer {
	t.Helper()
	store := mixerStore(t)
	var commit Committer = &ObjectCommitter{Store: store, Remote: remote, Log: quiet}
	if wrap != nil {
		commit = wrap(commit)
	}
	w := &writer{done: make(chan struct{})}
	w.sink = &SharedSink{
		JS: js, Stream: "flux-telemetry", Subject: "flux.telemetry.>",
		Persist: &TelemetrySink{store: store}, Store: store, Commit: commit, Held: w.held.Load,
		BatchInterval: 200 * time.Millisecond, CommitTimeout: 500 * time.Millisecond, Log: quiet,
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	go func() { defer close(w.done); w.sink.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-w.done })
	return w
}

// stored reads the shared store the way a third Mixer does, and returns how
// many times each line is in it.
func stored(t *testing.T, remote Remote) map[string]int {
	t.Helper()
	reader := mixerStore(t)
	cache := &Cache{Dir: reader.TelemetryDir(), Remote: remote, Log: quiet}
	reader.SetBeforeQuery(cache.Sync)
	logs, err := reader.QueryLogsFiltered(context.Background(), duckdb.LogQuery{Since: time.Now().Add(-time.Hour), Limit: 100000})
	require.NoError(t, err)
	seen := map[string]int{}
	for _, l := range logs {
		seen[l.Body]++
	}
	return seen
}

// waitStored polls until the store holds lines 1 to n, and fails on a line
// held twice.
func waitStored(t *testing.T, remote Remote, n int) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		seen := stored(t, remote)
		for line, count := range seen {
			require.Equal(t, 1, count, "%s is in the store %d times", line, count)
		}
		if len(seen) == n {
			return
		}
		if time.Now().After(deadline) {
			var missing []string
			for i := 1; i <= n; i++ {
				if seen[fmt.Sprintf("line %d", i)] == 0 {
					missing = append(missing, fmt.Sprint(i))
				}
			}
			t.Fatalf("the store holds %d of %d lines; missing %s", len(seen), n, strings.Join(missing, ","))
		}
		time.Sleep(200 * time.Millisecond) // poll interval
	}
}

func natsRemote(t *testing.T, js jetstream.JetStream) Remote {
	t.Helper()
	r, err := NewNATSRemote(context.Background(), js, "telemetry_test", 1)
	require.NoError(t, err)
	return r
}

// The lease moves from one Mixer to another while the Racks keep sending:
// the store ends with every line once.
func TestSharedSink_HandoverKeepsEveryLineOnce(t *testing.T) {
	js := startTelemetryStream(t)
	remote := natsRemote(t, js)
	a := startWriter(t, js, remote, nil)
	b := startWriter(t, js, remote, nil)

	a.held.Store(true)
	publishLines(t, js, 1, 40)
	waitStored(t, remote, 40)

	a.held.Store(false)
	publishLines(t, js, 41, 80)
	b.held.Store(true)
	publishLines(t, js, 81, 120)
	waitStored(t, remote, 120)
}

// crashAfterCommit commits the first batch and then fails as a holder that
// died before the stream had its confirmation.
type crashAfterCommit struct {
	Committer
	once  sync.Once
	crash func()
}

var errCrashed = errors.New("the holder died")

func (c *crashAfterCommit) Commit(ctx context.Context, first uint64, at time.Time) error {
	err := c.Committer.Commit(ctx, first, at)
	crashed := false
	c.once.Do(func() { crashed = true })
	if err == nil && crashed {
		c.crash()
		return errCrashed
	}
	return err
}

// A holder dies after the store took its batch and before the stream had the
// confirmation. The next holder gets the batch again, and replaces its files:
// the store holds each line once.
func TestSharedSink_BatchCommittedTwiceIsKeptOnce(t *testing.T) {
	js := startTelemetryStream(t)
	remote := natsRemote(t, js)
	var a *writer
	a = startWriter(t, js, remote, func(c Committer) Committer {
		return &crashAfterCommit{Committer: c, crash: func() { a.held.Store(false) }}
	})
	b := startWriter(t, js, remote, nil)

	publishLines(t, js, 1, 30)
	a.held.Store(true)
	deadline := time.Now().Add(20 * time.Second)
	for a.held.Load() {
		require.True(t, time.Now().Before(deadline), "the first holder never committed")
		time.Sleep(50 * time.Millisecond) // poll interval
	}
	b.held.Store(true)
	publishLines(t, js, 31, 50)
	waitStored(t, remote, 50)
}

// fakeRemote is a Remote in memory.
type fakeRemote struct {
	mu   sync.Mutex
	data map[string][]byte
	tags map[string]string
	n    int
}

func (f *fakeRemote) Put(_ context.Context, key, path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.data == nil {
		f.data, f.tags = map[string][]byte{}, map[string]string{}
	}
	f.n++
	f.data[key], f.tags[key] = b, fmt.Sprintf("tag%d", f.n)
	return f.tags[key], nil
}

func (f *fakeRemote) List(_ context.Context, prefix string) ([]RemoteObject, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []RemoteObject
	for k, tag := range f.tags {
		if strings.HasPrefix(k, prefix) {
			out = append(out, RemoteObject{Key: k, Tag: tag})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (f *fakeRemote) Get(_ context.Context, key, path string) error {
	f.mu.Lock()
	b := f.data[key]
	f.mu.Unlock()
	return os.WriteFile(path, b, 0o600)
}

func (f *fakeRemote) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.data, key)
	delete(f.tags, key)
	return nil
}

// A copy in the cache of a file the store has since replaced, or removed, is
// dropped at the next query, so a query never reads both versions.
func TestCache_DropsReplacedAndRemovedCopies(t *testing.T) {
	remote := &fakeRemote{}
	dir := t.TempDir()
	src := filepath.Join(t.TempDir(), "x.parquet")
	require.NoError(t, os.WriteFile(src, []byte("v1"), 0o600))
	hour := time.Now().UTC().Truncate(time.Hour)
	key := "logs/" + hour.Format("2006/01/02/15") + "/logs_00000000000000000001.parquet"
	_, err := remote.Put(context.Background(), key, src)
	require.NoError(t, err)
	cache := &Cache{Dir: dir, Remote: remote, Log: quiet}
	ctx := context.Background()
	require.NoError(t, cache.Sync(ctx, "logs", hour, time.Time{}))
	files := func() []string {
		m, _ := filepath.Glob(filepath.Join(dir, "logs", "*", "*", "*", "*", "*.parquet"))
		return m
	}
	require.Len(t, files(), 1)

	require.NoError(t, os.WriteFile(src, []byte("v2"), 0o600))
	_, err = remote.Put(ctx, key, src)
	require.NoError(t, err)
	require.NoError(t, cache.Sync(ctx, "logs", hour, time.Time{}))
	got := files()
	require.Len(t, got, 1, "only the new version stays")
	b, _ := os.ReadFile(got[0])
	assert.Equal(t, "v2", string(b))

	require.NoError(t, remote.Delete(ctx, key))
	require.NoError(t, cache.Sync(ctx, "logs", hour, time.Time{}))
	assert.Empty(t, files(), "a file removed from the store leaves the cache too")
}

// stopAfterFetch reads one batch, and stops as a holder that dies before it
// commits: the batch stays delivered to it and unconfirmed.
type stopAfterFetch struct {
	Committer
	crash func()
}

func (c *stopAfterFetch) Commit(context.Context, uint64, time.Time) error {
	c.crash()
	return errCrashed
}

// A holder dies with a batch read and not committed. The next holder must get
// that batch too, though the stream still counts it as delivered to the dead
// one: confirming its own first batch must not confirm the dead one's.
func TestSharedSink_BatchOfADeadHolderIsNotLost(t *testing.T) {
	js := startTelemetryStream(t)
	remote := natsRemote(t, js)
	var a *writer
	a = startWriter(t, js, remote, func(c Committer) Committer {
		return &stopAfterFetch{Committer: c, crash: func() { a.held.Store(false); a.cancel() }}
	})
	b := startWriter(t, js, remote, nil)

	publishLines(t, js, 1, 30)
	a.held.Store(true)
	select {
	case <-a.done:
	case <-time.After(20 * time.Second):
		t.Fatal("the first holder never read a batch")
	}
	b.held.Store(true)
	publishLines(t, js, 31, 60)
	waitStored(t, remote, 60)
}

// syncBuffer is a log destination two goroutines can share.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// A holder that cannot read the telemetry stream says so: it would commit
// nothing and stay silent otherwise.
func TestSharedSink_AHolderThatCannotReadSaysSo(t *testing.T) {
	js := startTelemetryStream(t)
	store := mixerStore(t)
	logs := &syncBuffer{}
	sink := &SharedSink{
		JS: js, Stream: "flux-telemetry", Subject: "flux.telemetry.>",
		Persist: &TelemetrySink{store: store}, Store: store,
		Commit: &ObjectCommitter{Store: store, Remote: natsRemote(t, js), Log: quiet}, Held: func() bool { return true },
		BatchInterval: 200 * time.Millisecond, CommitTimeout: 500 * time.Millisecond,
		Log: slog.New(slog.NewTextHandler(logs, nil)),
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); sink.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(logs.String(), "holds the lease and writes") {
		require.True(t, time.Now().Before(deadline), "the holder never started")
		time.Sleep(20 * time.Millisecond) // poll interval
	}
	require.NoError(t, js.DeleteConsumer(context.Background(), "flux-telemetry", SharedDurable))
	for !strings.Contains(logs.String(), "the holder read nothing from the telemetry stream") {
		require.True(t, time.Now().Before(deadline), "no warning after the consumer went: %s", logs.String())
		time.Sleep(20 * time.Millisecond) // poll interval
	}
}

// A holder with nothing to read is not warned about: an idle stream is not a
// failure.
func TestSharedSink_AnIdleStreamIsNotAWarning(t *testing.T) {
	js := startTelemetryStream(t)
	store := mixerStore(t)
	logs := &syncBuffer{}
	sink := &SharedSink{
		JS: js, Stream: "flux-telemetry", Subject: "flux.telemetry.>",
		Persist: &TelemetrySink{store: store}, Store: store,
		Commit: &ObjectCommitter{Store: store, Remote: natsRemote(t, js), Log: quiet}, Held: func() bool { return true },
		BatchInterval: 200 * time.Millisecond, CommitTimeout: 500 * time.Millisecond,
		Log: slog.New(slog.NewTextHandler(logs, nil)),
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); sink.Run(ctx) }()

	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(logs.String(), "holds the lease and writes") {
		require.True(t, time.Now().Before(deadline), "the holder never started")
		time.Sleep(20 * time.Millisecond) // poll interval
	}
	// Ten batch windows with nothing published.
	idle := time.Now().Add(10 * sink.BatchInterval)
	for time.Now().Before(idle) {
		time.Sleep(20 * time.Millisecond) // poll interval
	}
	cancel()
	<-done
	assert.NotContains(t, logs.String(), "read nothing", "an idle stream warned")
}
