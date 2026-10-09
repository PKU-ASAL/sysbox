package api

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSnapshotAPIStoresJournalAndRejectsActiveRestore(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	ctx := context.Background()
	created := httptest.NewRecorder()
	s.ServeHTTP(created, httptest.NewRequest("POST", "/v1/topologies/lab/state/snapshots", bytes.NewBufferString(`{"reason":"before-change"}`)))
	require.Equal(t, 201, created.Code, created.Body.String())
	var snap state.Snapshot
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &snap))
	require.NotEmpty(t, snap.ID)
	require.NoError(t, s.apiStore.SaveRun(ctx, controlplane.Run{ID: "busy", Topology: "lab", Op: "apply", Status: controlplane.RunAssigned, QueuedAt: time.Now().UTC()}))
	restore := httptest.NewRecorder()
	s.ServeHTTP(restore, httptest.NewRequest("POST", "/v1/topologies/lab/state/snapshots/"+snap.ID+"/restore", nil))
	require.Equal(t, 409, restore.Code, restore.Body.String())
	deleted := httptest.NewRecorder()
	s.ServeHTTP(deleted, httptest.NewRequest("DELETE", "/v1/topologies/lab/state/snapshots/"+snap.ID, nil))
	require.Equal(t, 204, deleted.Code, deleted.Body.String())
}

func TestStateMaintenanceRejectsIncompleteActivityRegistry(t *testing.T) {
	for _, missing := range []string{"runs", "nodes", "guests"} {
		t.Run(missing, func(t *testing.T) {
			s := NewServer(t.TempDir(), t.TempDir())
			service := s.workspaceService()
			manager, err := s.stateManager("lab")
			require.NoError(t, err)
			snapshot, err := manager.Snapshot(context.Background(), "before")
			require.NoError(t, err)
			switch missing {
			case "runs":
				service.loadRuns = nil
			case "nodes":
				service.loadNodeOperations = nil
			case "guests":
				service.loadGuestActivity = nil
			}
			require.ErrorIs(t, service.RestoreStateSnapshot(context.Background(), "lab", snapshot.ID), persistence.ErrConflict)
			require.ErrorIs(t, service.ForceUnlock(context.Background(), "lab"), persistence.ErrConflict)
		})
	}
}
