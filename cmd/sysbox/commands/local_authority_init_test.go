package commands

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/stretchr/testify/require"
)

func TestLocalAuthorityInitializationNeverReplacesKeys(t *testing.T) {
	parent := t.TempDir()
	require.NoError(t, os.Chmod(parent, 0700))
	root := filepath.Join(parent, "authority")
	path, err := initializeLocalAuthority(root)
	require.NoError(t, err)
	cfg, err := config.LoadServiceConfig(path)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1:9876", cfg.API.Listen)
	require.NotEmpty(t, cfg.API.Token)
	private, err := os.ReadFile(cfg.API.ExecutionGrant.SigningKeyFile)
	require.NoError(t, err)
	require.Len(t, private, ed25519.PrivateKeySize)
	public, err := os.ReadFile(cfg.Agent.ExecutionGrantPublicKeyFile)
	require.NoError(t, err)
	require.Equal(t, ed25519.PrivateKey(private).Public().(ed25519.PublicKey), ed25519.PublicKey(public))
	host, err := os.ReadFile(cfg.HostSupervisor.SigningKeyFile)
	require.NoError(t, err)
	require.Len(t, host, ed25519.PrivateKeySize)
	require.NotEqual(t, private, host)
	hostPublic, err := os.ReadFile(filepath.Join(root, "host.pub"))
	require.NoError(t, err)
	require.Equal(t, ed25519.PrivateKey(host).Public().(ed25519.PublicKey), ed25519.PublicKey(hostPublic))
	for _, file := range []string{path, cfg.API.ExecutionGrant.SigningKeyFile, cfg.Agent.ExecutionGrantPublicKeyFile, cfg.HostSupervisor.SigningKeyFile} {
		info, err := os.Stat(file)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	}
	_, err = initializeLocalAuthority(root)
	require.Error(t, err)
	again, err := os.ReadFile(cfg.API.ExecutionGrant.SigningKeyFile)
	require.NoError(t, err)
	require.Equal(t, private, again)
}

func TestLocalAuthorityInitializationRejectsExistingAndSymlinkDirectories(t *testing.T) {
	parent := t.TempDir()
	existing := filepath.Join(parent, "existing")
	require.NoError(t, os.Mkdir(existing, 0700))
	_, err := initializeLocalAuthority(existing)
	require.Error(t, err)
	link := filepath.Join(parent, "link")
	require.NoError(t, os.Symlink(existing, link))
	_, err = initializeLocalAuthority(link)
	require.Error(t, err)
	entries, err := os.ReadDir(existing)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestLocalAuthorityConcurrentInitializationPublishesOneKeySet(t *testing.T) {
	parent := t.TempDir()
	require.NoError(t, os.Chmod(parent, 0700))
	root := filepath.Join(parent, "authority")
	var group sync.WaitGroup
	outcomes := make(chan error, 8)
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() { defer group.Done(); _, err := initializeLocalAuthority(root); outcomes <- err }()
	}
	group.Wait()
	close(outcomes)
	successes := 0
	for err := range outcomes {
		if err == nil {
			successes++
		}
	}
	require.Equal(t, 1, successes)
	cfg, err := config.LoadServiceConfig(filepath.Join(root, "sysbox.json"))
	require.NoError(t, err)
	private, err := os.ReadFile(cfg.API.ExecutionGrant.SigningKeyFile)
	require.NoError(t, err)
	public, err := os.ReadFile(cfg.Agent.ExecutionGrantPublicKeyFile)
	require.NoError(t, err)
	require.Equal(t, ed25519.PrivateKey(private).Public().(ed25519.PublicKey), ed25519.PublicKey(public))
	entries, err := os.ReadDir(parent)
	require.NoError(t, err)
	require.Len(t, entries, 1)
}
