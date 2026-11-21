// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package kvstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

var (
	// errKeyNotFound means the key has no live value.
	errKeyNotFound = errors.New("kvstore: key not found")
	// errConflict means a create found the key already present, or an update
	// or delete found a revision other than the one it was given.
	errConflict = errors.New("kvstore: revision conflict")
)

// Bucket is the part of a key-value bucket the registry uses. Every write
// that must not overwrite a concurrent one is a Create (the key must be
// absent) or an Update (the key must still be at the revision read).
type Bucket interface {
	Get(ctx context.Context, key string) (value []byte, revision uint64, err error)
	Create(ctx context.Context, key string, value []byte) (uint64, error)
	Update(ctx context.Context, key string, value []byte, revision uint64) (uint64, error)
	Put(ctx context.Context, key string, value []byte) (uint64, error)
	// Delete removes key. A non-zero revision makes it conditional.
	Delete(ctx context.Context, key string, revision uint64) error
	// Keys lists the live keys that start with prefix.
	Keys(ctx context.Context, prefix string) ([]string, error)
}

// jsBucket adapts a JetStream key-value bucket. js reaches the bucket's
// stream, which Keys reads instead of a consumer.
type jsBucket struct {
	js jetstream.JetStream
	kv jetstream.KeyValue
}

// NewJetStreamBucket wraps a JetStream key-value bucket.
func NewJetStreamBucket(js jetstream.JetStream, kv jetstream.KeyValue) Bucket {
	return &jsBucket{js: js, kv: kv}
}

func mapJSError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, jetstream.ErrKeyNotFound) || errors.Is(err, jetstream.ErrKeyDeleted) {
		return errKeyNotFound
	}
	// A create on a present key and an update or delete on a moved revision
	// both come back as the stream's wrong-last-sequence error, which
	// ErrKeyExists matches by code.
	if errors.Is(err, jetstream.ErrKeyExists) {
		return errConflict
	}
	var apiErr *jetstream.APIError
	if errors.As(err, &apiErr) && (apiErr.ErrorCode == jetstream.JSErrCodeStreamWrongLastSequence ||
		apiErr.ErrorCode == jetstream.JSErrCodeStreamWrongLastSequenceConstant) {
		return errConflict
	}
	return err
}

// Get reads a key from the stream leader, and from a replica only when the
// cluster cannot answer through its leader. It does not use the KV client's
// Get, which is a single direct get.
//
//   - STREAM.MSG.GET is answered by the stream leader, so a read sees the
//     write before it. A direct get is answered by any replica, which can be
//     behind: a key written a moment ago can read as missing, and a Rack's
//     registration, which writes and then reads back, failed on it. Over a
//     route to a member cut off from the cluster (which counts as up until
//     its pings run out) a direct get can also be lost.
//   - While the cluster elects a metadata leader, for example right after a
//     heal, every member answers STREAM.MSG.GET with "cluster not available".
//     On the first such answer Get also sends a direct get, which replicas
//     still answer, and returns whichever valid answer comes first.
//
// A direct answer can be a little behind. Every registry write is
// revision-checked, so a stale read makes a write conflict, never overwrite.
func (b *jsBucket) Get(ctx context.Context, key string) ([]byte, uint64, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, b.js.Options().DefaultTimeout)
		defer cancel()
	}
	subject := "$KV." + b.kv.Bucket() + "." + key
	req, err := json.Marshal(struct {
		LastBySubject string `json:"last_by_subj"`
	}{subject})
	if err != nil {
		return nil, 0, err
	}
	nc := b.js.Conn()
	inbox := nc.NewRespInbox()
	sub, err := nc.SubscribeSync(inbox)
	if err != nil {
		return nil, 0, fmt.Errorf("kvstore: get %s: %w", key, err)
	}
	defer func() { _ = sub.Unsubscribe() }()
	prefix := apiPrefix(b.js.Options())
	stream := "KV_" + b.kv.Bucket()
	if err := nc.PublishRequest(prefix+"STREAM.MSG.GET."+stream, inbox, req); err != nil {
		return nil, 0, fmt.Errorf("kvstore: get %s: %w", key, err)
	}
	var unavailable error
	for {
		msg, err := sub.NextMsgWithContext(ctx)
		if err != nil {
			if unavailable != nil {
				err = unavailable
			}
			return nil, 0, fmt.Errorf("kvstore: get %s: %w", key, err)
		}
		value, rev, done, err := readAnswer(msg)
		if !done {
			if err != nil && unavailable == nil {
				unavailable = err
				if errDirect := nc.PublishRequest(prefix+"DIRECT.GET."+stream+"."+subject, inbox, nil); errDirect != nil {
					return nil, 0, fmt.Errorf("kvstore: get %s: %w", key, errDirect)
				}
			}
			continue
		}
		if err != nil && !errors.Is(err, errKeyNotFound) {
			err = fmt.Errorf("kvstore: get %s: %w", key, err)
		}
		return value, rev, err
	}
}

// readAnswer reads one answer to Get. done is false for an answer that says
// nothing about the key: no responders, or "cluster not available" (returned
// as err so the caller can report it if nothing better arrives).
func readAnswer(msg *nats.Msg) (value []byte, rev uint64, done bool, err error) {
	// A direct get answers with the stored message, its headers set by the
	// server, or with a status and no body.
	if status := msg.Header.Get("Status"); status != "" && len(msg.Data) == 0 {
		if status == "404" {
			return nil, 0, true, errKeyNotFound
		}
		return nil, 0, false, nil // 503 no responders, 408 and the like
	}
	if seq := msg.Header.Get(jetstream.SequenceHeader); seq != "" {
		if op := msg.Header.Get(kvOperationHeader); op == kvOperationDelete || op == kvOperationPurge {
			return nil, 0, true, errKeyNotFound
		}
		rev, errSeq := strconv.ParseUint(seq, 10, 64)
		if errSeq != nil {
			return nil, 0, true, errSeq
		}
		return msg.Data, rev, true, nil
	}
	var resp struct {
		Message *struct {
			Header   []byte `json:"hdrs"`
			Data     []byte `json:"data"`
			Sequence uint64 `json:"seq"`
		} `json:"message"`
		Error *jetstream.APIError `json:"error"`
	}
	if err := json.Unmarshal(msg.Data, &resp); err != nil {
		return nil, 0, true, err
	}
	switch {
	case resp.Error != nil && resp.Error.Code == http.StatusServiceUnavailable:
		return nil, 0, false, resp.Error
	case resp.Error != nil && resp.Error.ErrorCode == jetstream.JSErrCodeMessageNotFound:
		return nil, 0, true, errKeyNotFound
	case resp.Error != nil:
		return nil, 0, true, resp.Error
	case resp.Message == nil:
		return nil, 0, true, errors.New("empty answer")
	}
	// A delete or purge leaves a marker message with no value.
	if len(resp.Message.Header) > 0 {
		hdr, errHdr := nats.DecodeHeadersMsg(resp.Message.Header)
		if errHdr != nil {
			return nil, 0, true, errHdr
		}
		if op := hdr.Get(kvOperationHeader); op == kvOperationDelete || op == kvOperationPurge {
			return nil, 0, true, errKeyNotFound
		}
	}
	return resp.Message.Data, resp.Message.Sequence, true, nil
}

// apiRequest sends a JetStream API request and decodes into out the first
// answer that is not "cluster not available". Every member gets the request,
// and the stream leader answers it. A member that sees no metadata leader,
// for example one that has just rejoined after a partition, answers at once
// with that error, and the first answer would win over the leader's. Such an
// answer is kept and returned only if no other one arrives before ctx ends.
// Without a deadline on ctx the handle's default timeout applies.
func (b *jsBucket) apiRequest(ctx context.Context, api string, req []byte, out any) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, b.js.Options().DefaultTimeout)
		defer cancel()
	}
	nc := b.js.Conn()
	inbox := nc.NewRespInbox()
	sub, err := nc.SubscribeSync(inbox)
	if err != nil {
		return err
	}
	defer func() { _ = sub.Unsubscribe() }()
	if err := nc.PublishRequest(apiPrefix(b.js.Options())+api, inbox, req); err != nil {
		return err
	}
	var unavailable error
	for {
		msg, err := sub.NextMsgWithContext(ctx)
		if err != nil {
			if unavailable != nil {
				return unavailable
			}
			return err
		}
		if len(msg.Data) == 0 && msg.Header.Get("Status") == "503" {
			return nats.ErrNoResponders
		}
		var probe struct {
			Error *jetstream.APIError `json:"error"`
		}
		if err := json.Unmarshal(msg.Data, &probe); err != nil {
			return err
		}
		if probe.Error != nil && probe.Error.Code == http.StatusServiceUnavailable {
			unavailable = probe.Error
			continue
		}
		return json.Unmarshal(msg.Data, out)
	}
}

// The header and values the KV client writes on a delete or purge marker.
const (
	kvOperationHeader = "KV-Operation"
	kvOperationDelete = "DEL"
	kvOperationPurge  = "PURGE"
)

// apiPrefix is the subject prefix of the JetStream API the handle talks to.
func apiPrefix(o jetstream.JetStreamOptions) string {
	switch {
	case o.APIPrefix != "":
		return strings.TrimSuffix(o.APIPrefix, ".") + "."
	case o.Domain != "":
		return "$JS." + o.Domain + ".API."
	default:
		return jetstream.DefaultAPIPrefix
	}
}

func (b *jsBucket) Create(ctx context.Context, key string, value []byte) (uint64, error) {
	rev, err := b.kv.Create(ctx, key, value)
	return rev, mapJSError(err)
}

func (b *jsBucket) Update(ctx context.Context, key string, value []byte, revision uint64) (uint64, error) {
	rev, err := b.kv.Update(ctx, key, value, revision)
	return rev, mapJSError(err)
}

func (b *jsBucket) Put(ctx context.Context, key string, value []byte) (uint64, error) {
	rev, err := b.kv.Put(ctx, key, value)
	return rev, mapJSError(err)
}

func (b *jsBucket) Delete(ctx context.Context, key string, revision uint64) error {
	if revision == 0 {
		return mapJSError(b.kv.Delete(ctx, key))
	}
	return mapJSError(b.kv.Delete(ctx, key, jetstream.LastRevision(revision)))
}

// Keys reads the subjects of the bucket's stream, then keeps the keys that
// still hold a value. It does not use the KV key lister: that one creates an
// ephemeral consumer, and in a cluster with a member cut off the consumer can
// be placed on that member and never start, so the listing hangs until ctx
// ends. The subject index is answered by the stream leader, which is on the
// majority side. A deleted key keeps a delete marker in the index, so each
// key is read to drop those.
func (b *jsBucket) Keys(ctx context.Context, prefix string) ([]string, error) {
	subjectPrefix := "$KV." + b.kv.Bucket() + "."
	var subjects []string
	// The server pages a long subject list; offset asks for the next page.
	for {
		req, err := json.Marshal(struct {
			Offset        int    `json:"offset"`
			SubjectFilter string `json:"subjects_filter"`
		}{len(subjects), subjectPrefix + prefix + ">"})
		if err != nil {
			return nil, err
		}
		var resp struct {
			State struct {
				Subjects map[string]uint64 `json:"subjects"`
			} `json:"state"`
			Total int                 `json:"total"`
			Error *jetstream.APIError `json:"error"`
		}
		if err := b.apiRequest(ctx, "STREAM.INFO.KV_"+b.kv.Bucket(), req, &resp); err != nil {
			return nil, fmt.Errorf("kvstore: list keys %q: %w", prefix, err)
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("kvstore: list keys %q: %w", prefix, resp.Error)
		}
		for subject := range resp.State.Subjects {
			subjects = append(subjects, subject)
		}
		if len(resp.State.Subjects) == 0 || len(subjects) >= resp.Total {
			break
		}
	}
	keys := make([]string, 0, len(subjects))
	for _, subject := range subjects {
		key := strings.TrimPrefix(subject, subjectPrefix)
		if _, _, errGet := b.Get(ctx, key); errGet != nil {
			if errors.Is(errGet, errKeyNotFound) {
				continue // deleted: only its marker is left
			}
			return nil, fmt.Errorf("kvstore: list keys %q: read %s: %w", prefix, key, errGet)
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys, nil
}

// MemoryBucket is an in-memory Bucket with the same create, update and
// delete semantics as a JetStream bucket, and an optional max age. It backs
// tests that need a registry without a NATS server.
type MemoryBucket struct {
	mu     sync.Mutex
	rev    uint64
	maxAge time.Duration
	now    func() time.Time
	items  map[string]memItem
}

type memItem struct {
	value   []byte
	rev     uint64
	written time.Time
}

// NewMemoryBucket returns an empty in-memory bucket. A positive maxAge
// expires a key that long after its last write, as a bucket TTL does.
func NewMemoryBucket(maxAge time.Duration) *MemoryBucket {
	return &MemoryBucket{maxAge: maxAge, now: time.Now, items: map[string]memItem{}}
}

// SetClock replaces the bucket's clock. For tests of expiry.
func (m *MemoryBucket) SetClock(now func() time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = now
}

func (m *MemoryBucket) live(key string) (memItem, bool) {
	it, ok := m.items[key]
	if !ok {
		return memItem{}, false
	}
	if m.maxAge > 0 && m.now().Sub(it.written) >= m.maxAge {
		delete(m.items, key)
		return memItem{}, false
	}
	return it, true
}

func (m *MemoryBucket) write(key string, value []byte) uint64 {
	m.rev++
	m.items[key] = memItem{value: append([]byte(nil), value...), rev: m.rev, written: m.now()}
	return m.rev
}

func (m *MemoryBucket) Get(_ context.Context, key string) ([]byte, uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	it, ok := m.live(key)
	if !ok {
		return nil, 0, errKeyNotFound
	}
	return append([]byte(nil), it.value...), it.rev, nil
}

func (m *MemoryBucket) Create(_ context.Context, key string, value []byte) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.live(key); ok {
		return 0, errConflict
	}
	return m.write(key, value), nil
}

func (m *MemoryBucket) Update(_ context.Context, key string, value []byte, revision uint64) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	it, ok := m.live(key)
	if !ok || it.rev != revision {
		return 0, errConflict
	}
	return m.write(key, value), nil
}

func (m *MemoryBucket) Put(_ context.Context, key string, value []byte) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.write(key, value), nil
}

func (m *MemoryBucket) Delete(_ context.Context, key string, revision uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	it, ok := m.live(key)
	if revision != 0 && (!ok || it.rev != revision) {
		return errConflict
	}
	delete(m.items, key)
	return nil
}

func (m *MemoryBucket) Keys(_ context.Context, prefix string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var keys []string
	for k := range m.items {
		if _, ok := m.live(k); ok && strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys, nil
}
