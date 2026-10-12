//go:build e2e

package api

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/persistence"
	dockerprovider "github.com/oslab/sysbox/pkg/provider/docker"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestLocalSSHColdReceiptRecoveryE2E(t *testing.T) {
	helper := os.Getenv("SYSBOX_GUESTTASK_HELPER")
	if helper == "" {
		t.Skip("requires current guest helper and disposable Docker")
	}
	for _, backend := range []string{"file", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cfg := localSSHConfig(t)
			if backend == "sqlite" {
				cfg.State.Backend = "sqlite://" + filepath.Join(cfg.Paths.RunsDir, "canonical.db")
			}
			source := NewServerWithConfig(cfg)
			repo, err := source.actionRepository("lab")
			require.NoError(t, err)
			defer repo.Close()
			st, err := repo.LoadState(ctx, "lab")
			require.NoError(t, err)
			raw, err := exec.CommandContext(ctx, "docker", "run", "-d", "--rm", "--network", "none", "--label", "sysbox.managed=true", "--label", "sysbox.owner_uid="+st.TopologyUID, "--label", "sysbox.topology_uid="+st.TopologyUID, "--label", "sysbox.resource_uid=node", "--label", "sysbox.generation=1", "-v", helper+":/usr/local/bin/sysbox-guest-task:ro", "-v", cfg.Agent.ExecutionGrantPublicKeyFile+":/etc/sysbox/grant-public-key:ro", "alpine:3.22", "sleep", "60").CombinedOutput()
			require.NoError(t, err, string(raw))
			container := strings.TrimSpace(string(raw))
			defer func() {
				raw, err := exec.Command("docker", "rm", "-f", container).CombinedOutput()
				require.NoError(t, err, string(raw))
			}()
			provider, err := dockerprovider.New()
			require.NoError(t, err)
			defer provider.Close()
			registry := driver.DefaultRegistry
			driver.DefaultRegistry = driver.NewRegistry()
			defer func() { driver.DefaultRegistry = registry }()
			require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "docker", Version: "test", Node: provider, NodeState: provider}))
			st.Resources = []state.Resource{{Address: address.Resource("sysbox_node", "web"), ResourceType: "sysbox_node", Driver: "docker", OwnerUID: st.TopologyUID, ResourceUID: "node", Generation: 1, ExternalID: container, Attributes: state.MustAttributes(map[string]any{"desired": map[string]any{"connections": []map[string]any{{"User": "root"}}}})}}
			require.NoError(t, state.NewManagerWithBackend(repo.StateBackend("lab")).Save(st))
			local, err := NewLocalSSHController(ctx, cfg, "lab")
			require.NoError(t, err)
			public, err := os.ReadFile(cfg.Agent.ExecutionGrantPublicKeyFile)
			require.NoError(t, err)
			key, err := ssh.NewPublicKey(ed25519.PublicKey(public))
			require.NoError(t, err)
			mutation := substrate.SSHAccessMutation{ID: "cold-create", Config: substrate.SSHAccessConfig{User: "root", AccessID: "admin", AuthorizedKeys: []string{strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))}}}
			original := local.host.Report
			local.host.Report = func(context.Context, string, controlplane.SignedExecutionStopReceipt, json.RawMessage) error {
				return errors.New("completion link unavailable")
			}
			identity := substrate.OperationIdentity{ActionID: mutation.ID, TopologyUID: st.TopologyUID, OwnerUID: st.TopologyUID, ResourceUID: "node", Generation: 1}
			_, err = local.mutate(ctx, mutation, identity)
			require.ErrorContains(t, err, "completion link unavailable")
			op, err := local.server.apiStore.GetNodeOperation(ctx, mutation.ID)
			require.NoError(t, err)
			require.NotEqual(t, controlplane.NodeOperationStatusDone, op.Status)
			journal := local.host.Root
			var record struct {
				Receipt *controlplane.SignedExecutionStopReceipt `json:"receipt"`
			}
			paths, err := filepath.Glob(filepath.Join(journal, "*.json"))
			require.NoError(t, err)
			require.Len(t, paths, 1)
			bytes, err := os.ReadFile(paths[0])
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(bytes, &record))
			require.NotNil(t, record.Receipt)
			local.host.Report = original
			require.NoError(t, local.Close())
			cold, err := NewLocalSSHController(ctx, cfg, "lab")
			require.NoError(t, err)
			defer cold.Close()
			op, err = cold.server.apiStore.GetNodeOperation(ctx, mutation.ID)
			require.NoError(t, err)
			require.Equal(t, controlplane.NodeOperationStatusDone, op.Status)
			require.Equal(t, record.Receipt.SSHResult, op.SSHResult)
			commands, err := cold.server.apiStore.ListAgentCommands(ctx, cold.agent.ID)
			require.NoError(t, err)
			require.Empty(t, commands, "receipt recovery also retires local delivery")
			grants, err := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: cold.server.apiStore}).ListActiveGrants(ctx)
			require.NoError(t, err)
			require.Empty(t, grants)
			bytes, err = os.ReadFile(paths[0])
			require.NoError(t, err)
			var replay struct {
				Receipt  *controlplane.SignedExecutionStopReceipt `json:"receipt"`
				Reported bool                                     `json:"reported"`
			}
			require.NoError(t, json.Unmarshal(bytes, &replay))
			require.True(t, replay.Reported)
			require.Equal(t, record.Receipt, replay.Receipt)
		})
	}
}
