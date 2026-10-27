package api

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestFileRevisionRetirementFencesAllRecordWriters(t *testing.T) {
	ctx := context.Background()
	s := NewServer(t.TempDir(), t.TempDir())
	store := s.apiStore.(*localAPIStore)
	files := map[string][]byte{"field.sysbox.hcl": []byte("file revision")}
	id := controlplane.ComputeRevisionDigest(files)
	rev := controlplane.GlobalRevision{Revision: id, Files: files, CreatedAt: time.Now().Add(-time.Hour)}
	require.NoError(t, store.SaveGlobalRevision(ctx, rev))
	require.NoError(t, store.DeleteGlobalRevisionAtomic(ctx, id, time.Now()))
	_, err := store.GetGlobalRevision(ctx, id)
	require.ErrorIs(t, err, errGlobalRevisionNotFound)
	require.Error(t, store.SaveGlobalRevision(ctx, rev))
	require.Error(t, store.SaveRun(ctx, controlplane.Run{ID: "late", Topology: "lab", Revision: id, Status: controlplane.RunQueued, QueuedAt: time.Now()}))
	require.Error(t, store.SaveAgentCommand(ctx, controlplane.AgentCommand{ID: "cmd", AgentID: "agent", Type: "run_assigned", Status: controlplane.AgentCommandStatusQueued, Run: &controlplane.Run{ID: "late", Topology: "lab", Revision: id, QueuedAt: time.Now()}}))
	require.Error(t, writeLocalObject(store.checkpointFile("lab", "late"), map[string]string{"revision": id}))
	require.Error(t, store.DeleteGlobalRevisionAtomic(ctx, id, time.Now()))
}
func TestFileRevisionDeletionRacesRun(t *testing.T) {
	ctx := context.Background()
	s := NewServer(t.TempDir(), t.TempDir())
	store := s.apiStore.(*localAPIStore)
	for i := 0; i < 8; i++ {
		files := map[string][]byte{"field.sysbox.hcl": []byte(fmt.Sprint(i))}
		id := controlplane.ComputeRevisionDigest(files)
		require.NoError(t, store.SaveGlobalRevision(ctx, controlplane.GlobalRevision{Revision: id, Files: files, CreatedAt: time.Now().Add(-time.Hour)}))
		start := make(chan struct{})
		deleted, admitted := make(chan error, 1), make(chan error, 1)
		go func() { <-start; deleted <- store.DeleteGlobalRevisionAtomic(ctx, id, time.Now()) }()
		go func() {
			<-start
			admitted <- store.SaveRun(ctx, controlplane.Run{ID: fmt.Sprint(i), Topology: fmt.Sprintf("lab%d", i), Revision: id, Status: controlplane.RunQueued, QueuedAt: time.Now()})
		}()
		close(start)
		d, a := <-deleted, <-admitted
		require.False(t, d == nil && a == nil)
		require.True(t, d == nil || a == nil, "both operations unexpectedly failed")
	}
}

func TestIndependentFileRegistryProtectsAllEnrolledConsumers(t *testing.T) {
	ctx := context.Background()
	registry := filepath.Join(t.TempDir(), "registry")
	makeServer := func() *Server {
		cfg := config.DefaultServiceConfig()
		cfg.Paths.RunsDir = t.TempDir()
		cfg.Paths.WorkspacesDir = t.TempDir()
		cfg.RevisionRegistry = registry
		s := NewServerWithConfig(cfg)
		require.NoError(t, s.initErr)
		return s
	}
	a, b := makeServer(), makeServer()
	files := map[string][]byte{"field.sysbox.hcl": []byte("shared")}
	id := controlplane.ComputeRevisionDigest(files)
	require.NoError(t, a.apiStore.SaveGlobalRevision(ctx, controlplane.GlobalRevision{Revision: id, Files: files, CreatedAt: time.Now().Add(-time.Hour)}))
	require.NoError(t, b.apiStore.SaveRun(ctx, controlplane.Run{ID: "used", Topology: "other", Revision: id, Status: controlplane.RunDone, QueuedAt: time.Now(), EndedAt: time.Now()}))
	require.Error(t, a.apiStore.(*localAPIStore).DeleteGlobalRevisionAtomic(ctx, id, time.Now()))
	_, err := a.apiStore.GetGlobalRevision(ctx, id)
	require.NoError(t, err)
	secondFiles := map[string][]byte{"field.sysbox.hcl": []byte("unreferenced")}
	second := controlplane.ComputeRevisionDigest(secondFiles)
	require.NoError(t, a.apiStore.SaveGlobalRevision(ctx, controlplane.GlobalRevision{Revision: second, Files: secondFiles, CreatedAt: time.Now().Add(-time.Hour)}))
	require.NoError(t, a.apiStore.(*localAPIStore).DeleteGlobalRevisionAtomic(ctx, second, time.Now()))
	require.Error(t, b.apiStore.SaveRun(ctx, controlplane.Run{ID: "late", Topology: "other", Revision: second, Status: controlplane.RunQueued, QueuedAt: time.Now()}))
	require.Error(t, b.apiStore.SaveGlobalRevision(ctx, controlplane.GlobalRevision{Revision: second, Files: secondFiles, CreatedAt: time.Now()}))
	// A missing consumer never becomes proof of an empty inventory.
	require.NoError(t, os.Rename(b.runsDir, b.runsDir+"-offline"))
	t.Cleanup(func() { _ = os.Rename(b.runsDir+"-offline", b.runsDir) })
	require.Error(t, a.apiStore.(*localAPIStore).DeleteGlobalRevisionAtomic(ctx, id, time.Now()))
}
func TestFileRevisionProtectsEncodedSnapshotAndLifecycleWrites(t *testing.T) {
	ctx := context.Background()
	s := NewServer(t.TempDir(), t.TempDir())
	store := s.apiStore.(*localAPIStore)
	files := map[string][]byte{"field.sysbox.hcl": []byte("snap")}
	id := controlplane.ComputeRevisionDigest(files)
	require.NoError(t, store.SaveGlobalRevision(ctx, controlplane.GlobalRevision{Revision: id, Files: files, CreatedAt: time.Now().Add(-time.Hour)}))
	raw, _ := json.Marshal(map[string]string{"revision": id})
	path := filepath.Join(store.runsDir, "lab", "retained.json")
	require.NoError(t, writeLocalObject(path, map[string][]byte{"data": raw}))
	require.Error(t, store.DeleteGlobalRevisionAtomic(ctx, id, time.Now()))
	require.NoError(t, os.Remove(path))
	require.NoError(t, store.DeleteGlobalRevisionAtomic(ctx, id, time.Now()))
	require.Error(t, writeLocalObject(path, map[string][]byte{"data": raw}))
}

func TestFileRevisionLifecycleMutationCannotBypassRetirement(t *testing.T) {
	ctx := context.Background()
	s := NewServer(t.TempDir(), t.TempDir())
	store := s.apiStore.(*localAPIStore)
	files := map[string][]byte{"field.sysbox.hcl": []byte("late lifecycle")}
	id := controlplane.ComputeRevisionDigest(files)
	require.NoError(t, store.SaveGlobalRevision(ctx, controlplane.GlobalRevision{Revision: id, Files: files, CreatedAt: time.Now().Add(-time.Hour)}))
	repo, err := s.actionRepository("lab")
	require.NoError(t, err)
	st, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	require.NoError(t, store.DeleteGlobalRevisionAtomic(ctx, id, time.Now()))
	v, err := repo.BeginGCSource(ctx, "lab", "host", st.TopologyUID, 0)
	require.NoError(t, err)
	err = repo.CompleteGCSource(ctx, "lab", "host", st.TopologyUID, v, persistence.GCSourceManifest{Domains: map[string][]string{"revision": {id}, "artifact": {}, "cache": {}, "placement": {}}, ValidUntil: time.Now().Add(time.Hour)})
	require.Error(t, err)
}

func TestIndependentRegistryMissingEnrollmentNeverMeansEmpty(t *testing.T) {
	ctx := context.Background()
	cfg := config.DefaultServiceConfig()
	cfg.Paths.RunsDir = t.TempDir()
	cfg.Paths.WorkspacesDir = t.TempDir()
	cfg.RevisionRegistry = filepath.Join(t.TempDir(), "registry")
	s := NewServerWithConfig(cfg)
	require.NoError(t, s.initErr)
	files := map[string][]byte{"field.sysbox.hcl": []byte("manifest")}
	id := controlplane.ComputeRevisionDigest(files)
	require.NoError(t, s.apiStore.SaveGlobalRevision(ctx, controlplane.GlobalRevision{Revision: id, Files: files, CreatedAt: time.Now().Add(-time.Hour)}))
	require.NoError(t, os.Remove(filepath.Join(cfg.RevisionRegistry, "consumers.json")))
	require.Error(t, s.apiStore.(*localAPIStore).DeleteGlobalRevisionAtomic(ctx, id, time.Now()))
	_, err := s.apiStore.GetGlobalRevision(ctx, id)
	require.NoError(t, err)
}

func TestFileRevisionRetirementReclaimsPayloadAndKeepsProof(t *testing.T) {
	ctx := context.Background()
	s := NewServer(t.TempDir(), t.TempDir())
	store := s.apiStore.(*localAPIStore)
	files := map[string][]byte{"field.sysbox.hcl": []byte("# large"), "payload": make([]byte, 1<<20)}
	id := controlplane.ComputeRevisionDigest(files)
	require.NoError(t, store.SaveGlobalRevision(ctx, controlplane.GlobalRevision{Revision: id, Files: files, CreatedAt: time.Now().Add(-time.Hour)}))
	require.NoError(t, store.DeleteGlobalRevisionAtomic(ctx, id, time.Now()))
	info, err := os.Stat(filepath.Join(store.runsDir, "global-revisions", id+".json.retired"))
	require.NoError(t, err)
	require.Less(t, info.Size(), int64(1024))
	require.NoFileExists(t, filepath.Join(store.runsDir, "global-revisions", id+".json"))
}

func TestFileRevisionColdProcessCannotReviveRetirement(t *testing.T) {
	if os.Getenv("SYSBOX_REVISION_CHILD_ROOT") != "" {
		root := os.Getenv("SYSBOX_REVISION_CHILD_ROOT")
		id := os.Getenv("SYSBOX_REVISION_CHILD_ID")
		store := &localAPIStore{runsDir: root}
		err := store.SaveRun(context.Background(), controlplane.Run{ID: "late-child", Topology: "lab", Revision: id, Status: controlplane.RunQueued, QueuedAt: time.Now()})
		if err == nil {
			os.Exit(2)
		}
		os.Exit(73)
	}
	ctx := context.Background()
	s := NewServer(t.TempDir(), t.TempDir())
	store := s.apiStore.(*localAPIStore)
	files := map[string][]byte{"field.sysbox.hcl": []byte("cold")}
	id := controlplane.ComputeRevisionDigest(files)
	require.NoError(t, store.SaveGlobalRevision(ctx, controlplane.GlobalRevision{Revision: id, Files: files, CreatedAt: time.Now().Add(-time.Hour)}))
	require.NoError(t, store.DeleteGlobalRevisionAtomic(ctx, id, time.Now()))
	child := exec.Command(os.Args[0], "-test.run=^TestFileRevisionColdProcessCannotReviveRetirement$")
	child.Env = append(os.Environ(), "SYSBOX_REVISION_CHILD_ROOT="+store.runsDir, "SYSBOX_REVISION_CHILD_ID="+id)
	raw, err := child.CombinedOutput()
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit, string(raw))
	require.Equal(t, 73, exit.ExitCode())
	_, err = store.GetRun(ctx, "late-child")
	require.Error(t, err)
}

func TestDefaultRegistryMissingEnrollmentNeverMeansEmpty(t *testing.T) {
	ctx := context.Background()
	cfg := config.DefaultServiceConfig()
	cfg.Paths.RunsDir = t.TempDir()
	cfg.Paths.WorkspacesDir = t.TempDir()
	cfg.RevisionRegistry = filepath.Join(cfg.Paths.RunsDir, "global-revisions")
	a := NewServerWithConfig(cfg)
	require.NoError(t, a.initErr)
	cfg.Paths.RunsDir = t.TempDir()
	b := NewServerWithConfig(cfg)
	require.NoError(t, b.initErr)
	files := map[string][]byte{"field.sysbox.hcl": []byte("shared default")}
	id := controlplane.ComputeRevisionDigest(files)
	require.NoError(t, a.apiStore.SaveGlobalRevision(ctx, controlplane.GlobalRevision{Revision: id, Files: files, CreatedAt: time.Now().Add(-time.Hour)}))
	require.NoError(t, b.apiStore.SaveRun(ctx, controlplane.Run{ID: "used", Topology: "peer", Revision: id, Status: controlplane.RunQueued, QueuedAt: time.Now()}))
	require.NoError(t, os.Remove(filepath.Join(cfg.RevisionRegistry, "consumers.json")))
	require.Error(t, a.apiStore.(*localAPIStore).DeleteGlobalRevisionAtomic(ctx, id, time.Now()))
	_, err := a.apiStore.GetGlobalRevision(ctx, id)
	require.NoError(t, err)
}

func TestIndependentRegistryMissingManifestCannotRepublish(t *testing.T) {
	ctx := context.Background()
	cfg := config.DefaultServiceConfig()
	cfg.Paths.RunsDir = t.TempDir()
	cfg.Paths.WorkspacesDir = t.TempDir()
	cfg.RevisionRegistry = filepath.Join(t.TempDir(), "registry")
	s := NewServerWithConfig(cfg)
	require.NoError(t, s.initErr)
	store := s.apiStore.(*localAPIStore)
	files := map[string][]byte{"field.sysbox.hcl": []byte("retired")}
	id := controlplane.ComputeRevisionDigest(files)
	rev := controlplane.GlobalRevision{Revision: id, Files: files, CreatedAt: time.Now().Add(-time.Hour)}
	require.NoError(t, store.SaveGlobalRevision(ctx, rev))
	require.NoError(t, store.DeleteGlobalRevisionAtomic(ctx, id, time.Now()))
	require.NoError(t, os.Remove(filepath.Join(cfg.RevisionRegistry, "consumers.json")))
	require.Error(t, store.SaveGlobalRevision(ctx, rev))
	require.NoFileExists(t, filepath.Join(cfg.RevisionRegistry, id+".json"))
}
func TestFileRevisionRetainsResetReturnEvidence(t *testing.T) {
	ctx := context.Background()
	s := NewServer(t.TempDir(), t.TempDir())
	store := s.apiStore.(*localAPIStore)
	files := map[string][]byte{"field.sysbox.hcl": []byte("returned")}
	id := controlplane.ComputeRevisionDigest(files)
	require.NoError(t, store.SaveGlobalRevision(ctx, controlplane.GlobalRevision{Revision: id, Files: files, CreatedAt: time.Now().Add(-time.Hour)}))
	require.NoError(t, writeLocalObject(filepath.Join(store.runsDir, "lab", "old.checkpoint.json.returned"), map[string]string{"revision": id}))
	require.Error(t, store.DeleteGlobalRevisionAtomic(ctx, id, time.Now()))
}

func TestFileRevisionRetainsReturnedProviderProof(t *testing.T) {
	ctx := context.Background()
	s := NewServer(t.TempDir(), t.TempDir())
	store := s.apiStore.(*localAPIStore)
	files := map[string][]byte{"field.sysbox.hcl": []byte("returned provider")}
	id := controlplane.ComputeRevisionDigest(files)
	require.NoError(t, store.SaveGlobalRevision(ctx, controlplane.GlobalRevision{Revision: id, Files: files, CreatedAt: time.Now().Add(-time.Hour)}))
	require.NoError(t, writeLocalObject(filepath.Join(store.runsDir, "host", "returned-operation.proof.json"), map[string]any{"result": map[string]string{"revision": id}}))
	require.Error(t, store.DeleteGlobalRevisionAtomic(ctx, id, time.Now()))
}
