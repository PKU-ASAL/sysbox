package api

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/controlplane"
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
