package artifact

import (
	"context"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestPortableArtifactSources(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "kernel"), []byte("kernel"), 0600))
	ctx := WithPortableSources(context.Background())
	got, err := ResolveSource(ctx, "kernel", "", root)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(root, "kernel"), got)
	for _, source := range []string{".", "../kernel", "/api-only/kernel", "https://example.com/kernel"} {
		_, err := ResolveSource(ctx, source, "", root)
		require.Error(t, err, source)
	}
	got, err = ResolveSource(ctx, "https://example.com/kernel", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", root)
	require.NoError(t, err)
	require.Equal(t, "https://example.com/kernel", got)
	_, err = ResolveSource(ctx, "https://example.com/kernel", "not-a-digest", root)
	require.Error(t, err)
	require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(root, "escape")))
	_, err = ResolveSource(ctx, "escape", "", root)
	require.Error(t, err)
	got, err = ResolveSource(context.Background(), "/explicit/local/kernel", "", root)
	require.NoError(t, err)
	require.Equal(t, "/explicit/local/kernel", got)
}
