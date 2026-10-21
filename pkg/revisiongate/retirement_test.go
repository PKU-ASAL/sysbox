package revisiongate

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBatchIndexCommitsWholeProofAndDetectsMissingIndex(t *testing.T) {
	root := t.TempDir()
	ids := []string{"sha256:" + strings.Repeat("a", 64), "sha256:" + strings.Repeat("b", 64)}
	raw, err := json.Marshal(ids)
	require.NoError(t, err)
	proof := filepath.Join(root, fmt.Sprintf("%s%x.json", batchPrefix, sha256.Sum256(raw)))
	// Pre-index crash: a fully durable proof is not a committed retirement.
	require.NoError(t, writeRegistryRecord(proof, ids))
	for _, id := range ids {
		retired, err := Retired(root, id)
		require.NoError(t, err)
		require.False(t, retired)
	}
	unlock, err := Lock(context.Background(), root)
	require.NoError(t, err)
	require.NoError(t, RetireBatch(root, ids))
	unlock()
	for _, id := range ids {
		retired, err := Retired(root, id)
		require.NoError(t, err)
		require.True(t, retired)
	}
	require.NoError(t, os.Remove(filepath.Join(root, batchIndex)))
	for _, id := range ids {
		_, err := Retired(root, id)
		require.Error(t, err)
	}
	_, err = GuardCatalog(context.Background(), root, raw)
	require.Error(t, err)
}
