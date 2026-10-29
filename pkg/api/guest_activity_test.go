package api

import (
	"context"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestGuestActivityBlocksMetadataDeleteAndOldCleanupReceipt(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(map[bool]string{false: "active", true: "completed-after-destroy"}[after], func(t *testing.T) {
			s := NewServer(t.TempDir(), t.TempDir())
			ctx := context.Background()
			completedCleanupRun(t, s)
			x := controlplane.GuestExecution{ID: "guest", Version: 1, Topology: "lab", Node: "node", AgentID: "owner", Status: controlplane.GuestExecutionQueued, CreatedAt: time.Now().UTC()}
			require.NoError(t, s.apiStore.SaveGuestExecution(ctx, x))
			if after {
				x.Status = controlplane.GuestExecutionCompleted
				x.EndedAt = time.Now().UTC()
				ok, err := s.apiStore.CompareAndSwapGuestExecution(ctx, x, 1)
				require.NoError(t, err)
				require.True(t, ok)
			}
			require.ErrorIs(t, s.workspaceService().Delete(ctx, "lab", false), persistence.ErrConflict)
			if !after {
				require.ErrorIs(t, s.workspaceService().Delete(ctx, "lab", true), persistence.ErrConflict)
			}
		})
	}
}
