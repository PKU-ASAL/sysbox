package api

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestMaintenanceQuiescenceBlocksActiveAgentCommand(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	repo, err := s.actionRepository("lab")
	require.NoError(t, err)
	st, err := repo.LoadState(context.Background(), "lab")
	require.NoError(t, err)
	cmd := controlplane.AgentCommand{ID: "queued", AgentID: "agent", TopologyUID: st.TopologyUID, Type: "guest_execution", Status: controlplane.AgentCommandStatusQueued, CreatedAt: time.Now().UTC()}
	// Durable commands must remain visible even without an Agent registration.
	require.NoError(t, s.apiStore.SaveAgentCommand(context.Background(), cmd))
	require.ErrorIs(t, s.workspaceService().ensureTopologyQuiescent(context.Background(), "lab"), persistence.ErrConflict)
}
