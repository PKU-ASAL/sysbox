//go:build e2e

package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	fcprovider "github.com/oslab/sysbox/pkg/provider/firecracker"
	libvirtprovider "github.com/oslab/sysbox/pkg/provider/libvirt"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

// The outer API acceptance uploads this helper and its private inputs into the
// disposable execution VM. It runs the production coordinator and authority RPC.
func TestGCFenceGuestProbe(t *testing.T) {
	input := os.Getenv("SYSBOX_GC_FENCE_PROBE")
	if input == "" {
		t.Skip("only runs in explicitly scoped disposable execution VM")
	}
	var cfg struct {
		Identity  agent.Identity                    `json:"identity"`
		IssuerKey ed25519.PublicKey                 `json:"issuer_key"`
		HostKey   ed25519.PrivateKey                `json:"host_key"`
		Root      string                            `json:"root"`
		Candidate GCCandidate                       `json:"candidate"`
		Grant     controlplane.SignedExecutionGrant `json:"grant"`
		FenceID   string                            `json:"fence_id"`
		Mode      string                            `json:"mode"`
		Kernel    string                            `json:"kernel"`
		RootFS    string                            `json:"rootfs"`
		Binary    string                            `json:"binary"`
		Output    string                            `json:"output"`
	}
	raw, err := os.ReadFile(input)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &cfg))
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	require.NoError(t, err)
	if cfg.Candidate.Kind == "firecracker" {
		service := config.DefaultServiceConfig()
		service.Providers.Firecracker.Binary = cfg.Binary
		serviceRaw, _ := json.Marshal(service)
		configPath := input + ".config"
		require.NoError(t, os.WriteFile(configPath, serviceRaw, 0600))
		t.Setenv("SYSBOX_CONFIG", configPath)
		provider := fcprovider.New(cfg.Kernel, cfg.Root+"-fc")
		op := substrate.OperationIdentity{ActionID: "create", TopologyUID: cfg.Candidate.TopologyUID, OwnerUID: cfg.Candidate.OwnerUID, ResourceUID: cfg.Candidate.ResourceUID, Generation: cfg.Candidate.Generation}
		ctx := substrate.WithOperationIdentity(context.Background(), op)
		if cfg.Mode == "create-firecracker" {
			h, err := provider.CreateNode(ctx, substrate.NodeSpec{Name: "gc-fence", Image: substrate.ArtifactHandle{ID: cfg.RootFS}, ProviderConfig: &fcprovider.Config{Kernel: cfg.Kernel, AllowDirect: true}})
			require.NoError(t, err)
			require.NoError(t, provider.StartNode(ctx, h))
			raw, err := json.Marshal(map[string]string{"external_id": h.ID})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(cfg.Output, raw, 0600))
			return
		}
		if cfg.Mode == "cleanup-firecracker" {
			require.NoError(t, provider.DeleteManagedExternal(ctx, cfg.Candidate.ExternalID, cfg.Candidate.OwnerUID, cfg.Candidate.ResourceUID, cfg.Candidate.Generation))
			return
		}
	}
	authority := AuthorityClient{Identity: cfg.Identity}
	c := GCCoordinator{Root: cfg.Root, AgentID: cfg.Identity.ID, HostBootID: strings.TrimSpace(string(boot)), IssuerKey: cfg.IssuerKey, HostKey: cfg.HostKey, Authorize: authority.Authorize, AuthorizeFence: authority.AuthorizeGCFence, ValidateInventory: func(context.Context, GCCandidate) error { return nil }, InspectFencedCandidate: func(ctx context.Context, candidate GCCandidate) (string, error) {
		if candidate.Kind == "firecracker" {
			return fcprovider.New(cfg.Kernel, cfg.Root+"-fc").InspectFencedGC(ctx, candidate.ExternalID, candidate.OwnerUID, candidate.ResourceUID, candidate.Generation)
		}
		return libvirtprovider.New(filepath.Join(cfg.Root, "libvirt")).InspectFencedGC(ctx, candidate.ExternalID, candidate.OwnerUID, candidate.ResourceUID, candidate.Generation)
	}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if cfg.Mode == "intent" {
		_, err = c.Delete(ctx, cfg.Candidate, cfg.Grant, func(context.Context) (json.RawMessage, error) {
			return nil, fmt.Errorf("deliberately unknown before provider call")
		})
		require.Error(t, err)
		require.FileExists(t, filepath.Join(cfg.Root, digestHex([]byte(cfg.Candidate.TopologyUID))+".json"))
		return
	}
	if cfg.Mode == "corrupt" {
		require.NoError(t, os.WriteFile(filepath.Join(cfg.Root, digestHex([]byte(cfg.Candidate.TopologyUID))+".json"), []byte("broken"), 0600))
	}
	if cfg.Mode == "missing" {
		require.NoError(t, os.Remove(filepath.Join(cfg.Root, digestHex([]byte(cfg.Candidate.TopologyUID))+".json")))
	}
	proof, err := c.Disposition(ctx, cfg.FenceID)
	require.NoError(t, err)
	require.Equal(t, "present", proof.Presence)
	raw, err = json.Marshal(proof)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cfg.Output, raw, 0600))
}
