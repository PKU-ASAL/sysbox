package commands

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/persistence"
	dockerprovider "github.com/oslab/sysbox/pkg/provider/docker"
	firecrackerprovider "github.com/oslab/sysbox/pkg/provider/firecracker"
	libvirtprovider "github.com/oslab/sysbox/pkg/provider/libvirt"
	"github.com/oslab/sysbox/pkg/runtime"
)

// The RPC server owns the coordinator/gate. This callback only dispatches an
// already-authorized provider effect and cannot reenter the grant boundary.
// Clients are opened lazily so a Docker configuration error cannot disable GC
// observation/recovery or providers that do not use Docker.
func newSupervisorGCEffect(cfg config.ServiceConfig) func(context.Context, hostsupervisor.GCCandidate) (json.RawMessage, error) {
	return func(ctx context.Context, candidate hostsupervisor.GCCandidate) (json.RawMessage, error) {
		switch candidate.Kind {
		case "container", "network":
			docker, err := dockerprovider.New()
			if err != nil {
				return nil, err
			}
			defer docker.Close()
			if candidate.Kind == "container" {
				err = docker.DeleteManagedExternal(ctx, candidate.ExternalID, candidate.OwnerUID, candidate.ResourceUID, candidate.Generation)
			} else {
				err = docker.DeleteManagedNetworkExternal(ctx, candidate.ExternalID, candidate.OwnerUID, candidate.ResourceUID, candidate.Generation)
			}
			if err != nil {
				return nil, err
			}
		case "domain":
			lv := libvirtprovider.New(cfg.Providers.Libvirt.Workdir)
			if err := lv.DeleteManagedExternal(ctx, candidate.ExternalID, candidate.OwnerUID, candidate.ResourceUID, candidate.Generation); err != nil {
				return nil, err
			}
		case "firecracker":
			fc := firecrackerprovider.New(cfg.Providers.Firecracker.Kernel, cfg.Providers.Firecracker.Workdir)
			if err := fc.DeleteManagedExternal(ctx, candidate.ExternalID, candidate.OwnerUID, candidate.ResourceUID, candidate.Generation); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("host supervisor GC provider kind %q is not configured", candidate.Kind)
		}
		return json.RawMessage(`{"deleted":true}`), nil
	}
}

// The host must observe the reservation in its own canonical repository. An
// API with an independent state copy cannot authorize deletion of host resources.
func supervisorGCReferenceValidator(repository func(string) (runtime.ActionRepository, error)) func(context.Context, hostsupervisor.TargetRef, hostsupervisor.GCCandidate, controlplane.SignedExecutionGrant) error {
	return func(ctx context.Context, ref hostsupervisor.TargetRef, candidate hostsupervisor.GCCandidate, grant controlplane.SignedExecutionGrant) error {
		actions, err := repository(ref.Topology)
		if err != nil {
			return err
		}
		repo, ok := actions.(*persistence.Repository)
		if !ok {
			return fmt.Errorf("canonical GC reservation repository unavailable")
		}
		reservation, err := repo.LoadGCReservation(ctx, ref.Topology, grant.Grant.CommandID)
		if err != nil {
			return err
		}
		if reservation.AgentID != grant.Grant.AgentID || reservation.Candidate != candidate || reservation.ReceiptHash != "" {
			return persistence.ErrConflict
		}
		digest, err := candidate.Digest()
		if err != nil {
			return err
		}
		if digest != grant.Grant.Digest {
			return persistence.ErrConflict
		}
		return nil
	}
}
