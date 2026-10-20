// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

// Package kvstore keeps the Mixer's registry in NATS JetStream key-value
// buckets: Rack, Mixer and Snake records, the name index that keeps names
// unique, scenario files and the active-scenario pointer. A second bucket
// with a max age holds liveness, so a Rack whose heartbeats stop drops out
// of it on its own.
//
// Every write that must not lose a concurrent one is a create (the key must
// be absent) or an update checked against the revision it read. A conflict
// is read again and retried a bounded number of times. This is the fence a
// future write lease relies on; with one Mixer it also closes the window a
// read-then-write left open.
package kvstore

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/registry"
	"github.com/jaab-tech/fluxrig/pkg/security"
)

// Entity types kept in the registry bucket. They match idgen's entity types.
const (
	typeMixer uint8 = 2
	typeRack  uint8 = 4
	typeSnake uint8 = 9
)

const activeKey = "active"

var (
	// ErrScenarioTooLarge is returned when a scenario file exceeds the
	// registry bucket's largest value.
	ErrScenarioTooLarge = errors.New("scenario larger than registry.max_value_size")
	// errIdentityMismatch is returned when a known MachineID presents the
	// wrong secret.
	errIdentityMismatch = errors.New("identity mismatch: incorrect secret for existing MachineID")
)

// Telemetry is the DuckDB side the registry still delegates to: telemetry
// queries and the local topology rows.
type Telemetry interface {
	QueryLogs(ctx context.Context, q registry.LogQuery) ([]registry.LogEntry, error)
	QueryMetrics(ctx context.Context, q registry.MetricQuery) ([]registry.MetricEntry, error)
	ClearScenarioEntities(ctx context.Context) error
}

// Config holds the registry's limits.
type Config struct {
	// MaxValueSize bounds one record; in practice it bounds a scenario file.
	MaxValueSize int
	// WriteRetries is how many times a write that hit a revision conflict
	// is read again and retried before it fails.
	WriteRetries int
}

// Store is the KV-backed registry. It implements registry.Registry.
type Store struct {
	reg     Bucket
	live    Bucket
	tel     Telemetry
	cfg     Config
	mixerID uuid.UUID
	log     *slog.Logger
	now     func() time.Time
	// newUUID mints MachineIDs and entity IDs. A failure is returned, never
	// replaced by the nil ID: two records with a nil ID would collide.
	newUUID func() (uuid.UUID, error)

	mu              sync.RWMutex
	autoAdopt       bool
	bootstrapSecret string

	// Heartbeat stats and config are overwritten every heartbeat and mean
	// nothing after a restart, so each Mixer keeps its own in memory rather
	// than replicating them.
	statsMu sync.Mutex
	stats   map[string]heartbeatState
}

type heartbeatState struct {
	stats  map[string]any
	config map[string]any
}

var _ registry.Registry = (*Store)(nil)

// record is one entity in the registry bucket.
type record struct {
	EntityID    uuid.UUID      `json:"entity_id"`
	TypeID      uint8          `json:"type_id"`
	MachineID   uuid.UUID      `json:"machine_id"`
	MixerID     uuid.UUID      `json:"mixer_id"`
	Name        string         `json:"name"`
	Status      string         `json:"status"`
	SecretHash  string         `json:"secret_hash,omitempty"`
	Version     string         `json:"version"`
	IP          string         `json:"ip"`
	Port        int            `json:"port"`
	FirstSeen   time.Time      `json:"first_seen"`
	UpdatedAt   time.Time      `json:"updated_at"`
	UpdateCount int            `json:"update_count"`
	Config      map[string]any `json:"config,omitempty"`
	Attributes  map[string]any `json:"attributes,omitempty"`
}

// nameRef is the value of a name key: which entity holds the name.
type nameRef struct {
	TypeID    uint8     `json:"type_id"`
	MachineID uuid.UUID `json:"machine_id"`
}

// presence is the value of a liveness key.
type presence struct {
	LastSeen time.Time `json:"last_seen"`
	MixerID  uuid.UUID `json:"mixer_id"`
}

// New returns a registry over reg (records, names, scenarios, pointer) and
// live (liveness, with a max age). tel may be nil when no telemetry store
// exists; its methods then return an error.
func New(log *slog.Logger, reg, live Bucket, tel Telemetry, mixerID uuid.UUID, cfg Config) *Store {
	if cfg.WriteRetries < 0 {
		cfg.WriteRetries = 0
	}
	return &Store{
		reg:     reg,
		live:    live,
		tel:     tel,
		cfg:     cfg,
		mixerID: mixerID,
		log:     log.With("component", "REGISTRY"),
		now:     func() time.Time { return time.Now().UTC() },
		newUUID: uuid.NewV7,
		stats:   map[string]heartbeatState{},
	}
}

func entityKey(typeID uint8, id uuid.UUID) string {
	return fmt.Sprintf("entity.%d.%s", typeID, id)
}

// Names and scenario names are arbitrary text; a key allows only a narrow
// character set, so they are stored base64url-encoded.
func nameKey(name string) string {
	return "name." + base64.RawURLEncoding.EncodeToString([]byte(name))
}

func scenarioKey(name string) string {
	return "scenario." + base64.RawURLEncoding.EncodeToString([]byte(name))
}

// retry runs fn until it returns anything but a revision conflict, at most
// WriteRetries extra times.
func (s *Store) retry(op string, fn func() error) error {
	var err error
	for attempt := 0; attempt <= s.cfg.WriteRetries; attempt++ {
		if err = fn(); !errors.Is(err, errConflict) {
			return err
		}
	}
	return fmt.Errorf("kvstore: %s: still conflicting after %d retries: %w", op, s.cfg.WriteRetries, err)
}

func (s *Store) getRecord(ctx context.Context, typeID uint8, id uuid.UUID) (*record, uint64, error) {
	raw, rev, err := s.reg.Get(ctx, entityKey(typeID, id))
	if err != nil {
		if errors.Is(err, errKeyNotFound) {
			return nil, 0, registry.ErrNotFound
		}
		return nil, 0, err
	}
	var r record
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, 0, fmt.Errorf("kvstore: decode %s: %w", entityKey(typeID, id), err)
	}
	return &r, rev, nil
}

// encode marshals a value the store built. Every map that came from the wire
// (a Hello's config, a Snake's attributes) passes through jsonSafe first, and
// the store's own types hold only strings, numbers, times and UUIDs, so this
// cannot fail.
func encode(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// jsonSafe converts what CBOR decoding produces into what JSON can encode:
// maps keyed by interface{} become maps keyed by string, slices are walked,
// and NaN or an infinity (which JSON has no form for) becomes null. A value
// of any other type that JSON refuses becomes its printed form.
func jsonSafe(v any) any {
	switch t := v.(type) {
	case nil, bool, string, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return t
	case float32:
		return jsonSafe(float64(t))
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return nil
		}
		return t
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = jsonSafe(val)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[fmt.Sprint(k)] = jsonSafe(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = jsonSafe(val)
		}
		return out
	default:
		if _, err := json.Marshal(t); err == nil {
			return t
		}
		return fmt.Sprint(t)
	}
}

// jsonSafeMap applies jsonSafe to a map, keeping nil as nil.
func jsonSafeMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	return jsonSafe(m).(map[string]any)
}

// entityID mints an entity ID: a UUID v7 carrying a hint of the MachineID
// and the entity type.
func (s *Store) entityID(typeID uint8, machineID uuid.UUID) (uuid.UUID, error) {
	eid, err := s.newUUID()
	if err != nil {
		return uuid.Nil, fmt.Errorf("kvstore: mint an entity ID: %w", err)
	}
	copy(eid[9:13], machineID[12:16]) // MachineID hint
	eid[13] = typeID                  // entity type
	return eid, nil
}

func generateSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// claimName points name at ref. It succeeds when the name is free, already
// ref's, or held by an orphan key: one whose entity is gone or no longer
// carries that name, left by a rename or removal interrupted between its
// steps. Any other holder is a name conflict.
//
// Every flow writes the entity record, carrying the name, before it claims
// the name, and undoes the record when the claim fails. That ordering is
// what makes the orphan test safe: a name key whose holder record lacks the
// name cannot belong to a registration still in progress.
func (s *Store) claimName(ctx context.Context, name string, ref nameRef) error {
	if name == "" {
		return nil
	}
	key := nameKey(name)
	if _, err := s.reg.Create(ctx, key, encode(ref)); !errors.Is(err, errConflict) {
		return err
	}
	raw, rev, err := s.reg.Get(ctx, key)
	if errors.Is(err, errKeyNotFound) {
		return errConflict // deleted between the two calls: retry
	}
	if err != nil {
		return err
	}
	var held nameRef
	_ = json.Unmarshal(raw, &held)
	if held == ref {
		return nil
	}
	if holder, _, errGet := s.getRecord(ctx, held.TypeID, held.MachineID); errGet == nil && holder.Name == name {
		return registry.ErrNameConflict
	} else if errGet != nil && !errors.Is(errGet, registry.ErrNotFound) {
		return errGet
	}
	_, err = s.reg.Update(ctx, key, encode(ref), rev)
	return err
}

// moveName repoints name from one entity to another, only if it still
// points at from.
func (s *Store) moveName(ctx context.Context, name string, from, to nameRef) error {
	raw, rev, err := s.reg.Get(ctx, nameKey(name))
	if errors.Is(err, errKeyNotFound) {
		_, err = s.reg.Create(ctx, nameKey(name), encode(to))
		return err
	}
	if err != nil {
		return err
	}
	var held nameRef
	if json.Unmarshal(raw, &held) != nil || held != from {
		return errConflict
	}
	_, err = s.reg.Update(ctx, nameKey(name), encode(to), rev)
	return err
}

// releaseName deletes name's key if it still points at ref.
func (s *Store) releaseName(ctx context.Context, name string, ref nameRef) {
	if name == "" {
		return
	}
	raw, rev, err := s.reg.Get(ctx, nameKey(name))
	if err != nil {
		return
	}
	var held nameRef
	if json.Unmarshal(raw, &held) == nil && held == ref {
		_ = s.reg.Delete(ctx, nameKey(name), rev)
	}
}

// resolveName returns the entity that holds name.
func (s *Store) resolveName(ctx context.Context, name string) (*record, error) {
	raw, _, err := s.reg.Get(ctx, nameKey(name))
	if errors.Is(err, errKeyNotFound) {
		return nil, registry.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var ref nameRef
	if errDec := json.Unmarshal(raw, &ref); errDec != nil {
		return nil, errDec
	}
	rec, _, err := s.getRecord(ctx, ref.TypeID, ref.MachineID)
	if err != nil {
		return nil, err
	}
	if rec.Name != name {
		return nil, registry.ErrNotFound // orphan key
	}
	return rec, nil
}

func (s *Store) SetAutoAdopt(enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.autoAdopt = enabled
}

func (s *Store) SetBootstrapSecret(secret string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bootstrapSecret = secret
}

func (s *Store) Register(ctx context.Context, machineID uuid.UUID, name, secret, ip string, port int, version string, config map[string]any, mixerID uuid.UUID) (*registry.Rack, error) {
	return s.RegisterEntity(ctx, typeRack, machineID, name, secret, ip, port, version, config, nil, mixerID)
}

// RegisterEntity handles a Hello. A known MachineID must present its own
// secret. An unknown MachineID either adopts a name whose holder has the
// same secret, or registers fresh with the bootstrap secret and receives a
// new secret of its own: the one time the plaintext leaves the registry.
func (s *Store) RegisterEntity(ctx context.Context, typeID uint8, machineID uuid.UUID, name, secret, ip string, port int, version string, config, attrs map[string]any, mixerID uuid.UUID) (*registry.Rack, error) {
	config, attrs = jsonSafeMap(config), jsonSafeMap(attrs)
	var out *registry.Rack
	err := s.retry("register", func() error {
		var errStep error
		out, errStep = s.registerOnce(ctx, typeID, machineID, name, secret, ip, port, version, config, attrs, mixerID)
		return errStep
	})
	return out, err
}

func (s *Store) registerOnce(ctx context.Context, typeID uint8, machineID uuid.UUID, name, secret, ip string, port int, version string, config, attrs map[string]any, mixerID uuid.UUID) (*registry.Rack, error) {
	now := s.now()

	// 1. Known MachineID: the secret must match. The approved name is kept;
	// the Hello's name does not overwrite it.
	if machineID != uuid.Nil {
		rec, rev, err := s.getRecord(ctx, typeID, machineID)
		if err == nil {
			if !registry.SecretMatches(rec.SecretHash, secret) {
				return nil, errIdentityMismatch
			}
			rec.IP, rec.Port, rec.Version = ip, port, version
			rec.Config, rec.Attributes = config, attrs
			rec.UpdatedAt = now
			rec.UpdateCount++
			if _, errUpd := s.reg.Update(ctx, entityKey(typeID, machineID), encode(rec), rev); errUpd != nil {
				return nil, errUpd
			}
			return s.toRack(ctx, rec), nil
		}
		if !errors.Is(err, registry.ErrNotFound) {
			return nil, err
		}
	}

	// 2. Unknown MachineID, name taken: adoption by the holder's secret,
	// otherwise a conflict.
	if name != "" {
		holder, err := s.resolveName(ctx, name)
		if err == nil {
			if holder.TypeID != typeID || !registry.SecretMatches(holder.SecretHash, secret) {
				s.log.Warn("Registration denied: name conflict", "name", name, "requested_type", typeID, "existing_type", holder.TypeID)
				return nil, registry.ErrNameConflict
			}
			return s.adopt(ctx, holder, machineID, ip, port, version, config, attrs, now)
		}
		if !errors.Is(err, registry.ErrNotFound) {
			return nil, err
		}
	}

	// 3. Fresh registration: the bootstrap secret gates entry.
	s.mu.RLock()
	bootstrap, autoAdopt := s.bootstrapSecret, s.autoAdopt
	s.mu.RUnlock()
	if !security.SecretsEqual(bootstrap, secret) {
		s.log.Warn("Registration denied: bootstrap secret mismatch", "name", name, "machine_id", machineID)
		return nil, registry.ErrBootstrapSecretMismatch
	}
	if machineID == uuid.Nil {
		var errID error
		if machineID, errID = s.newUUID(); errID != nil {
			return nil, fmt.Errorf("kvstore: mint a MachineID: %w", errID)
		}
	}
	issued, err := generateSecret()
	if err != nil {
		return nil, fmt.Errorf("kvstore: issue a secret: %w", err)
	}
	status := "pending"
	if autoAdopt {
		status = "active"
	}
	eid, err := s.entityID(typeID, machineID)
	if err != nil {
		return nil, err
	}
	rec := &record{
		EntityID: eid, TypeID: typeID, MachineID: machineID, MixerID: mixerID,
		Name: name, Status: status, SecretHash: registry.HashSecret(issued),
		Version: version, IP: ip, Port: port, FirstSeen: now, UpdatedAt: now,
		Config: config, Attributes: attrs,
	}
	rev, err := s.reg.Create(ctx, entityKey(typeID, machineID), encode(rec))
	if err != nil {
		return nil, err
	}
	if err := s.claimName(ctx, name, nameRef{TypeID: typeID, MachineID: machineID}); err != nil {
		_ = s.reg.Delete(ctx, entityKey(typeID, machineID), rev)
		return nil, err
	}
	rack := s.toRack(ctx, rec)
	rack.Secret = issued
	return rack, nil
}

// adopt moves holder's record to a new MachineID: a Rack that lost its
// passport but kept its secret returns under a new machine identity.
func (s *Store) adopt(ctx context.Context, holder *record, machineID uuid.UUID, ip string, port int, version string, config, attrs map[string]any, now time.Time) (*registry.Rack, error) {
	if machineID == uuid.Nil || machineID == holder.MachineID {
		return nil, errIdentityMismatch
	}
	oldKey := entityKey(holder.TypeID, holder.MachineID)
	_, oldRev, err := s.getRecord(ctx, holder.TypeID, holder.MachineID)
	if err != nil {
		return nil, err
	}
	s.log.Info("Adopting existing name via secret match", "name", holder.Name, "old_machine_id", holder.MachineID, "new_machine_id", machineID)
	moved := *holder
	moved.MachineID = machineID
	moved.IP, moved.Port, moved.Version = ip, port, version
	moved.Config, moved.Attributes = config, attrs
	moved.UpdatedAt = now
	moved.UpdateCount++
	newKey := entityKey(holder.TypeID, machineID)
	newRev, err := s.reg.Create(ctx, newKey, encode(&moved))
	if err != nil {
		return nil, err
	}
	// The name moves from the old identity to the new one, checked against
	// the revision that still names the old one.
	oldRef := nameRef{TypeID: holder.TypeID, MachineID: holder.MachineID}
	newRef := nameRef{TypeID: holder.TypeID, MachineID: machineID}
	if err := s.moveName(ctx, holder.Name, oldRef, newRef); err != nil {
		_ = s.reg.Delete(ctx, newKey, newRev)
		return nil, err
	}
	_ = s.reg.Delete(ctx, oldKey, oldRev)
	return s.toRack(ctx, &moved), nil
}

// UpsertSelf records the Mixer's own entities (the Mixer, its embedded
// Snake). They carry no secret and claim no name: they are not Racks, and
// every Mixer's Snake shares one name.
func (s *Store) UpsertSelf(ctx context.Context, typeID uint8, machineID uuid.UUID, name, ip string, port int, version string, attrs map[string]any) error {
	attrs = jsonSafeMap(attrs)
	return s.retry("upsert self", func() error {
		now := s.now()
		rec, rev, err := s.getRecord(ctx, typeID, machineID)
		if errors.Is(err, registry.ErrNotFound) {
			eid, errID := s.entityID(typeID, machineID)
			if errID != nil {
				return errID
			}
			rec = &record{EntityID: eid, TypeID: typeID, MachineID: machineID, MixerID: s.mixerID, FirstSeen: now, Status: "active"}
			rec.Name, rec.IP, rec.Port, rec.Version, rec.Attributes, rec.UpdatedAt = name, ip, port, version, attrs, now
			_, err = s.reg.Create(ctx, entityKey(typeID, machineID), encode(rec))
			return err
		}
		if err != nil {
			return err
		}
		rec.Name, rec.IP, rec.Port, rec.Version, rec.Attributes, rec.UpdatedAt = name, ip, port, version, attrs, now
		rec.UpdateCount++
		_, err = s.reg.Update(ctx, entityKey(typeID, machineID), encode(rec), rev)
		return err
	})
}

func (s *Store) Approve(ctx context.Context, id uuid.UUID, newName string) (*registry.Rack, error) {
	var out *registry.Rack
	err := s.retry("approve", func() error {
		rec, rev, err := s.getRecord(ctx, typeRack, id)
		if err != nil {
			return err
		}
		orig := *rec
		oldName := rec.Name
		ref := nameRef{TypeID: typeRack, MachineID: id}
		if newName != "" && newName != oldName {
			// Refuse a name another entity holds before writing anything.
			// The claim below still decides a race.
			if holder, errName := s.resolveName(ctx, newName); errName == nil && holder.MachineID != id {
				return registry.ErrNameConflict
			}
			rec.Name = newName
		}
		rec.Status = "active"
		rec.UpdatedAt = s.now()
		newRev, err := s.reg.Update(ctx, entityKey(typeRack, id), encode(rec), rev)
		if err != nil {
			return err
		}
		if rec.Name != oldName {
			if errClaim := s.claimName(ctx, rec.Name, ref); errClaim != nil {
				// Another entity won the name: undo the whole approval, so a
				// refused rename leaves the Rack as it was.
				_, _ = s.reg.Update(ctx, entityKey(typeRack, id), encode(&orig), newRev)
				return errClaim
			}
			s.releaseName(ctx, oldName, ref)
		}
		out = s.toRack(ctx, rec)
		return nil
	})
	return out, err
}

func (s *Store) List(ctx context.Context, status string) ([]*registry.Rack, error) {
	keys, err := s.reg.Keys(ctx, fmt.Sprintf("entity.%d.", typeRack))
	if err != nil {
		return nil, err
	}
	var racks []*registry.Rack
	for _, k := range keys {
		id, errParse := uuid.Parse(strings.TrimPrefix(k, fmt.Sprintf("entity.%d.", typeRack)))
		if errParse != nil {
			continue
		}
		rec, _, errGet := s.getRecord(ctx, typeRack, id)
		if errGet != nil {
			continue // removed between the listing and the read
		}
		if status != "" && rec.Status != status {
			continue
		}
		racks = append(racks, s.toRack(ctx, rec))
	}
	return racks, nil
}

func (s *Store) Get(ctx context.Context, id uuid.UUID) (*registry.Rack, error) {
	return s.GetEntity(ctx, typeRack, id)
}

// GetEntity returns one entity of any type.
func (s *Store) GetEntity(ctx context.Context, typeID uint8, id uuid.UUID) (*registry.Rack, error) {
	rec, _, err := s.getRecord(ctx, typeID, id)
	if err != nil {
		return nil, err
	}
	return s.toRack(ctx, rec), nil
}

// GetRackByName returns the MachineID of the active Rack called name. A
// pending Rack reads as not found: scenario registration must never treat an
// unapproved Rack as a deploy target.
func (s *Store) GetRackByName(ctx context.Context, name string) (uuid.UUID, error) {
	rec, err := s.resolveName(ctx, name)
	if err != nil {
		return uuid.Nil, err
	}
	if rec.TypeID != typeRack || rec.Status != "active" {
		return uuid.Nil, registry.ErrNotFound
	}
	return rec.MachineID, nil
}

func (s *Store) Heartbeat(ctx context.Context, id uuid.UUID, stats, config map[string]any) error {
	return s.HeartbeatEntity(ctx, typeRack, id, stats, config)
}

// HeartbeatEntity records presence in the liveness bucket, whose max age
// expires it, and keeps stats and config in this Mixer's memory.
func (s *Store) HeartbeatEntity(ctx context.Context, typeID uint8, id uuid.UUID, stats, config map[string]any) error {
	if _, _, err := s.getRecord(ctx, typeID, id); err != nil {
		return err
	}
	if _, err := s.live.Put(ctx, entityKey(typeID, id), encode(presence{LastSeen: s.now(), MixerID: s.mixerID})); err != nil {
		return fmt.Errorf("kvstore: record presence: %w", err)
	}
	// Sanitized like a record: the API encodes these as JSON, and a map
	// decoded from CBOR would make that encoding fail for the whole list.
	s.statsMu.Lock()
	s.stats[entityKey(typeID, id)] = heartbeatState{stats: jsonSafeMap(stats), config: jsonSafeMap(config)}
	s.statsMu.Unlock()
	return nil
}

func (s *Store) Remove(ctx context.Context, id uuid.UUID) error {
	return s.RemoveEntity(ctx, typeRack, id)
}

func (s *Store) RemoveByName(ctx context.Context, name string) error {
	rec, err := s.resolveName(ctx, name)
	if err != nil {
		return err
	}
	if rec.TypeID != typeRack {
		return registry.ErrNotFound
	}
	return s.RemoveEntity(ctx, typeRack, rec.MachineID)
}

func (s *Store) RemoveEntity(ctx context.Context, typeID uint8, id uuid.UUID) error {
	return s.retry("remove", func() error {
		rec, rev, err := s.getRecord(ctx, typeID, id)
		if err != nil {
			return err
		}
		if err := s.reg.Delete(ctx, entityKey(typeID, id), rev); err != nil {
			return err
		}
		s.releaseName(ctx, rec.Name, nameRef{TypeID: typeID, MachineID: id})
		_ = s.live.Delete(ctx, entityKey(typeID, id), 0)
		s.statsMu.Lock()
		delete(s.stats, entityKey(typeID, id))
		s.statsMu.Unlock()
		return nil
	})
}

func (s *Store) UpdateStatus(ctx context.Context, id uuid.UUID, status string) error {
	return s.UpdateStatusEntity(ctx, typeRack, id, status)
}

func (s *Store) UpdateStatusEntity(ctx context.Context, typeID uint8, id uuid.UUID, status string) error {
	return s.retry("update status", func() error {
		rec, rev, err := s.getRecord(ctx, typeID, id)
		if err != nil {
			return err
		}
		rec.Status = status
		rec.UpdatedAt = s.now()
		_, err = s.reg.Update(ctx, entityKey(typeID, id), encode(rec), rev)
		return err
	})
}

// toRack builds the API view: the record, overlaid with presence from the
// liveness bucket and this Mixer's last heartbeat stats and config.
func (s *Store) toRack(ctx context.Context, rec *record) *registry.Rack {
	r := &registry.Rack{
		MachineID: rec.MachineID, Name: rec.Name, Status: rec.Status, Version: rec.Version,
		IP: rec.IP, Port: rec.Port, SecretHash: rec.SecretHash,
		FirstSeen: rec.FirstSeen, LastSeen: rec.UpdatedAt, UpdateCount: rec.UpdateCount,
		Config: rec.Config,
	}
	if raw, _, err := s.live.Get(ctx, entityKey(rec.TypeID, rec.MachineID)); err == nil {
		var p presence
		if json.Unmarshal(raw, &p) == nil {
			r.Online = true
			if p.LastSeen.After(r.LastSeen) {
				r.LastSeen = p.LastSeen
			}
		}
	}
	s.statsMu.Lock()
	if hb, ok := s.stats[entityKey(rec.TypeID, rec.MachineID)]; ok {
		r.Stats = hb.stats
		if hb.config != nil {
			r.Config = hb.config
		}
	}
	s.statsMu.Unlock()
	return r
}

// Telemetry and topology, delegated to the local store.

var errNoTelemetry = errors.New("kvstore: no telemetry store")

func (s *Store) QueryLogs(ctx context.Context, q registry.LogQuery) ([]registry.LogEntry, error) {
	if s.tel == nil {
		return nil, errNoTelemetry
	}
	return s.tel.QueryLogs(ctx, q)
}

func (s *Store) QueryMetrics(ctx context.Context, q registry.MetricQuery) ([]registry.MetricEntry, error) {
	if s.tel == nil {
		return nil, errNoTelemetry
	}
	return s.tel.QueryMetrics(ctx, q)
}

func (s *Store) ClearScenarioEntities(ctx context.Context) error {
	if s.tel == nil {
		return errNoTelemetry
	}
	return s.tel.ClearScenarioEntities(ctx)
}

// Scenario files and the active pointer.

// PutScenario stores a scenario file under name.
func (s *Store) PutScenario(ctx context.Context, name string, content []byte) error {
	if s.cfg.MaxValueSize > 0 && len(content) > s.cfg.MaxValueSize {
		return fmt.Errorf("%w: %d bytes, limit %d", ErrScenarioTooLarge, len(content), s.cfg.MaxValueSize)
	}
	_, err := s.reg.Put(ctx, scenarioKey(name), content)
	return err
}

// GetScenario returns the scenario file stored under name.
func (s *Store) GetScenario(ctx context.Context, name string) ([]byte, error) {
	raw, _, err := s.reg.Get(ctx, scenarioKey(name))
	if errors.Is(err, errKeyNotFound) {
		return nil, registry.ErrNotFound
	}
	return raw, err
}

// ListScenarios returns the names of the stored scenario files.
func (s *Store) ListScenarios(ctx context.Context) ([]string, error) {
	keys, err := s.reg.Keys(ctx, "scenario.")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(keys))
	for _, k := range keys {
		if b, errDec := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(k, "scenario.")); errDec == nil {
			names = append(names, string(b))
		}
	}
	return names, nil
}

// SetActive moves the active-scenario pointer, checked against the revision
// it read.
func (s *Store) SetActive(ctx context.Context, active registry.ActiveScenario) error {
	value := encode(active)
	if s.cfg.MaxValueSize > 0 && len(value) > s.cfg.MaxValueSize {
		return fmt.Errorf("%w: the registered active scenario is %d bytes, limit %d", ErrScenarioTooLarge, len(value), s.cfg.MaxValueSize)
	}
	return s.retry("set active", func() error {
		_, rev, err := s.reg.Get(ctx, activeKey)
		if errors.Is(err, errKeyNotFound) {
			_, err = s.reg.Create(ctx, activeKey, value)
			return err
		}
		if err != nil {
			return err
		}
		_, err = s.reg.Update(ctx, activeKey, value, rev)
		return err
	})
}

// GetActive returns the active-scenario pointer, or registry.ErrNotFound
// when none was ever activated.
func (s *Store) GetActive(ctx context.Context) (registry.ActiveScenario, error) {
	var p registry.ActiveScenario
	raw, _, err := s.reg.Get(ctx, activeKey)
	if errors.Is(err, errKeyNotFound) {
		return p, registry.ErrNotFound
	}
	if err != nil {
		return p, err
	}
	if errDec := json.Unmarshal(raw, &p); errDec != nil {
		return p, errDec
	}
	return p, nil
}
