package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func localSSHConfig(t *testing.T) config.ServiceConfig {
	t.Helper()
	cfg := config.DefaultServiceConfig()
	cfg.Paths.RunsDir = t.TempDir()
	cfg.Paths.WorkspacesDir = t.TempDir()
	require.NoError(t, os.Chmod(cfg.Paths.RunsDir, 0700))
	require.NoError(t, os.Chmod(cfg.Paths.WorkspacesDir, 0700))
	keys := t.TempDir()
	require.NoError(t, os.Chmod(keys, 0700))
	pub, issuer, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	_, host, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	cfg.API.ExecutionGrant.SigningKeyFile = filepath.Join(keys, "issuer")
	cfg.Agent.ExecutionGrantPublicKeyFile = filepath.Join(keys, "issuer.pub")
	cfg.HostSupervisor.SigningKeyFile = filepath.Join(keys, "host")
	require.NoError(t, os.WriteFile(cfg.API.ExecutionGrant.SigningKeyFile, issuer, 0600))
	require.NoError(t, os.WriteFile(cfg.Agent.ExecutionGrantPublicKeyFile, pub, 0600))
	require.NoError(t, os.WriteFile(cfg.HostSupervisor.SigningKeyFile, host, 0600))
	return cfg
}

func TestLocalSSHRejectsStaleGenerationBeforeAdmission(t *testing.T) {
	cfg := localSSHConfig(t)
	writeState(t, cfg.Paths.RunsDir, "lab", &state.State{Version: state.SchemaVersion, Resources: []state.Resource{{Address: address.Resource("sysbox_node", "web"), ResourceType: "sysbox_node", Driver: "unregistered", ResourceUID: "node", Generation: 1, Attributes: state.MustAttributes(map[string]any{"desired": map[string]any{"connections": []map[string]any{{"User": "ubuntu"}}}})}}})
	local, err := NewLocalSSHController(context.Background(), cfg, "lab")
	require.NoError(t, err)
	defer local.Close()
	raw, err := os.ReadFile(cfg.Agent.ExecutionGrantPublicKeyFile)
	require.NoError(t, err)
	key, err := ssh.NewPublicKey(ed25519.PublicKey(raw))
	require.NoError(t, err)
	req := substrate.SSHAccessMutation{ID: "stale", Config: substrate.SSHAccessConfig{User: "ubuntu", AccessID: "admin", AuthorizedKeys: []string{strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))}}}
	_, err = local.mutate(context.Background(), req, substrate.OperationIdentity{ActionID: req.ID, TopologyUID: local.owner, OwnerUID: local.owner, ResourceUID: "node", Generation: 2})
	require.ErrorContains(t, err, "node identity unavailable")
	operations, err := local.server.apiStore.ListNodeOperations(context.Background(), "lab")
	require.NoError(t, err)
	require.Empty(t, operations)
	require.Equal(t, "offline", local.agent.Status)
}

func TestLocalSSHConstructionDoesNotRecoverUnrelatedConsoles(t *testing.T) {
	cfg := localSSHConfig(t)
	server := NewServerWithConfig(cfg)
	session, err := server.consoles.Create(context.Background(), "other-topology", "node", "remote-agent", controlplane.ConsoleRequest{})
	require.NoError(t, err)
	// Even a later constructor failure must not mutate unrelated sessions.
	controller, _ := NewLocalSSHController(context.Background(), cfg, "lab")
	if controller != nil {
		defer controller.Close()
	}
	stored, err := server.apiStore.GetConsoleSession(context.Background(), session.ID)
	require.NoError(t, err)
	require.Equal(t, controlplane.ConsoleSessionStatusQueued, stored.Status)
}

func TestLocalSSHAuthorityRequiresEnrolledKeys(t *testing.T) {
	for _, fault := range []string{"missing-issuer", "missing-host", "wrong-public", "permissions"} {
		t.Run(fault, func(t *testing.T) {
			cfg := localSSHConfig(t)
			switch fault {
			case "missing-issuer":
				cfg.API.ExecutionGrant.SigningKeyFile = ""
			case "missing-host":
				cfg.HostSupervisor.SigningKeyFile = ""
			case "wrong-public":
				pub, _, err := ed25519.GenerateKey(rand.Reader)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(cfg.Agent.ExecutionGrantPublicKeyFile, pub, 0600))
			case "permissions":
				require.NoError(t, os.Chmod(cfg.HostSupervisor.SigningKeyFile, 0644))
			}
			controller, err := NewLocalSSHController(context.Background(), cfg, "lab")
			require.Error(t, err)
			require.Nil(t, controller)
		})
	}
}

func TestLocalInventorySkipsOrdinaryFilesAtWildcardDirectoryLevel(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "field.sysbox.hcl"), []byte("config"), 0600))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "lab", "sessions"), 0700))
	expected := filepath.Join(root, "lab", "sessions", "session.json")
	require.NoError(t, os.WriteFile(expected, []byte("{}"), 0600))
	files, err := localRecordPaths(filepath.Join(root, "*", "sessions", "*.json"))
	require.NoError(t, err)
	require.Equal(t, []string{expected}, files)
	_, err = localRecordPaths(filepath.Join(root, "field.sysbox.hcl", "*.json"))
	require.Error(t, err, "explicit non-directory remains invalid")
}
