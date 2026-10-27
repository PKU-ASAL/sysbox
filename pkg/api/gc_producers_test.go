package api

import (
	"context"
	"github.com/oslab/sysbox/pkg/artifact"
	"github.com/oslab/sysbox/pkg/substrate"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestGCProductionWorkspaceAndPlacementPublishBeforeUse(t *testing.T) {
	ctx := context.Background()
	cfg := config.DefaultServiceConfig()
	cfg.Paths.RunsDir = t.TempDir()
	cfg.Paths.WorkspacesDir = t.TempDir()
	cfg.State.Backend = "sqlite://" + filepath.Join(t.TempDir(), "state.db")
	cfg.GCPublicationSource = "api-a"
	s := NewServerWithConfig(cfg)
	require.NoError(t, s.initErr)
	files := map[string][]byte{"field.sysbox.hcl": []byte("# first")}
	require.NoError(t, s.workspaceService().UpsertProject(ctx, "lab", files))
	require.NoError(t, s.workspaceService().bindPlacement("lab", "agent-a", controlplane.AgentProtocolVersion))
	repo, err := s.actionRepository("lab")
	require.NoError(t, err)
	sources, err := repo.LoadGCSources(ctx, "lab")
	require.NoError(t, err)
	require.Contains(t, sources["api-a-workspace"].Manifest.Domains["revision"], controlplane.ComputeRevisionDigest(files))
	require.Equal(t, []string{"agent-a"}, sources["api-a-placement"].Manifest.Domains["placement"])
	refs, err := repo.LoadGCReferences(ctx, "lab")
	require.NoError(t, err)
	c := controlplane.GCCandidate{Kind: "container", ExternalID: "orphan", TopologyUID: refs.TopologyUID, OwnerUID: refs.TopologyUID, ResourceUID: "orphan", Generation: 1, InventoryDigest: refs.Digest}
	require.NoError(t, repo.ReserveGC(ctx, "lab", persistence.GCReservation{ID: "gc", AgentID: "agent-a", Candidate: c}))
	require.ErrorIs(t, s.workspaceService().UpsertProject(ctx, "lab", map[string][]byte{"field.sysbox.hcl": []byte("# second")}), persistence.ErrConflict)
	content, err := s.workspaceService().HCL("lab")
	require.NoError(t, err)
	require.Equal(t, files["field.sysbox.hcl"], content)
	require.ErrorIs(t, s.workspaceService().bindPlacement("lab", "agent-b", controlplane.AgentProtocolVersion), persistence.ErrConflict)
}

func TestGCProductionArtifactResolutionPublishesAndFencesCache(t *testing.T) {
	ctx := context.Background()
	cfg := config.DefaultServiceConfig()
	cfg.GCPublicationSource = "host-a"
	cfg.Paths.RunsDir = t.TempDir()
	cfg.Paths.WorkspacesDir = t.TempDir()
	bridge := NewExecutionBridge(cfg)
	run := &controlplane.Run{Topology: "lab"}
	ctx, release, err := bridge.PrepareExecution(ctx, run)
	require.NoError(t, err)
	defer release()
	repo, err := bridge.server.actionRepository("lab")
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := repo.LoadGCReferences(context.Background(), "lab")
		require.ErrorIs(t, err, persistence.ErrConflict)
		_, _ = w.Write([]byte("kernel"))
	}))
	defer server.Close()
	resolver := artifact.NewWithContext(ctx)
	resolver.CacheDir = t.TempDir()
	resolved, err := resolver.Resolve(artifact.Spec{Source: server.URL + "/kernel"})
	require.NoError(t, err)
	sources, err := repo.LoadGCSources(ctx, "lab")
	require.NoError(t, err)
	require.Contains(t, sources["host-a-artifacts"].Manifest.Domains["artifact"], "sha256:"+resolved.SHA256)
	refs, err := repo.LoadGCReferences(ctx, "lab")
	require.NoError(t, err)
	candidate := controlplane.GCCandidate{Kind: "container", ExternalID: "orphan", TopologyUID: refs.TopologyUID, OwnerUID: refs.TopologyUID, ResourceUID: "orphan", Generation: 1, InventoryDigest: refs.Digest}
	require.NoError(t, repo.ReserveGC(ctx, "lab", persistence.GCReservation{ID: "gc", AgentID: "agent", Candidate: candidate}))
	_, err = resolver.ResolveIdentity(artifact.IdentitySpec{Kind: substrate.ArtifactKernel, Source: server.URL + "/kernel", Architecture: "amd64", GuestFamily: substrate.GuestFamilyUnknown})
	require.ErrorIs(t, err, persistence.ErrConflict)
	raw, err := os.ReadFile(resolved.Path)
	require.NoError(t, err)
	require.Equal(t, "kernel", string(raw), "GC rejection precedes mutable URL invalidation")
}

func TestGCProductionPublisherCrashRemainsIncomplete(t *testing.T) {
	if os.Getenv("SYSBOX_GC_PRODUCER_CHILD") == "1" {
		cfg := config.DefaultServiceConfig()
		cfg.GCPublicationSource = "crash-host"
		cfg.Paths.RunsDir = os.Getenv("SYSBOX_GC_PRODUCER_ROOT")
		cfg.Paths.WorkspacesDir = filepath.Join(cfg.Paths.RunsDir, "workspaces")
		bridge := NewExecutionBridge(cfg)
		ctx, release, err := bridge.PrepareExecution(context.Background(), &controlplane.Run{Topology: "lab"})
		require.NoError(t, err)
		defer release()
		resolver := artifact.NewWithContext(ctx)
		resolver.CacheDir = filepath.Join(cfg.Paths.RunsDir, "cache")
		_, err = resolver.Resolve(artifact.Spec{Source: os.Getenv("SYSBOX_GC_PRODUCER_URL")})
		t.Fatalf("child must be killed while downloading: %v", err)
	}
	root := t.TempDir()
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("partial"))
		w.(http.Flusher).Flush()
		close(entered)
		<-r.Context().Done()
	}))
	defer server.Close()
	child := exec.Command(os.Args[0], "-test.run=^TestGCProductionPublisherCrashRemainsIncomplete$")
	child.Env = append(os.Environ(), "SYSBOX_GC_PRODUCER_CHILD=1", "SYSBOX_GC_PRODUCER_ROOT="+root, "SYSBOX_GC_PRODUCER_URL="+server.URL+"/image")
	require.NoError(t, child.Start())
	defer func() { _ = child.Process.Kill() }()
	select {
	case <-entered:
	case <-time.After(15 * time.Second):
		t.Fatal("child did not reach actual download")
	}
	require.NoError(t, child.Process.Kill())
	require.Error(t, child.Wait())
	cfg := config.DefaultServiceConfig()
	cfg.GCPublicationSource = "crash-host"
	cfg.Paths.RunsDir = root
	cfg.Paths.WorkspacesDir = filepath.Join(root, "workspaces")
	cold := NewExecutionBridge(cfg)
	repo, err := cold.server.actionRepository("lab")
	require.NoError(t, err)
	_, err = repo.LoadGCReferences(context.Background(), "lab")
	require.ErrorIs(t, err, persistence.ErrConflict)
	sources, err := repo.LoadGCSources(context.Background(), "lab")
	require.NoError(t, err)
	require.True(t, sources["crash-host-artifacts"].Updating)
	ctx, release, err := cold.PrepareExecution(context.Background(), &controlplane.Run{Topology: "lab"})
	require.NoError(t, err)
	defer release()
	_, err = artifact.NewWithContext(ctx).Resolve(artifact.Spec{Source: server.URL + "/image"})
	require.ErrorIs(t, err, persistence.ErrConflict)
}

func TestGCProductionMissingConfigurationCannotBypassEnrollment(t *testing.T) {
	cfg := config.DefaultServiceConfig()
	cfg.GCPublicationSource = "producer"
	cfg.Paths.RunsDir = t.TempDir()
	cfg.Paths.WorkspacesDir = t.TempDir()
	first := NewServerWithConfig(cfg)
	files := map[string][]byte{"field.sysbox.hcl": []byte("# retained")}
	require.NoError(t, first.workspaceService().UpsertProject(context.Background(), "lab", files))
	cfg.GCPublicationSource = ""
	cold := NewServerWithConfig(cfg)
	require.Error(t, cold.workspaceService().UpsertProject(context.Background(), "lab", files))
	require.Error(t, cold.workspaceService().bindPlacement("lab", "other", controlplane.AgentProtocolVersion))
	bridge := NewExecutionBridge(cfg)
	_, _, err := bridge.PrepareExecution(context.Background(), &controlplane.Run{Topology: "lab"})
	require.Error(t, err)
}

func TestGCProductionIndependentWorkspacesSharePublicationAuthority(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		a := stateDeleteTestServer(t, store)
		b := stateDeleteTestServer(t, store)
		for id, s := range map[string]*Server{"api-a": a, "host-b": b} {
			s.workspacesDir = t.TempDir()
			s.workspaces = nil
			s.cfg.GCPublicationSource = id
		}
		first := map[string][]byte{"field.sysbox.hcl": []byte("# api revision")}
		second := map[string][]byte{"field.sysbox.hcl": []byte("# host revision")}
		require.NoError(t, a.workspaceService().UpsertProject(ctx, "lab", first))
		_, err := b.workspaceService().MaterializeRevision("lab", controlplane.ComputeRevisionDigest(second), second)
		require.NoError(t, err)
		repo, err := a.actionRepository("lab")
		require.NoError(t, err)
		sources, err := repo.LoadGCSources(ctx, "lab")
		require.NoError(t, err)
		require.Contains(t, sources, "api-a-workspace")
		require.Contains(t, sources, "host-b-workspace")
		require.Contains(t, sources["api-a-workspace"].Manifest.Domains["revision"], controlplane.ComputeRevisionDigest(first))
		require.Contains(t, sources["host-b-workspace"].Manifest.Domains["revision"], controlplane.ComputeRevisionDigest(second))
	})
}
