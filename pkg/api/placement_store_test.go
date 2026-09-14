package api

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestPlacementSharedSQLiteAcrossIndependentRunsDirectories(t *testing.T) {
	cfg := config.MustLoadServiceConfig("")
	cfg.State.Backend = "sqlite://" + filepath.Join(t.TempDir(), "api.db")
	cfg.Paths.RunsDir, cfg.Paths.WorkspacesDir = t.TempDir(), t.TempDir()
	a := NewServerWithConfig(cfg)
	cfg.Paths.RunsDir, cfg.Paths.WorkspacesDir = t.TempDir(), t.TempDir()
	b := NewServerWithConfig(cfg)
	ctx := context.Background()
	for _, id := range []string{"host-a", "host-b"} {
		require.NoError(t, a.agentService().Save(ctx, controlplane.Agent{ID: id, Status: controlplane.AgentStatusOnline, Capabilities: []string{"docker"}}))
	}
	first := a.jobs.startWithOptions("shared", "apply", runStartOptions{AgentID: "host-b"})
	require.NoError(t, a.scheduler.DispatchRun(ctx, first, []string{"docker"}))
	next := b.jobs.startWithOptions("shared", "reset", runStartOptions{})
	require.NoError(t, b.scheduler.DispatchRun(ctx, next, []string{"docker"}))
	require.Equal(t, "host-b", next.AgentID)
	conflict := b.jobs.startWithOptions("shared", "apply", runStartOptions{AgentID: "host-a"})
	require.ErrorContains(t, b.scheduler.DispatchRun(ctx, conflict, []string{"docker"}), "placed")
}

func TestDeleteTopologyReleasesDatabasePlacement(t *testing.T) {
	cfg := config.MustLoadServiceConfig("")
	cfg.State.Backend = "sqlite://" + filepath.Join(t.TempDir(), "api.db")
	cfg.Paths.RunsDir, cfg.Paths.WorkspacesDir = t.TempDir(), t.TempDir()
	s := NewServerWithConfig(cfg)
	w := s.workspaceService()
	require.NoError(t, w.bindPlacement("lab", "host-b", controlplane.AgentProtocolVersion))
	require.NoError(t, w.Delete(context.Background(), "lab", false))
	p, err := w.loadPlacement("lab")
	require.NoError(t, err)
	require.Nil(t, p)
}

func TestSQLiteSnapshotSurvivesClaimAndReload(t *testing.T) {
	s := &sqliteAPIStore{dbPath: filepath.Join(t.TempDir(), "api.db")}
	ctx := context.Background()
	want := controlplane.Run{ID: "snapshot", Topology: "lab", Status: controlplane.RunAssigned, AgentID: "host", SnapshotPath: "/immutable/field.sysbox.hcl"}
	require.NoError(t, s.SaveRun(ctx, want))
	got, err := s.GetRun(ctx, want.ID)
	require.NoError(t, err)
	require.Equal(t, want.SnapshotPath, got.SnapshotPath)
	runs, err := s.LoadRuns(ctx)
	require.NoError(t, err)
	require.Equal(t, want.SnapshotPath, runs[0].SnapshotPath)
	claimed, ok, err := s.ClaimRun(ctx, want.ID, want.AgentID, "owner", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, want.SnapshotPath, claimed.SnapshotPath)
}

func TestRecoveryChildPreservesSnapshot(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	parent := &controlplane.Run{ID: "parent", Topology: "lab", Op: "apply", SnapshotPath: "/immutable/field.sysbox.hcl"}
	child := s.jobs.startChild(parent)
	require.Equal(t, parent.SnapshotPath, child.SnapshotPath)
}

func TestPlacementBindingTimeDoesNotChangeOnReuse(t *testing.T) {
	s := &sqliteAPIStore{dbPath: filepath.Join(t.TempDir(), "api.db")}
	ctx := context.Background()
	p := topologyPlacement{Topology: "lab", AgentID: "host", Protocol: controlplane.AgentProtocolVersion, BoundAt: time.Now().UTC()}
	require.NoError(t, s.SaveTopologyPlacement(ctx, p))
	first := p.BoundAt
	p.BoundAt = p.BoundAt.Add(time.Hour)
	require.NoError(t, s.SaveTopologyPlacement(ctx, p))
	got, err := s.GetTopologyPlacement(ctx, p.Topology)
	require.NoError(t, err)
	require.Equal(t, first, got.BoundAt)
}

func TestPlacementLocalAgentIsNotTreatedAsUnspecified(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	ctx := context.Background()
	for _, id := range []string{"host-a", "local"} {
		require.NoError(t, s.agentService().Save(ctx, controlplane.Agent{ID: id, Status: controlplane.AgentStatusOnline, Capabilities: []string{"docker"}}))
	}
	require.NoError(t, s.workspaceService().bindPlacement("lab", "local", controlplane.AgentProtocolVersion))
	agent, err := s.scheduler.SelectAgentForTopology(ctx, "lab", []string{"docker"}, "")
	require.NoError(t, err)
	require.Equal(t, "local", agent.ID)
}

func TestSQLitePlacementConcurrentDatabaseBind(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.db")
	stores := []*sqliteAPIStore{{dbPath: path}, {dbPath: path}}
	for _, s := range stores {
		_, err := s.open()
		require.NoError(t, err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i, s := range stores {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- s.SaveTopologyPlacement(context.Background(), topologyPlacement{Topology: "race", AgentID: []string{"a", "b"}[i], BoundAt: time.Now().UTC()})
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else {
			require.Contains(t, err.Error(), "placed")
		}
	}
	require.Equal(t, 1, winners)
}
