package api

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/oslab/sysbox/pkg/persistence"
)

type deletionIntent struct {
	Topology string    `json:"topology"`
	UID      string    `json:"topology_uid"`
	Serial   int64     `json:"serial"`
	Created  time.Time `json:"created_at"`
}

func (s *WorkspaceService) deletionIntentPath(topology string) string {
	return filepath.Join(s.runsDir, topology, "delete.intent")
}

func (s *WorkspaceService) writeDeletionIntent(intent deletionIntent) error {
	path := s.deletionIntentPath(intent.Topology)
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

func (s *WorkspaceService) readDeletionIntent(topology string) (*deletionIntent, error) {
	raw, err := os.ReadFile(s.deletionIntentPath(topology))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var intent deletionIntent
	if err = json.Unmarshal(raw, &intent); err != nil || intent.Topology != topology || intent.UID == "" || intent.Created.IsZero() {
		return nil, persistence.ErrCorruptRecord
	}
	return &intent, nil
}

func (s *WorkspaceService) finishDeletionIntent(ctx context.Context, intent deletionIntent) error {
	if s.deleteState != nil {
		if err := s.deleteState(ctx, intent.Topology, intent.Serial, intent.UID); err != nil && !errors.Is(err, persistence.ErrNotFound) {
			return err
		}
	}
	if s.stateBackend == "" {
		if err := s.removeWorkspaceMetadata(intent.Topology); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.RemoveAll(filepath.Join(s.workspacesDir, intent.Topology)); err != nil {
		return err
	}
	if err := s.deletePlacementUnpublished(ctx, intent.Topology); err != nil {
		return err
	}
	if s.health != nil {
		_ = s.health.DeleteHealth(ctx, intent.Topology)
	}
	if err := os.Remove(s.deletionIntentPath(intent.Topology)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncWorkspaceParents(s.runsDir)
}

// RecoverDeletionIntents finishes metadata cleanup after a controller crash.
// The intent is written only after provider quiescence evidence, so recovery
// never invents a destroy result or contacts a provider.
func (s *WorkspaceService) RecoverDeletionIntents(ctx context.Context) error {
	entries, err := os.ReadDir(s.runsDir)
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
		intent, err := s.readDeletionIntent(entry.Name())
		if err != nil {
			return err
		}
		if intent == nil {
			continue
		}
		var admissionUnlock func()
		if s.lockAdmission != nil {
			admissionUnlock = s.lockAdmission(entry.Name())
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
