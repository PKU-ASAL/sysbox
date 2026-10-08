package firecracker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/substrate"
)

func TestResolveImagePullsPinnedVMArtifactThenUsesOfflineCache(t *testing.T) {
	cacheRoot := filepath.Join(t.TempDir(), "cache")
	configPath := filepath.Join(t.TempDir(), "sysbox.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(fmt.Sprintf("version: 1\npaths:\n  cache: %q\n", cacheRoot)), 0o600))
	t.Setenv("SYSBOX_CONFIG", configPath)

	content := []byte("vm artifact bytes")
	sum := sha256.Sum256(content)
	pin := hex.EncodeToString(sum[:])
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write(content)
	}))
	sourceURL := server.URL + "/rootfs.ext4"
	localPath := filepath.Join(t.TempDir(), "missing-rootfs.ext4")
	source := substrate.ArtifactSource{
		Kind: substrate.ArtifactRootFS, Source: localPath, ResolvedSource: localPath, PullSource: sourceURL,
		ExpectedDigest: pin, Architecture: "amd64", GuestFamily: substrate.GuestFamilyLinux,
	}

	s := &Substrate{}
	first, err := s.ResolveImage(context.Background(), source)
	require.NoError(t, err)
	require.Equal(t, "sha256:"+pin, first.Identity.Digest)
	require.Equal(t, localPath, first.Identity.Source)
	require.Equal(t, content, mustReadArtifact(t, first.ID))
	require.EqualValues(t, 1, requests.Load())

	server.Close()
	second, err := s.ResolveImage(context.Background(), source)
	require.NoError(t, err)
	require.Equal(t, first.ID, second.ID)
	require.EqualValues(t, 1, requests.Load())
}

func mustReadArtifact(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	return content
}
