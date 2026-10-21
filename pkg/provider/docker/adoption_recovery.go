package docker

import (
	"context"
	"fmt"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
)

func (s *Substrate) InspectAdoptionRecovery(ctx context.Context, r controlplane.AdoptionRequest) (substrate.NodeHandle, error) {
	id, ok := substrate.OperationIdentityFromContext(ctx)
	if !ok {
		return substrate.NodeHandle{}, fmt.Errorf("recovery identity required")
	}
	ins, err := s.cli.ContainerInspect(ctx, r.ExternalID)
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	if ins.ID != r.ExternalID {
		return substrate.NodeHandle{}, fmt.Errorf("immutable container identity mismatch")
	}
	labels, err := s.managedOwnershipLabels(ctx, ins)
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	if err = verifyLabels(labels, operationLabels(id)); err != nil {
		return substrate.NodeHandle{}, err
	}
	return s.adoptHandle(ctx, ins, id), nil
}
