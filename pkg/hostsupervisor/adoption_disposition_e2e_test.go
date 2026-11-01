//go:build e2e

package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	fcprovider "github.com/oslab/sysbox/pkg/provider/firecracker"
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
		SourceIdentity              substrate.OperationIdentity
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
	var provider interface {
		InspectAdoption(context.Context, string, substrate.NodeSpec) (substrate.AdoptionEvidence, error)
		ApplyAdoption(context.Context, substrate.AdoptionEvidence) (substrate.NodeHandle, error)
		InspectAdoptionRecovery(context.Context, controlplane.AdoptionRequest) (substrate.NodeHandle, error)
		ObserveNode(context.Context, substrate.NodeHandle) (substrate.NodeObservation, error)
		MarshalProviderState(substrate.NodeHandle) (json.RawMessage, error)
	}
	var fc *fcprovider.Substrate
	if cfg.Adoption.Provider == "firecracker" {
		service := config.DefaultServiceConfig()
		service.Providers.Firecracker.Binary = "/var/tmp/gc-firecracker"
		serviceRaw, _ := json.Marshal(service)
		path := input + ".config"
		require.NoError(t, os.WriteFile(path, serviceRaw, 0600))
		t.Setenv("SYSBOX_CONFIG", path)
		fc = fcprovider.New("/var/tmp/gc-vmlinux", cfg.Root+"-provider")
		provider = fc
	} else {
		lv := libvirtprovider.New(cfg.Root + "-provider")
		_, err = lv.HostFenceScope(ctx)
		require.NoError(t, err)
		provider = lv
	}
	identity := substrate.OperationIdentity{ActionID: cfg.Adoption.ID, OwnerUID: cfg.Adoption.TopologyUID, TopologyUID: cfg.Adoption.TopologyUID, ResourceUID: cfg.Adoption.ResourceUID, Generation: cfg.Adoption.Generation}
	scoped := substrate.WithOperationIdentity(ctx, identity)
	save := func(v any) {
		raw, err := json.Marshal(v)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(cfg.Output, raw, 0600))
	}
	if cfg.Mode == "cleanup-source" {
		sourceCtx := substrate.WithOperationIdentity(ctx, cfg.SourceIdentity)
		m, err := fc.InspectAdoptionRecovery(sourceCtx, controlplane.AdoptionRequest{ExternalID: cfg.Adoption.ExternalID})
		require.NoError(t, err)
		require.NoError(t, fc.DestroyNode(sourceCtx, m))
		return
	}
	if cfg.Mode == "create-firecracker" {
		createCtx := scoped
		if cfg.SourceIdentity.OwnerUID != "" {
			createCtx = substrate.WithOperationIdentity(ctx, cfg.SourceIdentity)
		}
		h, err := fc.CreateNode(createCtx, substrate.NodeSpec{Name: "adoption-recovery", Image: substrate.ArtifactHandle{ID: "/var/tmp/gc-rootfs.ext4"}, ProviderConfig: &fcprovider.Config{Kernel: "/var/tmp/gc-vmlinux"}})
		require.NoError(t, err)
		if cfg.SourceIdentity.OwnerUID == "" {
			require.NoError(t, fc.StartNode(createCtx, h))
		}
		obs, err := fc.ObserveNode(createCtx, h)
		require.NoError(t, err)
		if cfg.SourceIdentity.OwnerUID == "" {
			require.True(t, obs.Running)
		} else {
			require.False(t, obs.Running)
		}
		evidence, err := fc.InspectAdoption(createCtx, h.ID, substrate.NodeSpec{})
		require.NoError(t, err)
		save(evidence)
		return
	}
	if cfg.Mode == "verify-restart-cleanup" {
		h, err := fc.InspectAdoptionRecovery(scoped, cfg.Adoption)
		require.NoError(t, err)
		require.NoError(t, fc.StartNode(scoped, h))
		obs, err := fc.ObserveNode(scoped, h)
		require.NoError(t, err)
		require.True(t, obs.Running)
		require.NoError(t, fc.DestroyNode(scoped, h))
		return
	}
	if cfg.Mode == "inspect" {
		e, err := provider.InspectAdoption(ctx, cfg.Adoption.ExternalID, substrate.NodeSpec{})
		require.NoError(t, err)
		if cfg.SourceIdentity.OwnerUID == "" {
			require.Nil(t, e.ManagedIdentity)
		} else {
			require.Equal(t, &cfg.SourceIdentity, e.ManagedIdentity)
		}
		save(e)
		return
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	require.NoError(t, err)
	authority := AuthorityClient{Identity: cfg.Identity}
	c := AdoptionCoordinator{Root: cfg.Root, IssuerKey: cfg.Issuer, HostKey: cfg.Host, AgentID: cfg.Identity.ID, HostBootID: strings.TrimSpace(string(boot)), Authorize: authority.Authorize, AuthorizeFence: authority.AuthorizeAdoptionFence, Report: authority.ReportNodeOperation}
	if cfg.Mode == "unknown" || cfg.Mode == "unknown-cancel" {
		_, err = c.Apply(ctx, cfg.Adoption, cfg.Grant, func(context.Context) (json.RawMessage, error) {
			if cfg.Mode == "unknown-cancel" {
				return nil, fmt.Errorf("injected lost return before ownership write")
			}
			_, err := provider.ApplyAdoption(scoped, cfg.Adoption.Evidence)
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("injected lost return after actual ownership write")
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "injected lost return", "must reach real provider ownership write")
		if cfg.Mode != "unknown-cancel" {
			_, err = provider.InspectAdoptionRecovery(scoped, cfg.Adoption)
			require.NoError(t, err)
		}
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
	if fc != nil {
		c.RecoverFencedAdoption = func(ctx context.Context, p controlplane.AdoptionDispositionProof, authorize func(context.Context) error) (substrate.NodeObservation, json.RawMessage, error) {
			h, err := fc.RecoverFencedAdoption(scoped, p, authorize)
			if err != nil {
				return substrate.NodeObservation{}, nil, err
			}
			obs, err := fc.ObserveNode(scoped, h)
			if err != nil {
				return obs, nil, err
			}
			raw, err := fc.MarshalProviderState(h)
			return obs, raw, err
		}
	}
	var proof controlplane.AdoptionDispositionProof
	if cfg.Mode == "cancel" {
		c.InspectAdoptionCancellation = func(ctx context.Context, p controlplane.AdoptionDispositionProof) (substrate.NodeObservation, error) {
			if fc != nil {
				return fc.InspectAdoptionCancellation(ctx, p)
			}
			return libvirtprovider.New(cfg.Root+"-provider").InspectAdoptionCancellation(ctx, p.Adoption)
		}
		proof, err = c.CancelDisposition(ctx, cfg.FenceID)
	} else {
		proof, err = c.Disposition(ctx, cfg.FenceID)
	}
	require.NoError(t, err)
	save(proof)
}
