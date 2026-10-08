package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVMArtifactPreflightLocalFirst(t *testing.T) {
	localPath := filepath.Join(t.TempDir(), "rootfs.ext4")
	pullURL := "https://example.invalid/rootfs.ext4"
	pin := strings.Repeat("a", 64)

	missing := VMArtifactPreflightCheck("image:app:rootfs", localPath, pullURL, pin)
	require.True(t, missing.OK)
	require.Equal(t, "warning", missing.Severity)
	require.Contains(t, missing.Message, "pull_source")

	require.NoError(t, os.WriteFile(localPath, []byte("rootfs"), 0o644))
	present := VMArtifactPreflightCheck("image:app:rootfs", localPath, pullURL, pin)
	require.True(t, present.OK)
	require.Equal(t, "info", present.Severity)

	invalid := VMArtifactPreflightCheck("image:app:rootfs", localPath, pullURL, "bad")
	require.False(t, invalid.OK)
	require.Equal(t, "error", invalid.Severity)
}
