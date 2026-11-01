package api

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestNodeOperationRepositoryPreservesIdentityAndRejectsOverwrite(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s controlstore.Store) {
		ctx := context.Background()
		op := controlplane.NodeOperation{ID: "operation", Topology: "lab", Workspace: "lab", ProjectID: "project", AgentID: "agent", Operation: "pause", Node: "web", Status: controlplane.NodeOperationStatusQueued, CreatedAt: time.Now().UTC(), CommandID: "command", ActionID: "action", ResourceUID: "resource", Generation: 2, LeaseEpoch: 3, LeaseOwner: "worker", Attempt: 1}
		require.NoError(t, s.CreateNodeOperation(ctx, op))
		stored, err := s.GetNodeOperation(ctx, op.ID)
		require.NoError(t, err)
		require.Equal(t, op, *stored)
		next := op
		next.Status = controlplane.NodeOperationStatusRunning
		require.ErrorIs(t, s.CreateNodeOperation(ctx, next), persistence.ErrConflict)
		require.NoError(t, s.CompareAndSwapNodeOperation(ctx, op, next))
		require.ErrorIs(t, s.CompareAndSwapNodeOperation(ctx, op, next), persistence.ErrConflict)
		require.Error(t, s.CompareAndSwapNodeOperation(ctx, next, op), "running must not regress to queued")
		forged := next
		forged.Generation++
		require.Error(t, s.CompareAndSwapNodeOperation(ctx, next, forged))
		for _, fault := range []string{"requester", "roles", "audit"} {
			forged := next
			switch fault {
			case "requester":
				forged.RequestedBy = "another"
			case "roles":
				forged.Roles = []string{"admin"}
			case "audit":
				forged.Audit = []controlplane.Event{{Actor: "forged"}}
			}
			if fault == "audit" {
				withAudit := next
				withAudit.Audit = []controlplane.Event{{Actor: "original"}}
				require.NoError(t, s.CompareAndSwapNodeOperation(ctx, next, withAudit))
				next = withAudit
			}
			require.Error(t, s.CompareAndSwapNodeOperation(ctx, next, forged), fault)
		}
	})
}

func TestNodeOperationCASHasOneWinner(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s controlstore.Store) {
		ctx := context.Background()
		op := controlplane.NodeOperation{ID: "race", Topology: "lab", AgentID: "agent", Status: controlplane.NodeOperationStatusQueued}
		require.NoError(t, s.CreateNodeOperation(ctx, op))
		var wg sync.WaitGroup
		results := make(chan error, 2)
		for _, status := range []string{controlplane.NodeOperationStatusDone, controlplane.NodeOperationStatusFailed} {
			wg.Add(1)
			go func(status string) {
				defer wg.Done()
				next := op
				next.Status = status
				results <- s.CompareAndSwapNodeOperation(ctx, op, next)
			}(status)
		}
		wg.Wait()
		close(results)
		wins := 0
		for err := range results {
			if err == nil {
				wins++
			} else {
				require.ErrorIs(t, err, persistence.ErrConflict)
			}
		}
		require.Equal(t, 1, wins)
		stored, err := s.GetNodeOperation(ctx, op.ID)
		require.NoError(t, err)
		changed := *stored
		changed.Err = "overwrite result"
		require.Error(t, s.CompareAndSwapNodeOperation(ctx, *stored, changed))
	})
}
