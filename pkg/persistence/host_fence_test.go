package persistence

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestHostFenceReceiptSurvivesRestartWithoutReleasingAuthority(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repo := NewFilesystemExecutionGrantRepository(root)
	session, err := repo.OpenSession(ctx, "agent", "process", "")
	require.NoError(t, err)
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	grant, err := repo.Issue(ctx, controlplane.ExecutionGrant{AgentID: "agent", SessionID: session.ID, CommandID: "cmd", ActionID: "run", Attempt: 1, Operation: "run_destroy", Digest: strings.Repeat("a", 64), TargetBootID: "11111111-1111-4111-8111-111111111111", Scope: controlplane.ExecutionScope{TopologyUID: "owner"}}, time.Minute)
	require.NoError(t, err)
	req := controlplane.HostFenceRequest{Version: 1, ID: "fence-1", Topology: "lab", RunID: "run", LabID: "disposable", ControllerKey: pub, JournalSHA256: strings.Repeat("b", 64), Grant: *grant}
	record, err := repo.BeginHostFence(ctx, req)
	require.NoError(t, err)
	require.Nil(t, record.Receipt)
	require.ErrorIs(t, repo.Authorize(ctx, *grant), ErrConflict)
	cold := NewFilesystemExecutionGrantRepository(root)
	same, err := cold.BeginHostFence(ctx, req)
	require.NoError(t, err)
	require.Equal(t, record, same)
	other := *grant
	other.Scope.TopologyUID = "other-topology"
	other.ActionID = "other-run"
	other.CommandID = "other-command"
	other.Epoch = 0
	_, err = cold.Issue(ctx, other, time.Minute)
	require.ErrorIs(t, err, ErrConflict, "same host may not accept unrelated work while a fence is pending")
	changed := req
	changed.JournalSHA256 = strings.Repeat("c", 64)
	_, err = cold.BeginHostFence(ctx, changed)
	require.ErrorIs(t, err, ErrConflict)
	digest, err := req.Digest()
	require.NoError(t, err)
	receipt, err := controlplane.SignHostFenceReceipt(key, req, controlplane.HostFenceReceipt{Version: 1, RequestSHA256: digest, LabID: req.LabID, OldBootID: grant.TargetBootID, NewBootID: "22222222-2222-4222-8222-222222222222", ControllerBootID: "33333333-3333-4333-8333-333333333333", ControllerBootUnchanged: true, QEMUPID: 42, QEMUStart: "123", QEMUExited: true, DiskDevice: 1, DiskInode: 2, ProviderScope: "local-only", OperationResult: "unknown", ObservedAt: time.Now().UTC()})
	require.NoError(t, err)
	require.NoError(t, cold.RecordHostFence(ctx, req.ID, receipt))
	require.NoError(t, cold.RecordHostFence(ctx, req.ID, receipt))
	stored, err := cold.ReadHostFence(ctx, req.ID)
	require.NoError(t, err)
	require.Equal(t, &receipt, stored.Receipt)
	raw, err := cold.storage.ReadExecutionAuthority(ctx)
	require.NoError(t, err)
	var ledger executionAuthority
	require.NoError(t, json.Unmarshal(raw, &ledger))
	oldVersion := ledger
	oldVersion.Version = 4
	oldRaw, err := json.Marshal(oldVersion)
	require.NoError(t, err)
	_, err = decodeExecutionAuthority(oldRaw)
	require.ErrorIs(t, err, ErrUnsupportedVersion)
	broken := ledger.Maintenance[record.BarrierID]
	broken.Released = true
	ledger.Maintenance[record.BarrierID] = broken
	brokenRaw, err := json.Marshal(ledger)
	require.NoError(t, err)
	_, err = decodeExecutionAuthority(brokenRaw)
	require.ErrorIs(t, err, ErrCorruptRecord, "fence-linked barriers cannot be released by edited storage")

	barrier, err := cold.InspectMaintenance(ctx, record.BarrierID)
	require.NoError(t, err)
	require.ErrorIs(t, cold.EndMaintenance(ctx, *barrier), ErrConflict)
	retained, err := cold.InspectGrant(ctx, "agent", grant.Scope)
	require.NoError(t, err)
	require.Equal(t, controlplane.GrantRevoked, retained.State)
	require.Empty(t, retained.StopReceipt)
	changed = req
	changed.ID = "another"
	changed.Grant.Epoch++
	_, err = cold.BeginHostFence(ctx, changed)
	require.ErrorIs(t, err, ErrConflict)
}
