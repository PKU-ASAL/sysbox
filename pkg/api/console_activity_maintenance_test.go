package api

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestMaintenanceQuiescenceBlocksActiveConsoleSession(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	sess := controlplane.ConsoleSession{ID: "console", Topology: "lab", Workspace: "other-workspace", Status: controlplane.ConsoleSessionStatusRunning, CreatedAt: time.Now().UTC(), StartedAt: time.Now().UTC()}
	require.NoError(t, s.apiStore.SaveConsoleSession(context.Background(), sess))
	require.ErrorIs(t, s.workspaceService().ensureTopologyQuiescent(context.Background(), "lab"), persistence.ErrConflict)
}
