package persistence

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestMaintenanceBarrierSurvivesRestartAndRequiresStops(t *testing.T) {
	root := t.TempDir()
	repo := NewFilesystemExecutionGrantRepository(root)
	ctx := context.Background()
	session, err := repo.OpenSession(ctx, "agent", "incarnation", "")
	require.NoError(t, err)
	request := controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "topology"}, AgentID: "agent", SessionID: session.ID, ActionID: "run", CommandID: "command", Attempt: 1, Operation: "run_apply", Digest: "digest", TargetBootID: "host"}
	grant, err := repo.Issue(ctx, request, time.Minute)
	require.NoError(t, err)
	barrier, err := repo.BeginMaintenance(ctx, "maintenance", "topology", "snapshot:immutable-digest")
	require.NoError(t, err)
	require.Greater(t, barrier.Epoch, grant.Epoch)
	cold := NewFilesystemExecutionGrantRepository(root)
	require.ErrorIs(t, cold.Authorize(ctx, *grant), ErrConflict)
	_, err = cold.Renew(ctx, *grant, time.Minute)
	require.ErrorIs(t, err, ErrConflict)
	_, err = cold.Issue(ctx, request, time.Minute)
	require.ErrorIs(t, err, ErrConflict)
	require.ErrorIs(t, cold.EndMaintenance(ctx, *barrier), ErrConflict)
	for _, field := range []string{"epoch", "topology", "digest", "created", "released"} {
		forged := *barrier
		switch field {
		case "epoch":
			forged.Epoch++
		case "topology":
			forged.TopologyUID = "other"
		case "digest":
			forged.RequestDigest = "other"
		case "created":
			forged.CreatedAt = forged.CreatedAt.Add(time.Second)
		case "released":
			forged.Released = true
		}
		require.ErrorIs(t, cold.EndMaintenance(ctx, forged), ErrConflict, field)
	}
	independent := request
	independent.Scope.TopologyUID = "other-topology"
	other, err := cold.Issue(ctx, independent, time.Minute)
	require.NoError(t, err)
	require.NoError(t, cold.Authorize(ctx, *other))
	again, err := cold.BeginMaintenance(ctx, "maintenance", "topology", "snapshot:immutable-digest")
	require.NoError(t, err)
	require.Equal(t, barrier, again)
	_, err = cold.BeginMaintenance(ctx, "maintenance", "topology", "different")
	require.ErrorIs(t, err, ErrConflict)
	_, err = cold.BeginMaintenance(ctx, "other", "topology", "snapshot:immutable-digest")
	require.ErrorIs(t, err, ErrConflict)
	require.NoError(t, cold.AcceptHostStop(ctx, *grant, "verified-host-stop"))
	require.NoError(t, cold.EndMaintenance(ctx, *barrier))
	require.NoError(t, cold.EndMaintenance(ctx, *barrier))
	next, err := cold.Issue(ctx, request, time.Minute)
	require.NoError(t, err)
	require.Greater(t, next.Epoch, barrier.Epoch)
	// Retrying an old maintenance request never re-freezes its successor.
	retired, err := cold.BeginMaintenance(ctx, "maintenance", "topology", "snapshot:immutable-digest")
	require.NoError(t, err)
	require.True(t, retired.Released)
	require.NoError(t, cold.Authorize(ctx, *next))
}

func TestMaintenanceRevokesParentAndChildrenWithoutInventingStops(t *testing.T) {
	repo, parent, request, _ := childGrantFixture(t)
	ctx := context.Background()
	child, err := repo.DeriveGuestTask(ctx, parent, request, time.Minute)
	require.NoError(t, err)
	barrier, err := repo.BeginMaintenance(ctx, "restore", "topology", "snapshot-digest")
	require.NoError(t, err)
	_, err = repo.DeriveGuestTask(ctx, parent, request, time.Minute)
	require.ErrorIs(t, err, ErrConflict)
	_, err = repo.RenewDerivedGuestTask(ctx, parent, *child, time.Minute)
	require.ErrorIs(t, err, ErrConflict)
	require.ErrorIs(t, repo.Authorize(ctx, *child), ErrConflict)
	require.NoError(t, repo.AcceptHostStop(ctx, *child, "child-stopped"))
	require.ErrorIs(t, repo.EndMaintenance(ctx, *barrier), ErrConflict)
	require.NoError(t, repo.AcceptHostStop(ctx, parent, "parent-stopped"))
	require.NoError(t, repo.EndMaintenance(ctx, *barrier))
	next, err := repo.BeginMaintenance(ctx, "next-restore", "topology", "next-digest")
	require.NoError(t, err)
	require.NoError(t, repo.EndMaintenance(ctx, *barrier))
	_, err = repo.Issue(ctx, parent, time.Minute)
	require.ErrorIs(t, err, ErrConflict, "old release cannot unfreeze successor")
	require.NoError(t, repo.EndMaintenance(ctx, *next))
}

func TestMaintenanceAuthorityRejectsOldFormat(t *testing.T) {
	repo := NewFilesystemExecutionGrantRepository(t.TempDir())
	ctx := context.Background()
	require.NoError(t, repo.ObserveTime(ctx))
	require.NoError(t, repo.storage.UpdateExecutionAuthority(ctx, func(raw []byte) ([]byte, error) {
		var record map[string]any
		if err := json.Unmarshal(raw, &record); err != nil {
			return nil, err
		}
		record["version"] = 1
		return json.Marshal(record)
	}))
	_, err := repo.BeginMaintenance(ctx, "restore", "topology", "digest")
	require.ErrorIs(t, err, ErrUnsupportedVersion)
	_, err = repo.ListActiveGrants(ctx)
	require.ErrorIs(t, err, ErrUnsupportedVersion)
}

func TestMaintenanceWriteRejectsEmptyPersistedLedger(t *testing.T) {
	require.NoError(t, ValidateMaintenanceWrite(nil, "topology"))
	require.ErrorIs(t, ValidateMaintenanceWrite([]byte{}, "topology"), ErrCorruptRecord)
	require.ErrorIs(t, ValidateMaintenanceWrite([]byte(`{}`), "topology"), ErrUnsupportedVersion)
}
