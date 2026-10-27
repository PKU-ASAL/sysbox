package persistence

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

func TestAuthorizedSnapshotRestoreRequiresFrozenStoppedScope(t *testing.T) {
	ctx := context.Background()
	repo, err := OpenFilesystem(t.TempDir())
	require.NoError(t, err)
	snapshot, err := repo.StateBackend("lab").(state.SnapshotBackend).Snapshot(ctx, "before")
	require.NoError(t, err)
	request, err := repo.PlanSnapshotRestore(ctx, "lab", snapshot.ID, "restore", 0)
	require.NoError(t, err)
	digest, err := request.Digest()
	require.NoError(t, err)
	authority := NewFilesystemExecutionGrantRepository(t.TempDir())
	session, err := authority.OpenSession(ctx, "agent", "incarnation", "")
	require.NoError(t, err)
	grant, err := authority.Issue(ctx, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: request.TopologyUID}, AgentID: "agent", SessionID: session.ID, ActionID: "action", CommandID: "command", Attempt: 1, Operation: "run_apply", Digest: "digest", TargetBootID: "host"}, time.Minute)
	require.NoError(t, err)
	barrier, err := authority.BeginMaintenance(ctx, request.ID, request.TopologyUID, digest)
	require.NoError(t, err)
	request.MaintenanceEpoch = barrier.Epoch
	var raw []byte
	require.NoError(t, repo.update(ctx, "lab", func(a *aggregate) error { raw, err = json.Marshal(a); return err }))
	ledger, err := authority.storage.ReadExecutionAuthority(ctx)
	require.NoError(t, err)
	_, _, err = ApplyAuthorizedSnapshotRestore(raw, ledger, "lab", request)
	require.ErrorIs(t, err, ErrConflict, "revoked is not stopped")
	require.NoError(t, authority.AcceptHostStop(ctx, *grant, "verified-stop"))
	ledger, err = authority.storage.ReadExecutionAuthority(ctx)
	require.NoError(t, err)
	var corrupt executionAuthority
	require.NoError(t, json.Unmarshal(ledger, &corrupt))
	for key, stopped := range corrupt.Grants {
		stopped.StopReceipt = ""
		corrupt.Grants[key] = stopped
	}
	corruptRaw, err := json.Marshal(corrupt)
	require.NoError(t, err)
	_, _, err = ApplyAuthorizedSnapshotRestore(raw, corruptRaw, "lab", request)
	require.Error(t, err, "stopped enum without durable receipt cannot authorize restore")
	next, receipt, err := ApplyAuthorizedSnapshotRestore(raw, ledger, "lab", request)
	require.NoError(t, err)
	require.Equal(t, request.ExpectedSerial+1, receipt.Serial)
	for _, mutate := range []func(*SnapshotRestoreRequest){
		func(r *SnapshotRestoreRequest) { r.MaintenanceEpoch++ },
		func(r *SnapshotRestoreRequest) { r.ExpectedSerial++ },
		func(r *SnapshotRestoreRequest) { r.ID = "other" },
	} {
		changed := request
		mutate(&changed)
		_, _, err := ApplyAuthorizedSnapshotRestore(raw, ledger, "lab", changed)
		require.Error(t, err)
	}
	require.NoError(t, authority.EndMaintenance(ctx, *barrier))
	ledger, err = authority.storage.ReadExecutionAuthority(ctx)
	require.NoError(t, err)
	_, _, err = ApplyAuthorizedSnapshotRestore(raw, ledger, "lab", request)
	require.ErrorIs(t, err, ErrConflict, "released barrier cannot authorize a new restore")
	replayed, oldReceipt, err := ApplyAuthorizedSnapshotRestore(next, ledger, "lab", request)
	require.NoError(t, err)
	require.Equal(t, next, replayed)
	require.Equal(t, receipt, oldReceipt)
}
