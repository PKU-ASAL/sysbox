package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

type recoveryResource struct {
	kind                         string
	exists, unknown, provisioner bool
	creates, deletes, reads      int
	createErr                    error
	onRead                       func()
}

func (h *recoveryResource) Type() string { return h.kind }
func (h *recoveryResource) Schema() runtime.ResourceSchema {
	return runtime.ResourceSchema{Type: h.kind, Version: 1}
}
func (h *recoveryResource) Read(_ context.Context, r state.Resource, providers *driver.Registry) (runtime.ResourceReadResult, error) {
	if h.onRead != nil {
		h.onRead()
	}
	h.reads++
	status := state.ResourceAbsent
	if h.exists {
		status = state.ResourcePresent
	}
	if h.unknown {
		status = state.ResourceUnknown
	}
	return runtime.ResourceReadResult{Status: status, Resource: r}, nil
}
func (h *recoveryResource) Discover(ctx context.Context, _ *runtime.ProviderContext, n *graph.Node) (runtime.ResourceReadResult, error) {
	return h.Read(ctx, state.Resource{Address: n.Address}, driver.DefaultRegistry)
}
func (h *recoveryResource) PlanDiff(n *graph.Node, r *state.Resource) (model.PlannedChange, error) {
	a := model.PlanActionNoop
	if r == nil {
		a = model.PlanActionCreate
	}
	return model.PlannedChange{Address: n.Address, Action: a}, nil
}
func (h *recoveryResource) Create(_ context.Context, _ *runtime.ProviderContext, n *graph.Node) (state.Resource, error) {
	h.exists = true
	h.creates++
	return state.Resource{Address: n.Address, ExternalID: "managed-instance"}, h.createErr
}
func (h *recoveryResource) Delete(context.Context, *runtime.ProviderContext, state.Resource) error {
	h.exists = false
	h.deletes++
	return nil
}
func (h *recoveryResource) ExternalID(r state.Resource) string { return r.ExternalID }
func (h *recoveryResource) DecodeResource(config.ResourceBlock, string, *hcl.EvalContext, *driver.Registry) (any, []address.Address, error) {
	if h.provisioner {
		return &config.NodeConfig{Provisioners: []config.ProvisionerConfig{{Type: "exec", Program: "work", Shell: "none"}}}, nil, nil
	}
	return nil, nil, nil
}

type rejectRecoveryCommit struct{ runtime.ActionRepository }

func (r rejectRecoveryCommit) CommitActionOutcome(ctx context.Context, a runtime.ActionRecord, p *runtime.StatePatch) error {
	if p != nil {
		return errors.New("commit unavailable")
	}
	return r.ActionRepository.CommitActionOutcome(ctx, a, p)
}

var recoveryResourceID atomic.Uint64

func TestCanonicalRecoveryRejectsFreezeStartedDuringObservation(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		t.Run(fmt.Sprint(cleanup), func(t *testing.T) {
			s, run, provider, _ := canonicalRecoveryFixture(t, false)
			ctx := context.Background()
			repo, err := s.actionRepository("lab")
			require.NoError(t, err)
			before, err := repo.LoadState(ctx, "lab")
			require.NoError(t, err)
			provider.onRead = func() {
				provider.onRead = nil
				_, err := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore}).BeginMaintenance(ctx, "freeze-during-read", before.TopologyUID, "restore-digest")
				require.NoError(t, err)
			}
			manager, err := s.stateManager("lab")
			require.NoError(t, err)
			if cleanup {
				_, err = s.cleanupCanonicalActions(ctx, "lab", run.ID)
			} else {
				_, err = s.reconcileCanonicalActions(ctx, "lab", run.ID, manager)
			}
			require.ErrorIs(t, err, persistence.ErrConflict)
			after, err := repo.LoadState(ctx, "lab")
			require.NoError(t, err)
			require.Equal(t, before.Meta.Serial, after.Meta.Serial)
			require.Equal(t, before.Resources, after.Resources)
		})
	}
}

func TestCanonicalRecoveryAndCleanupRefuseMaintenanceBeforeProviderAccess(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		t.Run(fmt.Sprint(cleanup), func(t *testing.T) {
			s, run, provider, _ := canonicalRecoveryFixture(t, false)
			ctx := context.Background()
			repo, err := s.actionRepository("lab")
			require.NoError(t, err)
			before, err := repo.LoadState(ctx, "lab")
			require.NoError(t, err)
			journal, err := repo.LoadActions(ctx, "lab")
			require.NoError(t, err)
			authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore})
			_, err = authority.BeginMaintenance(ctx, "maintenance", before.TopologyUID, "restore-digest")
			require.NoError(t, err)
			reads, deletes := provider.reads, provider.deletes
			if cleanup {
				_, err = s.cleanupCanonicalActions(ctx, "lab", run.ID)
			} else {
				manager, managerErr := s.stateManager("lab")
				require.NoError(t, managerErr)
				_, err = s.reconcileCanonicalActions(ctx, "lab", run.ID, manager)
			}
			require.ErrorIs(t, err, persistence.ErrConflict)
			require.Equal(t, reads, provider.reads)
			require.Equal(t, deletes, provider.deletes)
			after, err := repo.LoadActions(ctx, "lab")
			require.NoError(t, err)
			require.Equal(t, journal, after)
		})
	}
}

func canonicalRecoveryFixture(t *testing.T, provisioner bool, agents ...string) (*Server, *controlplane.Run, *recoveryResource, address.Address) {
	t.Helper()
	s := NewServer(t.TempDir(), t.TempDir())
	h := &recoveryResource{kind: fmt.Sprintf("api_recovery_%d", recoveryResourceID.Add(1)), provisioner: provisioner}
	if provisioner {
		h.createErr = errors.New("guest reply lost")
	}
	runtime.RegisterResourceHandler(h)
	files := map[string][]byte{"field.sysbox.hcl": []byte(fmt.Sprintf(`resource %q "node" {}`, h.kind))}
	digest := controlplane.ComputeRevisionDigest(files)
	require.NoError(t, s.apiStore.SaveGlobalRevision(context.Background(), controlplane.GlobalRevision{Revision: digest, Files: files}))
	path, err := s.workspaceService().MaterializeRevision("lab", digest, files)
	require.NoError(t, err)
	opts := runStartOptions{}
	if len(agents) > 0 {
		opts.AgentID = agents[0]
	}
	run := s.jobs.startWithOptions("lab", "apply", opts)
	run.Revision = digest
	run.SnapshotPath = path
	run.Status = controlplane.RunFailed
	run.Recoverable = true
	s.jobs.replace(run)
	mgr, err := s.stateManager("lab")
	require.NoError(t, err)
	g, _, st, _, _, err := runtime.LoadWorkspaceWithInputs(path, mgr, nil, driver.DefaultRegistry)
	require.NoError(t, err)
	repo, err := s.actionRepository("lab")
	require.NoError(t, err)
	addr := address.Resource(h.kind, "node")
	executor := runtime.NewExecutor(g, st, driver.DefaultRegistry)
	executor.SetRunContext("lab", run.ID)
	executor.SetRunIncarnation(run.QueuedAt, run.Revision)
	executor.SetActionRepository(rejectRecoveryCommit{repo})
	require.Error(t, executor.Apply(context.Background(), &runtime.Plan{Actions: []model.PlannedChange{{Address: addr, Action: model.PlanActionCreate}}}))
	require.Equal(t, 1, h.creates)
	return s, run, h, addr
}
func recoveryRequest(s *Server, runID, op string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/runs/"+runID+"/"+op, nil))
	return rec
}
func TestCanonicalRecoverUsesDurableActionAndPinnedRevision(t *testing.T) {
	s, run, h, addr := canonicalRecoveryFixture(t, false)
	rec := recoveryRequest(s, run.ID, "recover")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "recovered")
	repo, err := s.actionRepository("lab")
	require.NoError(t, err)
	st, err := repo.LoadState(context.Background(), "lab")
	require.NoError(t, err)
	require.NotNil(t, st.FindResource(addr))
	require.Equal(t, 1, h.creates)
	require.Zero(t, h.deletes)
}
func TestCanonicalRecoverUnknownNeverRestoresState(t *testing.T) {
	s, run, h, addr := canonicalRecoveryFixture(t, false)
	h.unknown = true
	rec := recoveryRequest(s, run.ID, "recover")
	require.GreaterOrEqual(t, rec.Code, 400)
	require.Contains(t, rec.Body.String(), "observation unknown")
	repo, err := s.actionRepository("lab")
	require.NoError(t, err)
	st, err := repo.LoadState(context.Background(), "lab")
	require.NoError(t, err)
	require.Nil(t, st.FindResource(addr))
	require.Equal(t, 1, h.creates)
}
func TestCanonicalRecoverDoesNotReplayUncertainProvisioner(t *testing.T) {
	s, run, h, _ := canonicalRecoveryFixture(t, true)
	rec := recoveryRequest(s, run.ID, "recover")
	require.GreaterOrEqual(t, rec.Code, 400)
	require.Contains(t, rec.Body.String(), "provisioner")
	require.Equal(t, 1, h.creates)
}
func TestCanonicalRecoverRejectsCheckpointOnlyEvidence(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	run := s.jobs.start("lab", "apply")
	run.Status = controlplane.RunFailed
	s.jobs.replace(run)
	cp := runtime.OperationCheckpoint{RunID: run.ID, Topology: "lab", StatePatches: []runtime.StatePatch{{Resource: "sysbox_node.web", Action: model.PlanActionCreate, Op: runtime.StatePatchUpsert, State: &runtime.StateResourceLog{Type: "sysbox_node", Name: "web", ExternalID: "unverified"}}}}
	require.NoError(t, s.apiStore.SaveCheckpoint(context.Background(), "lab", run.ID, cp))
	rec := recoveryRequest(s, run.ID, "recover")
	require.GreaterOrEqual(t, rec.Code, 400)
	mgr, err := s.stateManager("lab")
	require.NoError(t, err)
	st, err := mgr.Load()
	require.NoError(t, err)
	require.Empty(t, st.Resources)
}
func TestCanonicalCleanupRejectsNewerGeneration(t *testing.T) {
	s, run, h, addr := canonicalRecoveryFixture(t, false)
	require.Equal(t, http.StatusOK, recoveryRequest(s, run.ID, "recover").Code)
	mgr, err := s.stateManager("lab")
	require.NoError(t, err)
	st, err := mgr.Load()
	require.NoError(t, err)
	st.FindResource(addr).Generation++
	st.FindResource(addr).ExternalID = "new-instance"
	require.NoError(t, mgr.Save(st))
	rec := recoveryRequest(s, run.ID, "cleanup")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "identity_conflict")
	require.Zero(t, h.deletes)
}
func TestCanonicalCleanupUsesVerifiedDestroy(t *testing.T) {
	s, run, h, addr := canonicalRecoveryFixture(t, false)
	require.Equal(t, http.StatusOK, recoveryRequest(s, run.ID, "recover").Code)
	rec := recoveryRequest(s, run.ID, "cleanup")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, h.deletes)
	mgr, err := s.stateManager("lab")
	require.NoError(t, err)
	st, err := mgr.Load()
	require.NoError(t, err)
	require.Nil(t, st.FindResource(addr))
}
func TestCanonicalRecoverRefusesWrongExecutionHost(t *testing.T) {
	s, run, h, _ := canonicalRecoveryFixture(t, false, "remote-host")
	before := h.reads
	rec := recoveryRequest(s, run.ID, "recover")
	require.GreaterOrEqual(t, rec.Code, 400)
	require.Contains(t, rec.Body.String(), "owning agent")
	require.Equal(t, before, h.reads)
}

func TestSupervisorDriftDoesNotAuthorizeGenericApply(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	writeState(t, s.runsDir, "lab", &state.State{Version: state.SchemaVersion, Resources: []state.Resource{{Address: address.Resource("sysbox_kernel", "kernel"), Attributes: state.MustAttributes(map[string]any{"path": "/missing-canonical-recovery-kernel"})}}})
	supervisor := newSupervisor(s, time.Minute)
	supervisor.policy = SupervisorPolicyRestartOnCrash
	require.NoError(t, supervisor.ScanTopology(context.Background(), "lab"))
	require.Empty(t, s.jobs.list("lab"))
	snapshot, err := s.loadHealthSnapshot("lab")
	require.NoError(t, err)
	require.Equal(t, "approval_required", snapshot.Action)
	require.False(t, snapshot.AutoHeal)
}
func TestSupervisorVerifiesCanonicalPendingAction(t *testing.T) {
	s, run, h, addr := canonicalRecoveryFixture(t, false)
	supervisor := newSupervisor(s, time.Minute)
	require.NoError(t, supervisor.ScanTopology(context.Background(), "lab"))
	mgr, err := s.stateManager("lab")
	require.NoError(t, err)
	st, err := mgr.Load()
	require.NoError(t, err)
	require.NotNil(t, st.FindResource(addr))
	require.Equal(t, 1, h.creates)
	snapshot, err := s.loadHealthSnapshot("lab")
	require.NoError(t, err)
	require.Contains(t, snapshot.Recovered, run.ID)
}
