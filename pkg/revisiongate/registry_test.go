package revisiongate

import (
	"context"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIndependentRegistryRejectsLateReferencesAcrossConsumers(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	registry := filepath.Join(root, "registry")
	a := filepath.Join(root, "a")
	b := filepath.Join(root, "b")
	require.NoError(t, Enroll(ctx, a, registry))
	require.NoError(t, Enroll(ctx, b, registry))
	id := "sha256:" + strings.Repeat("b", 64)
	require.NoError(t, os.WriteFile(filepath.Join(registry, id+".json.retired"), []byte("proof"), 0600))
	for _, path := range []string{filepath.Join(a, "lab", "run.json"), filepath.Join(b, "lab", "run.json"), filepath.Join(registry, id+".json")} {
		_, err := Guard(ctx, path, []byte(`{"revision":"`+id+`"}`))
		require.Error(t, err)
	}
	require.Error(t, Enroll(ctx, a, filepath.Join(root, "other")))
}

func TestEnrollmentNeverForgetsExistingConsumers(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	registry := filepath.Join(root, "registry")
	require.NoError(t, Enroll(ctx, a, registry))
	require.NoError(t, Enroll(ctx, b, registry))
	require.NoError(t, os.Remove(filepath.Join(registry, "consumers.json")))
	require.Error(t, Enroll(ctx, a, registry), "restart cannot reconstruct missing manifest with only itself")
	original := filepath.Join(root, "original", "global-revisions")
	require.NoError(t, os.MkdirAll(original, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(original, "old.json"), []byte("{}"), 0600))
	require.Error(t, Enroll(ctx, filepath.Join(root, "new"), original), "joining existing unmanifested registry cannot forget its original owner")
}
