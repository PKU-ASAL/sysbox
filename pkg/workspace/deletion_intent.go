package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/oslab/sysbox/pkg/persistence"
)

type DeletionIntent struct {
	Topology string    `json:"topology"`
	UID      string    `json:"topology_uid"`
	Serial   int64     `json:"serial"`
	Created  time.Time `json:"created_at"`
}

func (s *Catalog) DeletionIntentPath(topology string) string {
	return filepath.Join(s.RunsDir, topology, "delete.intent")
}

func (s *Catalog) WriteDeletionIntent(intent DeletionIntent) error {
	path := s.DeletionIntentPath(intent.Topology)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(intent)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err = os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	return syncWorkspaceParents(filepath.Dir(path))
}

func (s *Catalog) ReadDeletionIntent(topology string) (*DeletionIntent, error) {
	raw, err := os.ReadFile(s.DeletionIntentPath(topology))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var intent DeletionIntent
	if err = json.Unmarshal(raw, &intent); err != nil || intent.Topology != topology || intent.UID == "" || intent.Created.IsZero() {
		return nil, persistence.ErrCorruptRecord
	}
	return &intent, nil
}

func (s *Catalog) finishDeletionIntent(ctx context.Context, intent DeletionIntent) error {
	if s.DeleteState != nil {
		if err := s.DeleteState(ctx, intent.Topology, intent.Serial, intent.UID); err != nil && !errors.Is(err, persistence.ErrNotFound) {
			return err
		}
	}
	if s.stateBackend == "" {
		if err := s.removeWorkspaceMetadata(intent.Topology); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.RemoveAll(filepath.Join(s.WorkspacesDir, intent.Topology)); err != nil {
		return err
	}
	if err := s.deletePlacementUnpublished(ctx, intent.Topology); err != nil {
		return err
	}
	if s.health != nil {
		_ = s.health.DeleteHealth(ctx, intent.Topology)
	}
	if err := os.Remove(s.DeletionIntentPath(intent.Topology)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncWorkspaceParents(s.RunsDir)
}

// RecoverDeletionIntents finishes metadata cleanup after a controller crash.
// The intent is written only after provider quiescence evidence, so recovery
// never invents a destroy result or contacts a provider.
func (s *Catalog) RecoverDeletionIntents(ctx context.Context) error {
	entries, err := os.ReadDir(s.RunsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == ".workspace-locks" {
			continue
		}
		intent, err := s.ReadDeletionIntent(entry.Name())
		if err != nil {
			return err
		}
		if intent == nil {
			continue
		}
		var admissionUnlock func()
		if s.LockAdmission != nil {
			admissionUnlock = s.LockAdmission(entry.Name())
		}
		unlock, err := s.lockPublication(ctx, entry.Name())
		if err != nil {
			if admissionUnlock != nil {
				admissionUnlock()
			}
			return err
		}
		err = s.finishDeletionIntent(ctx, *intent)
		unlock()
		if admissionUnlock != nil {
			admissionUnlock()
		}
		if err != nil {
			return err
		}
	}
	return nil
}
