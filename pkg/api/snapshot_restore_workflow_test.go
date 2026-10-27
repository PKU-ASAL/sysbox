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

func TestSnapshotRestoreWorkflowReplaysAfterAPIReconstruction(t *testing.T) {
	root, workspace := t.TempDir(), t.TempDir()
	s := NewServer(root, workspace)
	ctx := context.Background()
	repo, err := s.actionRepository("lab")
	require.NoError(t, err)
	snapshot, err := repo.StateBackend("lab").(state.SnapshotBackend).Snapshot(ctx, "before")
	require.NoError(t, err)
	plan, err := repo.PlanSnapshotRestore(ctx, "lab", snapshot.ID, "restore-request", 0)
	require.NoError(t, err)
	first, err := s.restoreSnapshotWorkflow(ctx, "lab", plan)
	require.NoError(t, err)
	require.Equal(t, "restored", first.Status)
	require.NotNil(t, first.Receipt)
	cold := NewServer(root, workspace)
	second, err := cold.restoreSnapshotWorkflow(ctx, "lab", plan)
	require.NoError(t, err)
	require.Equal(t, first.Receipt, second.Receipt)
	authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: cold.apiStore})
	barrier, err := authority.BeginMaintenance(ctx, plan.ID, plan.TopologyUID, mustRestoreDigest(t, plan))
	require.NoError(t, err)
	require.True(t, barrier.Released)
}

func TestSnapshotRestoreHTTPPlanCommitAndAbandon(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	ctx := context.Background()
	repo, err := s.actionRepository("lab")
	require.NoError(t, err)
	snapshot, err := repo.StateBackend("lab").(state.SnapshotBackend).Snapshot(ctx, "before")
	require.NoError(t, err)
	base := "/v1/topologies/lab/state/snapshots/" + snapshot.ID
	missing := httptest.NewRecorder()
	s.ServeHTTP(missing, httptest.NewRequest("POST", base+"/restore-plan", nil))
	require.Equal(t, 400, missing.Code)
	planReq := httptest.NewRequest("POST", base+"/restore-plan", nil)
	planReq.Header.Set("Idempotency-Key", "http-restore")
	planned := httptest.NewRecorder()
	s.ServeHTTP(planned, planReq)
	require.Equal(t, 200, planned.Code, planned.Body.String())
	var plan persistence.SnapshotRestoreRequest
	require.NoError(t, json.Unmarshal(planned.Body.Bytes(), &plan))
	post := func(path string, value any) *httptest.ResponseRecorder {
		raw, err := json.Marshal(value)
		require.NoError(t, err)
		out := httptest.NewRecorder()
		s.ServeHTTP(out, httptest.NewRequest("POST", base+path, bytes.NewReader(raw)))
		return out
	}
	// Abandonment must not manufacture a freeze for a never-started operation.
	require.NotEqual(t, 200, post("/restore-cancel", plan).Code)
	restored := post("/restore", plan)
	require.Equal(t, 200, restored.Code, restored.Body.String())
	require.Equal(t, 200, post("/restore", plan).Code)
	require.Equal(t, 409, post("/restore-cancel", plan).Code)
	// A separate prepared operation can be abandoned after all grants stop.
	next, err := repo.PlanSnapshotRestore(ctx, "lab", snapshot.ID, "cancel-me", 0)
	require.NoError(t, err)
	authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore})
	_, err = authority.BeginMaintenance(ctx, next.ID, next.TopologyUID, mustRestoreDigest(t, next))
	require.NoError(t, err)
	abandoned := post("/restore-cancel", next)
	require.Equal(t, 200, abandoned.Code, abandoned.Body.String())
	require.Contains(t, abandoned.Body.String(), "abandoned")
	require.Equal(t, 409, post("/restore", next).Code)
}

func TestSnapshotRestoreWorkflowWaitsForStopAcrossRestart(t *testing.T) {
	root, workspace := t.TempDir(), t.TempDir()
	s := NewServer(root, workspace)
	ctx := context.Background()
	repo, err := s.actionRepository("lab")
	require.NoError(t, err)
	snapshot, err := repo.StateBackend("lab").(state.SnapshotBackend).Snapshot(ctx, "before")
	require.NoError(t, err)
	plan, err := repo.PlanSnapshotRestore(ctx, "lab", snapshot.ID, "restore", 0)
	require.NoError(t, err)
	authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore})
	session, err := authority.OpenSession(ctx, "agent", "session", "")
	require.NoError(t, err)
	grant, err := authority.Issue(ctx, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: plan.TopologyUID}, AgentID: "agent", SessionID: session.ID, ActionID: "action", CommandID: "command", Attempt: 1, Operation: "run_apply", Digest: "digest", TargetBootID: "host"}, time.Minute)
	require.NoError(t, err)
	pending, err := s.restoreSnapshotWorkflow(ctx, "lab", plan)
	require.NoError(t, err)
	require.Equal(t, "waiting_for_stop_or_reconciliation", pending.Status)
	require.Nil(t, pending.Receipt)
	require.ErrorIs(t, authority.Authorize(ctx, *grant), persistence.ErrConflict)
	before, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	require.Equal(t, plan.ExpectedSerial, before.Meta.Serial)
	require.NoError(t, authority.AcceptHostStop(ctx, *grant, "verified-stop"))
	cold := NewServer(root, workspace)
	complete, err := cold.restoreSnapshotWorkflow(ctx, "lab", pending.Request)
	require.NoError(t, err)
	require.Equal(t, "restored", complete.Status)
	require.Equal(t, plan.ExpectedSerial+1, complete.Receipt.Serial)
}

func mustRestoreDigest(t *testing.T, request persistence.SnapshotRestoreRequest) string {
	t.Helper()
	digest, err := request.Digest()
	require.NoError(t, err)
	return digest
}
