//go:build e2e

package commands

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestLocalCLIManagedSSHApplyRotateDestroyE2E(t *testing.T) {
	for _, backend := range []string{"file", "sqlite"} {
		t.Run(backend, func(t *testing.T) { testLocalCLIManagedSSH(t, backend) })
	}
}

func testLocalCLIManagedSSH(t *testing.T, backend string) {
	binary, helper := os.Getenv("SYSBOX_CLI_BINARY"), os.Getenv("SYSBOX_GUESTTASK_HELPER")
	if binary == "" || helper == "" {
		t.Skip("requires current SYSBOX_CLI_BINARY, SYSBOX_GUESTTASK_HELPER and disposable Docker")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	statePath := filepath.Join(root, "lab", "state.json")
	backendURL := statePath
	if backend == "sqlite" {
		backendURL = "sqlite://" + filepath.Join(root, "canonical.db") + "?topology=lab"
	}
	hclPath := filepath.Join(root, "field.sysbox.hcl")
	authorityRoot := filepath.Join(root, "authority")
	bootstrap := exec.CommandContext(ctx, binary, "--state", statePath, "init", "--authority-dir", authorityRoot)
	output, err := bootstrap.CombinedOutput()
	require.NoError(t, err, string(output))
	serviceConfig := filepath.Join(authorityRoot, "sysbox.json")
	cfg, err := config.LoadServiceConfig(serviceConfig)
	require.NoError(t, err)
	public, err := os.ReadFile(cfg.Agent.ExecutionGrantPublicKeyFile)
	require.NoError(t, err)
	pubkey, err := ssh.NewPublicKey(ed25519.PublicKey(public))
	require.NoError(t, err)
	hcl := func(key string) string {
		return fmt.Sprintf(`resource "sysbox_image" "base" {
 substrate = "docker"
 source = "alpine:3.22"
 kind = "oci"
 architecture = "amd64"
 guest_family = "linux"
}
resource "sysbox_node" "web" {
 substrate = "docker"
 image = sysbox_image.base.id
 connection {
  type = "docker"
  user = "root"
 }
 provider "docker" {
  entrypoint = ["sleep"]
  command = ["infinity"]
  binds = [%q,%q]
 }
}
resource "sysbox_ssh_access" "admin" {
 node = sysbox_node.web.id
 authorized_keys = [%q]
}
`, helper+":/usr/local/bin/sysbox-guest-task:ro", cfg.Agent.ExecutionGrantPublicKeyFile+":/etc/sysbox/grant-public-key:ro", key)
	}
	require.NoError(t, os.WriteFile(hclPath, []byte(hcl(strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pubkey))))), 0600))
	run := func(args ...string) {
		flags := []string{"--state", statePath, "-f", hclPath, "--auto-approve"}
		if backend == "sqlite" {
			flags = append(flags, "--backend", backendURL)
		}
		command := exec.CommandContext(ctx, binary, append(flags, args...)...)
		command.Env = append(os.Environ(), "SYSBOX_CONFIG="+serviceConfig)
		output, err := command.CombinedOutput()
		require.NoError(t, err, string(output))
	}
	repo, err := persistence.OpenConfigured(ctx, backendURL, statePath)
	require.NoError(t, err)
	defer repo.Close()
	initial, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		output, err := exec.CommandContext(cleanup, "docker", "ps", "-aq", "--filter", "label=sysbox.owner_uid="+initial.TopologyUID).Output()
		if err != nil {
			t.Error(err)
			return
		}
		for _, id := range strings.Fields(string(output)) {
			raw, err := exec.CommandContext(cleanup, "docker", "rm", "-f", id).CombinedOutput()
			require.NoError(t, err, string(raw))
		}
	})
	run("apply")
	first, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	node := first.FindResource(address.Resource("sysbox_node", "web"))
	require.NotNil(t, node)
	nodeID := node.ExternalID
	access := first.FindResource(address.Resource("sysbox_ssh_access", "admin"))
	require.NotNil(t, access)
	require.Equal(t, 2, access.SchemaVersion)
	rotated, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	pubkey, err = ssh.NewPublicKey(rotated)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(hclPath, []byte(hcl(strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pubkey))))), 0600))
	run("apply", "--target", "sysbox_ssh_access.admin")
	second, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	require.Equal(t, nodeID, second.FindResource(address.Resource("sysbox_node", "web")).ExternalID)
	keys, err := exec.CommandContext(ctx, "docker", "exec", nodeID, "/usr/local/bin/sysbox-guest-task", "--ssh-authorized-keys", "root").CombinedOutput()
	require.NoError(t, err, string(keys))
	require.Equal(t, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pubkey))), strings.TrimSpace(string(keys)), "read guest keys independently after targeted rotation")
	run("reset", "--target", "sysbox_node.web")
	reset, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	replacement := reset.FindResource(address.Resource("sysbox_node", "web"))
	require.NotNil(t, replacement)
	require.Greater(t, replacement.Generation, node.Generation)
	require.NotEqual(t, nodeID, replacement.ExternalID)
	nodeID = replacement.ExternalID
	keys, err = exec.CommandContext(ctx, "docker", "exec", nodeID, "/usr/local/bin/sysbox-guest-task", "--ssh-authorized-keys", "root").CombinedOutput()
	require.NoError(t, err, string(keys))
	require.Equal(t, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pubkey))), strings.TrimSpace(string(keys)), "reset must recreate the rotated keys on the new generation")
	require.NoError(t, os.Remove(hclPath))
	run("destroy")
	final, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	require.Empty(t, final.Resources)
	_, err = exec.CommandContext(ctx, "docker", "inspect", nodeID).CombinedOutput()
	require.Error(t, err)
	remaining, err := exec.CommandContext(ctx, "docker", "ps", "-aq", "--filter", "label=sysbox.owner_uid="+initial.TopologyUID).Output()
	require.NoError(t, err)
	require.Empty(t, strings.TrimSpace(string(remaining)))
	t.Log("independent CLI processes created, rotated, reset and destroyed managed SSH without HCL; new guest retained the rotated keys")
}
