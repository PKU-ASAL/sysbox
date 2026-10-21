//go:build e2e

package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/controlplane"
	libvirtprovider "github.com/oslab/sysbox/pkg/provider/libvirt"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"os"
	"strings"
	"testing"
)

func TestAdoptionFenceGuestProbe(t *testing.T) {
	input := os.Getenv("SYSBOX_ADOPTION_FENCE_PROBE")
	if input == "" {
		t.Skip("disposable execution VM only")
	}
	var cfg struct {
		Identity                    agent.Identity
		Issuer                      ed25519.PublicKey
		Host                        ed25519.PrivateKey
		Root, Mode, Output, FenceID string
		Adoption                    controlplane.AdoptionRequest
		Grant                       controlplane.SignedExecutionGrant
	}
	raw, err := os.ReadFile(input)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &cfg))
	ctx := context.Background()
	provider := libvirtprovider.New(cfg.Root + "-provider")
	_, err = provider.HostFenceScope(ctx)
	require.NoError(t, err)
	save := func(v any) {
		raw, err := json.Marshal(v)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(cfg.Output, raw, 0600))
	}
	if cfg.Mode == "inspect" {
		e, err := provider.InspectAdoption(ctx, cfg.Adoption.ExternalID, substrate.NodeSpec{})
		require.NoError(t, err)
		require.Nil(t, e.ManagedIdentity)
		save(e)
		return
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	require.NoError(t, err)
	authority := AuthorityClient{Identity: cfg.Identity}
	c := AdoptionCoordinator{Root: cfg.Root, IssuerKey: cfg.Issuer, HostKey: cfg.Host, AgentID: cfg.Identity.ID, HostBootID: strings.TrimSpace(string(boot)), Authorize: authority.Authorize, AuthorizeFence: authority.AuthorizeAdoptionFence, Report: authority.ReportNodeOperation}
	identity := substrate.OperationIdentity{ActionID: cfg.Adoption.ID, OwnerUID: cfg.Adoption.TopologyUID, TopologyUID: cfg.Adoption.TopologyUID, ResourceUID: cfg.Adoption.ResourceUID, Generation: cfg.Adoption.Generation}
	scoped := substrate.WithOperationIdentity(ctx, identity)
	if cfg.Mode == "unknown" {
		_, err = c.Apply(ctx, cfg.Adoption, cfg.Grant, func(context.Context) (json.RawMessage, error) {
			_, err := provider.ApplyAdoption(scoped, cfg.Adoption.Evidence)
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("injected lost return after actual ownership write")
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "injected lost return", "must reach real provider ownership write")
		_, err = provider.InspectAdoptionRecovery(scoped, cfg.Adoption)
		require.NoError(t, err)
		return
	}
	c.InspectFencedAdoption = func(context.Context, controlplane.AdoptionRequest) (substrate.NodeObservation, json.RawMessage, error) {
		h, err := provider.InspectAdoptionRecovery(scoped, cfg.Adoption)
		if err != nil {
			return substrate.NodeObservation{}, nil, err
		}
		obs, err := provider.ObserveNode(scoped, h)
		if err != nil {
			return obs, nil, err
		}
		private, err := provider.MarshalProviderState(h)
		return obs, private, err
	}
	proof, err := c.Disposition(ctx, cfg.FenceID)
	require.NoError(t, err)
	save(proof)
}
