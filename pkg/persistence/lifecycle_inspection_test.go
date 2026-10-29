package persistence

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/runtime"
)

func TestLifecycleInspectionIncludesPendingIntentWithoutState(t *testing.T) {
	for _, kind := range []string{"file", "sqlite"} {
		t.Run(kind, func(t *testing.T) {
			var repo *Repository
			var err error
			if kind == "file" {
				repo, err = OpenFilesystem(t.TempDir())
			} else {
				repo, err = OpenSQLite(filepath.Join(t.TempDir(), "db"))
			}
			require.NoError(t, err)
			defer repo.Close()
			ctx := context.Background()
			before, err := repo.InspectLifecycle(ctx, "lab")
			require.NoError(t, err)
			require.False(t, before.StateExists)
			action := runtime.ActionRecord{ID: "pending", RunID: "run", Topology: "lab", Resource: address.Resource("sysbox_node", "web"), Action: model.PlanActionCreate, Status: runtime.ActionIntentCommitted, OwnerUID: before.State.TopologyUID, ResourceUID: "uid", Generation: 1}
			require.NoError(t, repo.CommitActionIntent(ctx, action))
			snapshot, err := repo.InspectLifecycle(ctx, "lab")
			require.NoError(t, err)
			require.False(t, snapshot.StateExists)
			require.Empty(t, snapshot.State.Resources)
			require.Len(t, snapshot.Actions, 1)
			require.Equal(t, snapshot.State.TopologyUID, snapshot.Actions[0].OwnerUID)
			require.Equal(t, snapshot.State.Meta.Serial, snapshot.Actions[0].StateSerial)
			snapshot.Actions[0].ResourceUID = "changed"
			again, err := repo.InspectLifecycle(ctx, "lab")
			require.NoError(t, err)
			require.Equal(t, "uid", again.Actions[0].ResourceUID)
		})
	}
}
