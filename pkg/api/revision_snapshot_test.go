package api

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestRevisionSnapshotRejectsDigestMismatch(t *testing.T) {
	w := NewServer(t.TempDir(), t.TempDir()).workspaceService()
	files := map[string][]byte{"field.sysbox.hcl": []byte("original")}
	digest := controlplane.ComputeRevisionDigest(files)
	files["field.sysbox.hcl"] = []byte("changed")
	_, err := w.MaterializeRevision("lab", digest, files)
	require.Error(t, err)
}

func TestRevisionSnapshotPublishesWholeTreeConcurrently(t *testing.T) {
	w := NewServer(t.TempDir(), t.TempDir()).workspaceService()
	files := map[string][]byte{"field.sysbox.hcl": []byte("root"), "modules/net/main.hcl": []byte("module")}
	digest := controlplane.ComputeRevisionDigest(files)
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			path, err := w.MaterializeRevision("lab", digest, files)
			if err == nil {
				_, err = os.Stat(filepath.Join(filepath.Dir(path), "modules/net/main.hcl"))
			}
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
}
