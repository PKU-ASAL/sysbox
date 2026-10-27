package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/revisiongate"
	"github.com/stretchr/testify/require"
)

func fileBatchFixture(t *testing.T) (*Server, []string, RevisionRetentionPolicy) {
	t.Helper()
	s := NewServer(t.TempDir(), t.TempDir())
	ids := []string{}
	for _, value := range []string{"one", "two"} {
		files := map[string][]byte{"field.sysbox.hcl": []byte(value)}
		id := controlplane.ComputeRevisionDigest(files)
		ids = append(ids, id)
		require.NoError(t, s.apiStore.SaveGlobalRevision(context.Background(), controlplane.GlobalRevision{Revision: id, Files: files, CreatedAt: time.Now().Add(-time.Hour)}))
	}
	return s, ids, RevisionRetentionPolicy{OlderThan: time.Now(), Limit: 2}
}
func TestFileRevisionBatchConflictRollsBackAll(t *testing.T) {
	s, ids, policy := fileBatchFixture(t)
	ctx := context.Background()
	require.NoError(t, s.apiStore.SaveRun(ctx, controlplane.Run{ID: "late", Topology: "lab", Revision: ids[1], Status: controlplane.RunQueued, QueuedAt: time.Now()}))
	require.Error(t, s.deleteLocalRevisionBatch(ctx, ids, policy))
	for _, id := range ids {
		_, err := s.apiStore.GetGlobalRevision(ctx, id)
		require.NoError(t, err)
	}
}
func TestFileRevisionBatchCommitSurvivesUnremovedPayloads(t *testing.T) {
	s, ids, _ := fileBatchFixture(t)
	ctx := context.Background()
	root := filepath.Join(s.runsDir, "global-revisions")
	release, err := revisiongate.Lock(ctx, root)
	require.NoError(t, err)
	// Fault boundary: proof committed, process exits before any payload removal.
	require.NoError(t, revisiongate.RetireBatch(root, ids))
	release()
	cold := NewServer(s.runsDir, t.TempDir())
	for _, id := range ids {
		require.FileExists(t, filepath.Join(root, id+".json"))
		_, err = cold.apiStore.GetGlobalRevision(ctx, id)
		require.Error(t, err)
		require.Error(t, cold.apiStore.SaveRun(ctx, controlplane.Run{ID: "late", Topology: "lab", Revision: id, Status: controlplane.RunQueued, QueuedAt: time.Now()}))
	}
	all, err := cold.globalRevisionInventory(ctx)
	require.NoError(t, err)
	require.Empty(t, all)
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	for _, entry := range entries {
		if revisiongate.IsBatchProof(entry.Name()) {
			require.NoError(t, os.WriteFile(filepath.Join(root, entry.Name()), []byte(`[]`), 0600))
		}
	}
	_, err = cold.apiStore.GetGlobalRevision(ctx, ids[0])
	require.Error(t, err)
	require.Error(t, cold.apiStore.SaveRun(ctx, controlplane.Run{ID: "late", Topology: "lab", Revision: ids[0], Status: controlplane.RunQueued, QueuedAt: time.Now()}))
}
func TestFileRevisionBatchRacesRun(t *testing.T) {
	for range 8 {
		s, ids, policy := fileBatchFixture(t)
		ctx := context.Background()
		start := make(chan struct{})
		a, b := make(chan error, 1), make(chan error, 1)
		go func() { <-start; a <- s.deleteLocalRevisionBatch(ctx, ids, policy) }()
		go func() {
			<-start
			b <- s.apiStore.SaveRun(ctx, controlplane.Run{ID: "late", Topology: "lab", Revision: ids[1], Status: controlplane.RunQueued, QueuedAt: time.Now()})
		}()
		close(start)
		deleted, admitted := <-a, <-b
		require.False(t, deleted == nil && admitted == nil)
		require.True(t, deleted == nil || admitted == nil)
		_, err := s.apiStore.GetGlobalRevision(ctx, ids[0])
		if deleted == nil {
			require.Error(t, err)
		} else {
			require.NoError(t, err)
		}
	}
}

func TestFileRevisionBatchCleanupAfterInterruptedCommit(t *testing.T) {
	s, ids, _ := fileBatchFixture(t)
	ctx := context.Background()
	root := filepath.Join(s.runsDir, "global-revisions")
	release, err := revisiongate.Lock(ctx, root)
	require.NoError(t, err)
	require.NoError(t, revisiongate.RetireBatch(root, ids))
	release()
	count, err := s.cleanupLocalRevisionPayloads(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, count)
	for _, id := range ids {
		require.NoFileExists(t, filepath.Join(root, id+".json"))
		retired, err := revisiongate.Retired(root, id)
		require.NoError(t, err)
		require.True(t, retired)
	}
	count, err = s.cleanupLocalRevisionPayloads(ctx)
	require.NoError(t, err)
	require.Zero(t, count)
}

func TestBatchProofFilenameOutsideCatalogCannotHideReference(t *testing.T) {
	s, ids, policy := fileBatchFixture(t)
	require.NoError(t, writeLocalObject(filepath.Join(s.runsDir, "lab", "retirement-batch-fake.json"), map[string]string{"revision": ids[0]}))
	require.Error(t, s.deleteLocalRevisionBatch(context.Background(), ids, policy))
	for _, id := range ids {
		_, err := s.apiStore.GetGlobalRevision(context.Background(), id)
		require.NoError(t, err)
	}
}

func TestFileRevisionMissingBatchProofNeverReopensAdmission(t *testing.T) {
	s, ids, _ := fileBatchFixture(t)
	ctx := context.Background()
	root := filepath.Join(s.runsDir, "global-revisions")
	release, err := revisiongate.Lock(ctx, root)
	require.NoError(t, err)
	require.NoError(t, revisiongate.RetireBatch(root, ids))
	release()
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	for _, entry := range entries {
		if revisiongate.IsBatchProof(entry.Name()) {
			require.NoError(t, os.Remove(filepath.Join(root, entry.Name())))
		}
	}
	_, err = s.apiStore.GetGlobalRevision(ctx, ids[0])
	require.Error(t, err)
	require.Error(t, s.apiStore.SaveRun(ctx, controlplane.Run{ID: "late", Topology: "lab", Revision: ids[0], Status: controlplane.RunQueued, QueuedAt: time.Now()}))
}

func TestRetiredCleanupPreservesNonPayloadFile(t *testing.T) {
	s, ids, _ := fileBatchFixture(t)
	ctx := context.Background()
	root := filepath.Join(s.runsDir, "global-revisions")
	require.NoError(t, os.WriteFile(filepath.Join(root, ids[0]), []byte("keep unrelated"), 0600))
	release, err := revisiongate.Lock(ctx, root)
	require.NoError(t, err)
	require.NoError(t, revisiongate.RetireBatch(root, ids))
	release()
	_, err = s.cleanupLocalRevisionPayloads(ctx)
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(root, ids[0]))
}
