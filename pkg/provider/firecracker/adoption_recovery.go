package firecracker

import (
	"context"
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
