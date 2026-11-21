// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/store/duckdb"
)

// SharedDurable names the consumer of the telemetry stream that the lease
// holder reads. Every Mixer uses the same one, so a new holder goes on from
// the last batch the previous one confirmed.
const SharedDurable = "fluxrig_telemetry_store"

// Committer makes the rows in the store's telemetry tables durable, as one
// batch.
type Committer interface {
	// Commit makes the batch that starts at stream sequence first, and whose
	// first message the stream received at at, durable. After a failure it is
	// called again for the same batch, and finishes it.
	//
	// Commit is idempotent across holders: the files of a batch are named
	// after its first sequence (see batchName), and the remote key derives
	// from the file, so two holders committing the same batch replace the
	// same files and the same keys. A batch committed again after the
	// previous holder died replaces its own files: nothing is kept twice.
	// A Commit that returns nil has made the whole batch durable; a later
	// Commit of the same batch is a harmless replace. Callers still fence
	// with Held between Commit and the stream confirmation (see batch), so
	// a holder that lost the lease mid-commit does not confirm for another.
	Commit(ctx context.Context, first uint64, at time.Time) error
	// Discard drops a batch that did not commit.
	Discard(ctx context.Context)
}

// SharedSink reads the telemetry stream on the Mixer that holds the write
// lease, and commits it in batches to a store every Mixer shares. A batch is
// confirmed to the stream only once committed, so when the holder changes,
// the new one goes on from the last confirmed batch: nothing is lost. The
// files of a batch are named after its first sequence, so a batch committed
// again after the previous holder died replaces its own files: nothing is
// kept twice.
type SharedSink struct {
	JS      jetstream.JetStream
	Stream  string
	Subject string
	// Persist decodes a message into the store's telemetry tables.
	Persist *TelemetrySink
	Store   *duckdb.Store
	Commit  Committer
	// Held reports whether this Mixer holds the write lease.
	Held          func() bool
	BatchInterval time.Duration
	CommitTimeout time.Duration
	Log           *slog.Logger

	lastCommitted uint64
}

// sharedFetch is how many messages one fetch asks for.
const sharedFetch = 500

// leaseCheck is how often a Mixer that does not hold the lease looks again.
const leaseCheck = time.Second

// Run serves until ctx ends: it reads and commits while this Mixer holds the
// lease, and waits while it does not.
func (s *SharedSink) Run(ctx context.Context) {
	for {
		if s.Held() {
			s.lead(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(leaseCheck):
		}
	}
}

// ackWait outlasts a batch and its commit, so the stream does not hand a
// message to the holder again while the holder still works on it. A new
// holder does not wait for it: it takes the unconfirmed messages over.
func (s *SharedSink) ackWait() time.Duration { return 2*s.BatchInterval + 2*s.CommitTimeout }

// lead reads and commits until this Mixer loses the lease or ctx ends.
func (s *SharedSink) lead(ctx context.Context) {
	cons, start, err := s.takeOver(ctx)
	if err != nil {
		s.Log.Warn("telemetry store: cannot open the consumer of the telemetry stream", "stream", s.Stream, "error", err)
		return
	}
	s.lastCommitted = start - 1
	s.Log.Info("telemetry store: this Mixer holds the lease and writes the telemetry", "stream", s.Stream, "from_seq", start)
	for ctx.Err() == nil {
		if !s.batch(ctx, cons) {
			s.Log.Info("telemetry store: this Mixer no longer holds the lease; another one writes")
			return
		}
	}
}

// takeOver opens the consumer from the first message no holder confirmed.
//
// A holder that died leaves its batch delivered to it and unconfirmed. The
// stream hands such messages again only after the ack wait, and meanwhile it
// hands the new holder the messages after them: with AckAll, the new
// holder's first confirmation would then confirm the dead holder's batch, and
// it would never be stored. So the new holder recreates the consumer from the
// ack floor: it gets every unconfirmed message at once, in order, and its
// first batch starts where the dead holder's did, so it replaces that batch's
// files if they were stored.
func (s *SharedSink) takeOver(ctx context.Context) (jetstream.Consumer, uint64, error) {
	cfg := jetstream.ConsumerConfig{
		Durable:       SharedDurable,
		FilterSubject: s.Subject,
		AckPolicy:     jetstream.AckAllPolicy,
		AckWait:       s.ackWait(),
		DeliverPolicy: jetstream.DeliverAllPolicy,
	}
	start := uint64(1)
	old, err := s.JS.Consumer(ctx, s.Stream, SharedDurable)
	switch {
	case err == nil:
		start = old.CachedInfo().AckFloor.Stream + 1
		if err = s.JS.DeleteConsumer(ctx, s.Stream, SharedDurable); err != nil && !errors.Is(err, jetstream.ErrConsumerNotFound) {
			return nil, 0, fmt.Errorf("reset the consumer: %w", err)
		}
		cfg.DeliverPolicy, cfg.OptStartSeq = jetstream.DeliverByStartSequencePolicy, start
	case !errors.Is(err, jetstream.ErrConsumerNotFound):
		return nil, 0, err
	}
	cons, err := s.JS.CreateConsumer(ctx, s.Stream, cfg)
	if err != nil {
		return nil, 0, err
	}
	return cons, start, nil
}

// batch reads one batch and commits it. It returns false when the lease is
// lost, after dropping what it had not committed.
func (s *SharedSink) batch(ctx context.Context, cons jetstream.Consumer) bool {
	var (
		first, lastSeq uint64
		at             time.Time
		last           jetstream.Msg
		stale          jetstream.Msg
		fetchErr       error
	)
	deadline := time.Now().Add(s.BatchInterval)
	for time.Now().Before(deadline) {
		if !s.Held() {
			s.drop(ctx)
			return false
		}
		wait := min(time.Until(deadline), time.Second)
		if wait <= 0 {
			break
		}
		msgs, err := cons.Fetch(sharedFetch, jetstream.FetchMaxWait(wait))
		if err != nil {
			s.Log.Debug("telemetry store: fetch failed", "error", err)
			fetchErr = err
			time.Sleep(wait)
			continue
		}
		for m := range msgs.Messages() {
			meta, errMeta := m.Metadata()
			if errMeta != nil {
				continue
			}
			seq := meta.Sequence.Stream
			if seq <= s.lastCommitted {
				// Handed again after a redelivery: committed already.
				stale = m
				continue
			}
			var fm fluxmsg.FluxMsg
			if errDec := cbor.Unmarshal(m.Data(), &fm); errDec != nil {
				s.Log.Warn("telemetry store: a message does not decode, it is skipped", "seq", seq, "error", errDec)
			} else if errP := s.Persist.handleMessage(ctx, &fm); errP != nil {
				s.Log.Warn("telemetry store: a message was not stored whole", "seq", seq, "error", errP)
			}
			if first == 0 {
				first, at = seq, meta.Timestamp
			}
			last, lastSeq = m, seq
		}
		// A pull that failed ends its batch early and reports the failure
		// here, not as the error of Fetch.
		if errBatch := msgs.Error(); errBatch != nil {
			s.Log.Debug("telemetry store: fetch ended with an error", "error", errBatch)
			fetchErr = errBatch
		}
	}
	if last == nil {
		if stale != nil {
			_ = stale.Ack()
		}
		// A holder that cannot read the stream commits nothing, and would say
		// nothing about it: once per batch window, it says why.
		if fetchErr != nil {
			s.Log.Warn("telemetry store: the holder read nothing from the telemetry stream in this batch window",
				"stream", s.Stream, "error", fetchErr)
		}
		return s.Held()
	}
	for attempt := 1; ; attempt++ {
		cctx, cancel := context.WithTimeout(ctx, s.CommitTimeout)
		err := s.Commit.Commit(cctx, first, at)
		cancel()
		if err == nil {
			break
		}
		s.Log.Warn("telemetry store: a batch did not commit; it is tried again, and stays unconfirmed until it commits",
			"first", first, "last", lastSeq, "attempt", attempt, "error", err)
		if !s.Held() || ctx.Err() != nil {
			s.drop(ctx)
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(time.Second):
		}
	}
	// Fencing re-check: the lease may have moved while the last attempt
	// committed. A holder that lost it drops what it committed instead of
	// confirming it: the new holder replays the batch from the ack floor
	// and replaces the same files (Commit's idempotency contract), so
	// confirming here would only race its replay.
	if !s.Held() || ctx.Err() != nil {
		s.drop(ctx)
		return false
	}
	// AckAll confirms every message up to this one.
	if err := last.Ack(); err != nil {
		s.Log.Warn("telemetry store: the confirmation of a committed batch failed; the stream hands it again, and it replaces its own files",
			"last", lastSeq, "error", err)
	}
	s.lastCommitted = lastSeq
	return true
}

// drop discards what this Mixer read and did not commit: the new holder
// reads it again from the stream.
func (s *SharedSink) drop(ctx context.Context) {
	if err := s.Store.ClearTelemetry(ctx); err != nil {
		s.Log.Warn("telemetry store: cannot drop an uncommitted batch", "error", err)
	}
	s.Commit.Discard(ctx)
	s.lastCommitted = 0
}

// batchName is the part of a file name that comes from its batch.
func batchName(first uint64) string { return fmt.Sprintf("%020d", first) }

// ObjectCommitter commits a batch as Parquet files to a Remote. The files
// stay on this Mixer's disk as the first copy of its query cache.
type ObjectCommitter struct {
	Store  *duckdb.Store
	Remote Remote
	Log    *slog.Logger

	name    string
	pending map[string]string // kind -> local file not stored yet
}

func (c *ObjectCommitter) Commit(ctx context.Context, first uint64, at time.Time) error {
	name := batchName(first)
	if c.pending == nil || c.name != name {
		files, err := c.Store.FlushTelemetryBatch(ctx, name, at)
		if err != nil {
			return fmt.Errorf("write the batch: %w", err)
		}
		c.name, c.pending = name, files
	}
	dir := c.Store.TelemetryDir()
	for kind, path := range c.pending {
		key, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		key = filepath.ToSlash(key)
		tag, err := c.Remote.Put(ctx, key, path)
		if err != nil {
			return fmt.Errorf("store %s: %w", key, err)
		}
		if err = os.Rename(path, taggedPath(path, tag)); err != nil {
			c.Log.Warn("telemetry store: cannot tag a local file; the next query fetches it again", "file", path, "error", err)
		}
		delete(c.pending, kind)
	}
	c.pending = nil
	return nil
}

func (c *ObjectCommitter) Discard(context.Context) {
	for _, path := range c.pending {
		_ = os.Remove(path)
	}
	c.pending, c.name = nil, ""
}

var notTag = regexp.MustCompile(`[^A-Za-z0-9]`)

// taggedPath is the local name of a stored file: the key's name with the
// object's tag before the extension, so a file the store replaced is told
// from the copy here.
func taggedPath(path, tag string) string {
	t := notTag.ReplaceAllString(tag, "")
	if len(t) > 16 {
		t = t[:16]
	}
	return strings.TrimSuffix(path, ".parquet") + "." + t + ".parquet"
}

// tagged reports whether a local file name carries the tag of a stored
// object: kind_<20 digits>.<tag>.parquet, where a file not stored yet has no
// tag.
func tagged(name string) bool {
	return strings.Count(strings.TrimSuffix(name, ".parquet"), ".") == 1
}

// RemoveUnstored removes the local files of batches that were never stored
// with their tag: what a writer that died left. Run it before the writer
// starts, when no batch of this Mixer is in flight. A batch that did reach
// the store is fetched again by the next query that needs it.
func RemoveUnstored(dir string) (int, error) {
	removed := 0
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".parquet") || tagged(d.Name()) {
			return nil
		}
		if errRm := os.Remove(path); errRm == nil {
			removed++
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	return removed, err
}

// Cache keeps on this Mixer's disk the files of a Remote that queries need,
// and only those: before a query it fetches the hours the query covers.
type Cache struct {
	Dir    string // the store's telemetry directory
	Remote Remote
	Log    *slog.Logger

	mu      sync.Mutex
	settled map[string]bool // hours that no longer change, synced once
}

// settleAfter is how old an hour must be before its files stop changing: no
// batch of it is committed again after that.
const settleAfter = 2 * time.Hour

// maxSyncHours bounds the hours one query fetches.
const maxSyncHours = 24 * 31

// Sync fetches the files of kind for the hours from since to until (now when
// zero), and removes the copies here that the store replaced or removed.
func (c *Cache) Sync(ctx context.Context, kind string, since, until time.Time) error {
	if until.IsZero() {
		until = time.Now()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.settled == nil {
		c.settled = map[string]bool{}
	}
	h := since.UTC().Truncate(time.Hour)
	if earliest := until.UTC().Add(-maxSyncHours * time.Hour); h.Before(earliest) {
		h = earliest.Truncate(time.Hour)
	}
	for ; !h.After(until); h = h.Add(time.Hour) {
		hour := kind + "/" + h.Format("2006/01/02/15") + "/"
		if c.settled[hour] {
			continue
		}
		if err := c.syncHour(ctx, hour); err != nil {
			return fmt.Errorf("fetch the telemetry of %s: %w", strings.TrimSuffix(hour, "/"), err)
		}
		if time.Since(h) > settleAfter+time.Hour {
			c.settled[hour] = true
		}
	}
	return nil
}

func (c *Cache) syncHour(ctx context.Context, hour string) error {
	objs, err := c.Remote.List(ctx, hour)
	if err != nil {
		return err
	}
	dir := filepath.Join(c.Dir, filepath.FromSlash(hour))
	want := map[string]RemoteObject{} // local tagged name -> object
	for _, o := range objs {
		want[filepath.Base(taggedPath(o.Key, o.Tag))] = o
	}
	entries, _ := os.ReadDir(dir)
	have := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".parquet") {
			continue
		}
		if _, ok := want[e.Name()]; ok {
			have[e.Name()] = true
			continue
		}
		if !tagged(e.Name()) {
			// A batch the holder here is storing: it gets its tag once
			// stored. One left by a process that died is removed at start
			// (RemoveUnstored).
			continue
		}
		// Replaced or removed in the store: this copy is not the store's.
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
	for name, o := range want {
		if have[name] {
			continue
		}
		if err = os.MkdirAll(dir, 0o750); err != nil {
			return err
		}
		tmp := filepath.Join(dir, "."+name+".part")
		if err = c.Remote.Get(ctx, o.Key, tmp); err != nil {
			_ = os.Remove(tmp)
			return err
		}
		if err = os.Rename(tmp, filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return nil
}

// SweepRemote removes the files of a Remote older than retention, from the
// hours between retention and retention plus window ago. The holder runs it,
// often enough that window covers the time since the last run.
func SweepRemote(ctx context.Context, r Remote, retention, window time.Duration, log *slog.Logger) {
	if retention <= 0 {
		return
	}
	end := time.Now().UTC().Add(-retention).Truncate(time.Hour)
	removed := 0
	for _, kind := range []string{"logs", "metrics", "spans"} {
		for h := end.Add(-window); h.Before(end); h = h.Add(time.Hour) {
			objs, err := r.List(ctx, kind+"/"+h.Format("2006/01/02/15")+"/")
			if err != nil {
				log.Warn("telemetry store: retention sweep cannot list", "error", err)
				return
			}
			for _, o := range objs {
				if err = r.Delete(ctx, o.Key); err != nil && !errors.Is(err, jetstream.ErrObjectNotFound) {
					log.Warn("telemetry store: retention sweep cannot remove a file", "key", o.Key, "error", err)
					continue
				}
				removed++
			}
		}
	}
	if removed > 0 {
		log.Info("telemetry store: retention sweep removed old files", "files", removed, "older_than", end)
	}
}
