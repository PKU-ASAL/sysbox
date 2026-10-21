package libvirt

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
)

// InspectAdoptionRecovery uses the immutable domain UUID, never a logical name.
// Fresh XML supplies the post-transfer fingerprint and preserved recovery state.
func (s *Substrate) InspectAdoptionRecovery(ctx context.Context, r controlplane.AdoptionRequest) (substrate.NodeHandle, error) {
	evidence, err := s.InspectAdoption(ctx, r.ExternalID, substrate.NodeSpec{})
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	return s.ObserveAdoption(ctx, evidence)
}
