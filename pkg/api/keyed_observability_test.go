package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/workspace"
	"github.com/stretchr/testify/require"
)

// sharedStateServer mirrors the deployed layout: a shared SQLite/PostgreSQL
// lifecycle catalog with no mutable workspace pointer for keyed Apply.
func sharedStateServer(t *testing.T) *Server {
	t.Helper()
	cfg := config.DefaultServiceConfig()
	cfg.Paths.RunsDir = t.TempDir()
	cfg.Paths.WorkspacesDir = t.TempDir()
	cfg.State.Backend = "sqlite://" + filepath.Join(t.TempDir(), "api.db")
	return NewServerWithConfig(cfg)
}

func listedTopology(t *testing.T, s *Server, name string) *workspace.Info {
	t.Helper()
	items, err := s.workspaceService().List(context.Background())
	require.NoError(t, err)
	for i := range items {
		if items[i].Name == name {
			return &items[i]
		}
	}
	return nil
}

func findCondition(t *testing.T, status *controlplane.TopologyStatus, typ string) controlplane.Condition {
	t.Helper()
	for _, c := range status.Conditions {
		if c.Type == typ {
			return c
		}
	}
	t.Fatalf("condition %q missing from %+v", typ, status.Conditions)
	return controlplane.Condition{}
}

// commitHealthyKeyedState records one committed resource for a topology that
// has no workspace pointer. This is what a successful keyed Apply leaves behind
// in the shared lifecycle catalog.
func commitHealthyKeyedState(t *testing.T, s *Server, topology string) {
	t.Helper()
	kernel := filepath.Join(t.TempDir(), "vmlinux")
	require.NoError(t, os.WriteFile(kernel, []byte("kernel"), 0o644))
	mgr, err := s.stateManager(topology)
	require.NoError(t, err)
	canonical, err := mgr.LoadWithContext(context.Background())
	require.NoError(t, err)
	canonical.Resources = []state.Resource{{
		Address:     address.Resource("sysbox_kernel", "linux"),
		Driver:      "artifact",
		OwnerUID:    canonical.TopologyUID,
		ResourceUID: canonical.TopologyUID + "/sysbox_kernel.linux",
		Generation:  1,
		Attributes:  map[string]any{"path": kernel},
	}}
	require.NoError(t, mgr.SaveWithContext(context.Background(), canonical))
}

// A keyed Apply never publishes a workspace pointer, so observability must be
// derived from durable facts (an active Run, committed lifecycle state, or a
// placement) rather than from has_hcl. This is the exact shape the deployed
// control plane sees: has_hcl=false, has_state=true, ready conditions true.
func TestKeyedApplyTopologyIsObservableWithoutWorkspacePointer(t *testing.T) {
	s := sharedStateServer(t)
	registerDockerAgent(t, s)
	ctx := context.Background()

	rev := publishRevision(t, s, upsertApplyHCL)
	response := keyedApply(t, s, rev, "observe-1", "canary")
	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	var started struct {
		RunID string `json:"run_id"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &started))
	require.NotEmpty(t, started.RunID)

	// While the Run is in flight, the supervisor can already enumerate the
	// topology from the durable Run, without any HCL copied for it.
	require.Contains(t, mustNames(t, s), "keyed")
	inflight := listedTopology(t, s, "keyed")
	require.NotNil(t, inflight)
	require.False(t, inflight.HasHCL)
	require.False(t, inflight.HasState)
	require.Equal(t, started.RunID, inflight.ActiveRunID)

	// The host completes the Apply and commits state; the workspace pointer is
	// still absent by design.
	claimed, err := s.jobs.claim(started.RunID, "host-a")
	require.NoError(t, err)
	claimed.Assertion = &controlplane.AssertionResult{}
	require.NoError(t, s.jobs.finish(claimed, nil))
	commitHealthyKeyedState(t, s, "keyed")

	completed := listedTopology(t, s, "keyed")
	require.NotNil(t, completed, "a keyed topology with committed state must stay in the catalog")
	require.False(t, completed.HasHCL, "keyed Apply must not publish a mutable workspace pointer")
	require.True(t, completed.HasState)
	require.Empty(t, completed.ActiveRunID)

	// The background supervisor must observe it without HCL and persist health.
	supervisor := newSupervisor(s, time.Minute)
	supervisor.Scan(ctx)
	snapshot, err := s.loadHealthSnapshot("keyed")
	require.NoError(t, err, "health observation must not depend on a workspace pointer")
	require.Equal(t, model.ResourceHealthHealthy, snapshot.Health.Status)

	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/topologies/keyed", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var info workspace.Info
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &info))
	require.NotNil(t, info.Status)
	require.False(t, info.HasHCL)
	require.True(t, info.HasState)
	require.Equal(t, controlplane.ConditionTrue, findCondition(t, info.Status, controlplane.ConditionApplied).Status)
	require.Equal(t, controlplane.ConditionTrue, findCondition(t, info.Status, controlplane.ConditionProvisioned).Status)
	require.Equal(t, controlplane.ConditionTrue, findCondition(t, info.Status, controlplane.ConditionAsserted).Status)
	require.Equal(t, controlplane.ConditionTrue, findCondition(t, info.Status, controlplane.ConditionReady).Status)
	require.Equal(t, controlplane.PhaseReady, info.Status.Phase)

	// Deleting the metadata must remove it for good: retained terminal Run
	// history cannot resurrect an HCL-less topology after placement is deleted.
	stored, err := s.apiStore.GetRun(ctx, started.RunID)
	require.NoError(t, err)
	require.NoError(t, s.apiStore.DeleteAgentCommand(ctx, "host-a", controlstore.RunCommandID(*stored)))
	require.NoError(t, s.workspaceService().Delete(ctx, "keyed", true))
	require.Nil(t, listedTopology(t, s, "keyed"))
	require.NotContains(t, mustNames(t, s), "keyed")
}

func mustNames(t *testing.T, s *Server) []string {
	t.Helper()
	names, err := s.workspaceService().Names(context.Background())
	require.NoError(t, err)
	return names
}

// The default filesystem lifecycle backend stores committed aggregates in
// runs/<topology>/state.json.lifecycle. Catalog enumeration must find a keyed
// topology there too, not only in a shared database.
func TestKeyedTopologyObservableOnFilesystemBackend(t *testing.T) {
	cfg := config.DefaultServiceConfig()
	cfg.Paths.RunsDir = t.TempDir()
	cfg.Paths.WorkspacesDir = t.TempDir()
	s := NewServerWithConfig(cfg)
	ctx := context.Background()

	apply := s.jobs.start("keyed", "apply")
	apply.Status = controlplane.RunDone
	apply.EndedAt = time.Now().UTC()
	apply.Assertion = &controlplane.AssertionResult{}
	s.jobs.replace(apply)
	commitHealthyKeyedState(t, s, "keyed")

	info := listedTopology(t, s, "keyed")
	require.NotNil(t, info)
	require.False(t, info.HasHCL)
	require.True(t, info.HasState)

	supervisor := newSupervisor(s, time.Minute)
	supervisor.Scan(ctx)
	snapshot, err := s.loadHealthSnapshot("keyed")
	require.NoError(t, err)
	require.Equal(t, model.ResourceHealthHealthy, snapshot.Health.Status)

	status := s.topologyStatus("keyed")
	require.Equal(t, controlplane.ConditionTrue, findCondition(t, status, controlplane.ConditionReady).Status)
}

// A Destroy Run's terminal verdict says nothing about whether the desired
// revision was applied. A completed destroy must not turn Applied into True,
// otherwise a released keyed topology can be reported as ready.
func TestDestroyRunIsNotAnAppliedVerdict(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())

	apply := s.jobs.start("keyed", "apply")
	apply.Status = controlplane.RunDone
	apply.EndedAt = time.Now().UTC().Add(-time.Minute)
	apply.Assertion = &controlplane.AssertionResult{}
	s.jobs.replace(apply)
	require.NoError(t, s.saveHealthSnapshot("keyed", controlstore.HealthSnapshot{Topology: "keyed", Health: model.TopologyHealth{Status: model.ResourceHealthHealthy}}))

	status := s.topologyStatus("keyed")
	require.Equal(t, controlplane.ConditionTrue, findCondition(t, status, controlplane.ConditionApplied).Status)

	destroy := s.jobs.start("keyed", "destroy")
	destroy.Status = controlplane.RunDone
	destroy.EndedAt = time.Now().UTC()
	s.jobs.replace(destroy)

	status = s.topologyStatus("keyed")
	require.NotEqual(t, controlplane.ConditionTrue, findCondition(t, status, controlplane.ConditionApplied).Status,
		"a terminal destroy must not be reported as a successful apply")
	require.NotEqual(t, controlplane.ConditionTrue, findCondition(t, status, controlplane.ConditionReady).Status)
}
