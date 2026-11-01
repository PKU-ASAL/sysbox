package firecracker

import (
	"context"
	"fmt"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
)

func (s *Substrate) InspectAdoptionRecovery(ctx context.Context, r controlplane.AdoptionRequest) (substrate.NodeHandle, error) {
	evidence, err := s.InspectAdoption(ctx, r.ExternalID, substrate.NodeSpec{})
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	return s.ObserveAdoption(ctx, evidence)
}

// Cancellation never normalizes a manifest or touches any process. Source-owned
// manifests must be structurally verified and belong to the stopped kernel.
func (s *Substrate) InspectAdoptionCancellation(ctx context.Context, p controlplane.AdoptionDispositionProof) (substrate.NodeObservation, error) {
	r := p.Adoption
	zero := substrate.NodeObservation{}
	boot, err := currentBootID()
	if err != nil {
		return zero, err
	}
	if p.Fence.NewBootID != boot || controlplane.VerifyHostFenceReceipt(p.Request, p.Fence) != nil {
		return zero, fmt.Errorf("verified current host fence required")
	}
	source := substrate.OperationIdentity{OwnerUID: r.TopologyUID, TopologyUID: r.TopologyUID, ResourceUID: r.ResourceUID, Generation: r.Generation}
	if r.Transfer != nil {
		source = r.Transfer.Plan.Source
	}
	present, err := s.InspectFencedGC(ctx, r.ExternalID, source.OwnerUID, source.ResourceUID, source.Generation)
	if err != nil {
		return zero, err
	}
	if present == "absent" {
		return substrate.NodeObservation{ExternalID: r.ExternalID}, nil
	}
	m, _, err := s.findAdoptionManifest(ctx, r.ExternalID)
	if err != nil {
		return zero, err
	}
	// The caller's target context does not authorize source mutations. This
	// acquisition is read-only and binds the actual source identity explicitly.
	_, current, release, err := s.lockedManifest(substrate.WithOperationIdentity(ctx, source), manifestHandle(&m))
	if err != nil {
		return zero, err
	}
	defer release()
	if current.Phase == "attaching" {
		return zero, fmt.Errorf("unresolved attachment")
	}
	if current.Process.BootID != "" && current.Process.BootID != p.Fence.OldBootID {
		return zero, fmt.Errorf("source process outside fenced kernel")
	}
	if residue, err := hasLaunchResidue(current.Handle.ConfigPath); err != nil {
		return zero, err
	} else if residue {
		return zero, fmt.Errorf("source generation has current process")
	}
	observed := substrate.NodeObservation{Exists: true, ExternalID: r.ExternalID, OwnerUID: current.OwnerUID, ResourceUID: current.ResourceUID, Generation: current.Generation}
	if _, err = controlplane.AdoptionCancellationResolution(r, observed); err != nil {
		return zero, err
	}
	return observed, nil
}
