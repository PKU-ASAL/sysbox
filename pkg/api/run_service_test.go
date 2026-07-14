package api

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/controlplane"
)

func TestRunServiceStartApplyDispatchesAssignedRun(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	writeRunServiceTopology(t, s, "lab", `resource "sysbox_network" "lab" {
  cidr = "10.77.0.0/24"
}`)
	require.NoError(t, s.agentService().Save(context.Background(), controlplane.Agent{
		ID:           "host-a",
		Status:       controlplane.AgentStatusOnline,
		Capabilities: []string{"network"},
	}))

	run, err := s.runs().StartApply(context.Background(), "lab", RunStartRequest{})
	require.NoError(t, err)
	require.Equal(t, "lab", run.Topology)
	require.Equal(t, "apply", run.Op)
	require.Equal(t, "host-a", run.AgentID)
	require.Equal(t, controlplane.RunAssigned, run.Status)
}

func TestRunServiceStartResetPersistsExactTargetAndDispatches(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	writeRunServiceTopology(t, s, "lab", `resource "sysbox_network" "lab" {
  cidr = "10.77.0.0/24"
}`)
	require.NoError(t, s.agentService().Save(context.Background(), controlplane.Agent{
		ID: "host-a", Status: controlplane.AgentStatusOnline, Capabilities: []string{"network"},
	}))

	run, err := s.runs().StartReset(context.Background(), "lab", RunStartRequest{Target: "sysbox_node.web"})
	require.NoError(t, err)
	require.Equal(t, "reset", run.Op)
	require.Equal(t, "sysbox_node.web", run.Target)
	require.Equal(t, controlplane.RunAssigned, run.Status)
}

func TestRunServiceResumeAllowsFailedReset(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	writeRunServiceTopology(t, s, "lab", `resource "sysbox_network" "lab" {
  cidr = "10.77.0.0/24"
}`)
	require.NoError(t, s.agentService().Save(context.Background(), controlplane.Agent{
		ID: "host-a", Status: controlplane.AgentStatusOnline, Capabilities: []string{"network"},
	}))
	parent := s.jobs.startWithOptions("lab", "reset", runStartOptions{Target: "sysbox_node.web", UnsafeState: true})
	s.jobs.finish(parent, errors.New("interrupted"))

	run, _, err := s.runs().Resume(context.Background(), parent.ID)
	require.NoError(t, err)
	require.Equal(t, "reset", run.Op)
	require.Equal(t, "sysbox_node.web", run.Target)
	require.True(t, run.UnsafeState)
}

func TestRunServiceResumeRejectsRunningParent(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	parent := s.jobs.start("lab", "apply")
	s.jobs.markRunning(parent)

	run, gotParent, err := s.runs().Resume(context.Background(), parent.ID)
	require.Nil(t, run)
	require.Equal(t, parent.ID, gotParent.ID)
	require.ErrorContains(t, err, "still running")
	require.Equal(t, http.StatusConflict, runServiceStatus(err))
}

func writeRunServiceTopology(t *testing.T, s *Server, topology, hcl string) {
	t.Helper()
	path := s.workspaceService().HCLFile(topology)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(hcl), 0o644))
}
