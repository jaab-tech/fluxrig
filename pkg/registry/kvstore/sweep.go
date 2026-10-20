// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package kvstore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/registry"
)

// SweepResult counts what one sweep changed.
type SweepResult struct {
	// Removed counts Rack records deleted because no name ever pointed at
	// them: a registration or an adoption that stopped before claiming its
	// name. Its secret never reached a Rack, so nothing can use it.
	Removed int
	// Repaired counts renames completed or rolled back.
	Repaired int
	// NameKeysRemoved counts name keys deleted because their entity is gone.
	NameKeysRemoved int
}

// SweepOrphans repairs what a crash between two registry writes leaves
// behind. The name keys say which case it is:
//
//   - A Rack record that no name key points at never finished a registration
//     or an adoption: it is deleted.
//   - A Rack record whose own name does not resolve to it, but an older name
//     does, stopped in the middle of a rename: the rename is completed when
//     the new name is free, and rolled back to the older name when it is not.
//   - A name key whose entity no longer exists is left by an interrupted
//     removal: it is deleted.
//
// A record updated less than grace ago is skipped: every write sequence
// passes through these states briefly, and a sweep must not race one.
// Every change is revision-checked, so a record that moves during the sweep
// is left for the next one.
func (s *Store) SweepOrphans(ctx context.Context, grace time.Duration) (SweepResult, error) {
	var res SweepResult

	// Which name keys point at which entity.
	//
	// The record pass below deletes a Rack that no name key points at, so
	// this map must be complete. A name key that cannot be read or decoded
	// stops the sweep: skipping it would make its Rack look unnamed, and a
	// leader election during one pass would delete every approved Rack.
	nameKeys, err := s.reg.Keys(ctx, "name.")
	if err != nil {
		return res, fmt.Errorf("kvstore: sweep: list name keys: %w", err)
	}
	held := map[nameRef][]string{}
	for _, k := range nameKeys {
		raw, rev, errGet := s.reg.Get(ctx, k)
		if errors.Is(errGet, errKeyNotFound) {
			continue // removed since the listing
		}
		if errGet != nil {
			return res, fmt.Errorf("kvstore: sweep: read name key %s: %w", k, errGet)
		}
		var ref nameRef
		if errJSON := json.Unmarshal(raw, &ref); errJSON != nil {
			return res, fmt.Errorf("kvstore: sweep: decode name key %s: %w", k, errJSON)
		}
		name, errDec := decodeNameKey(k)
		if errDec != nil {
			return res, fmt.Errorf("kvstore: sweep: decode name key %s: %w", k, errDec)
		}
		// No grace period here, unlike the record loop below. None is needed:
		// every flow creates a record before its name key and deletes a name
		// key only after (or instead of) its record, so a key whose record is
		// missing is never a write still in progress. The check reads the
		// record after the key, and the delete is revision-checked, so a key
		// repointed in between is left alone.
		if _, _, errRec := s.getRecord(ctx, ref.TypeID, ref.MachineID); errors.Is(errRec, registry.ErrNotFound) {
			if s.reg.Delete(ctx, k, rev) == nil {
				res.NameKeysRemoved++
				s.log.Warn("registry sweep: removed a name key whose entity is gone", "name", name, "machine_id", ref.MachineID)
			}
			continue
		}
		held[ref] = append(held[ref], name)
	}

	prefix := fmt.Sprintf("entity.%d.", typeRack)
	keys, err := s.reg.Keys(ctx, prefix)
	if err != nil {
		return res, fmt.Errorf("kvstore: sweep: list records: %w", err)
	}
	for _, k := range keys {
		id, errParse := uuid.Parse(strings.TrimPrefix(k, prefix))
		if errParse != nil {
			continue
		}
		rec, rev, errGet := s.getRecord(ctx, typeRack, id)
		if errGet != nil || rec.Name == "" || s.now().Sub(rec.UpdatedAt) < grace {
			continue
		}
		ref := nameRef{TypeID: typeRack, MachineID: id}
		names := held[ref]
		if contains(names, rec.Name) {
			continue // its name resolves to it: healthy
		}
		if len(names) == 0 {
			if s.reg.Delete(ctx, k, rev) == nil {
				_ = s.live.Delete(ctx, entityKey(typeRack, id), 0)
				res.Removed++
				s.log.Warn("registry sweep: removed a Rack record that never finished registering", "name", rec.Name, "machine_id", id)
			}
			continue
		}
		// An interrupted rename: rec.Name is the new name, names hold the old.
		if errClaim := s.claimName(ctx, rec.Name, ref); errClaim == nil {
			for _, old := range names {
				s.releaseName(ctx, old, ref)
			}
			res.Repaired++
			s.log.Info("registry sweep: completed an interrupted rename", "name", rec.Name, "machine_id", id)
			continue
		} else if !errors.Is(errClaim, registry.ErrNameConflict) {
			return res, errClaim
		}
		// The new name went to another Rack meanwhile: roll back.
		rec.Name = names[0]
		rec.UpdatedAt = s.now()
		if _, errUpd := s.reg.Update(ctx, k, encode(rec), rev); errUpd == nil {
			res.Repaired++
			s.log.Warn("registry sweep: rolled back an interrupted rename whose new name is taken", "name", rec.Name, "machine_id", id)
		}
	}
	return res, nil
}

func decodeNameKey(key string) (string, error) {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(key, "name."))
	return string(b), err
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
