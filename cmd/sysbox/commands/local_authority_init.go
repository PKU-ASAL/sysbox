package commands

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"golang.org/x/sys/unix"
)

// Publish a fresh authority directory atomically. Existing directories are never
// imported, repaired or overwritten: rotating guest trust is a separate operation.
func initializeLocalAuthority(directory string) (string, error) {
	root, err := filepath.Abs(directory)
	if err != nil {
		return "", err
	}
	if _, err = os.Lstat(root); err == nil {
		return "", fmt.Errorf("authority directory already exists; refusing to replace keys: %s", root)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	parent := filepath.Dir(root)
	if err = hostsupervisor.EnsureOwnedDirectory(parent, 0755); err != nil {
		return "", err
	}
	staging, err := os.MkdirTemp(parent, ".sysbox-authority-")
	if err != nil {
		return "", err
	}
	names := []string{"issuer.key", "issuer.pub", "host.key", "sysbox.json"}
	defer func() {
		for _, name := range names {
			_ = os.Remove(filepath.Join(staging, name))
		}
		_ = os.Remove(staging)
	}()
	public, issuer, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	_, host, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	cfg := config.DefaultServiceConfig()
	cfg.API.Listen = "127.0.0.1:9876"
	cfg.API.Token = rand.Text()
	cfg.Paths.Home = root
	cfg.Paths.RunsDir = filepath.Join(root, "runs")
	cfg.Paths.WorkspacesDir = filepath.Join(root, "workspaces")
	cfg.Paths.Cache = filepath.Join(root, "cache")
	cfg.API.ExecutionGrant.SigningKeyFile = filepath.Join(root, "issuer.key")
	cfg.API.ExecutionGrant.HostPublicKeyFiles = nil
	cfg.Agent.ExecutionGrantPublicKeyFile = filepath.Join(root, "issuer.pub")
	cfg.HostSupervisor.SigningKeyFile = filepath.Join(root, "host.key")
	cfg.HostSupervisor.Root = filepath.Join(root, "host")
	cfg.HostSupervisor.Socket = filepath.Join(root, "host.sock")
	configBytes, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	for i, raw := range [][]byte{issuer, public, host, configBytes} {
		file, err := os.OpenFile(filepath.Join(staging, names[i]), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return "", err
		}
		_, writeErr := file.Write(raw)
		if writeErr == nil {
			writeErr = file.Sync()
		}
		closeErr := file.Close()
		if writeErr != nil {
			return "", writeErr
		}
		if closeErr != nil {
			return "", closeErr
		}
	}
	if err = syncAuthorityDirectory(staging); err != nil {
		return "", err
	}
	if err = unix.Renameat2(unix.AT_FDCWD, staging, unix.AT_FDCWD, root, unix.RENAME_NOREPLACE); err != nil {
		return "", fmt.Errorf("publish local authority without replacement: %w", err)
	}
	if err = syncAuthorityDirectory(parent); err != nil {
		return "", fmt.Errorf("authority published but parent sync failed: %w", err)
	}
	return filepath.Join(root, "sysbox.json"), nil
}

func syncAuthorityDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
