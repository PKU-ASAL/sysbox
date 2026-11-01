package hostexec

import (
	"context"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/runtime"
)

func (b *Environment) RecoveryActionRepository(ctx context.Context, proof hostsupervisor.RunRecoveryRequest, run controlplane.Run) (runtime.ActionRepository, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	repo, err := b.Repository(run.Topology)
	if err != nil {
		return nil, err
	}
	return &controlstore.RecoveryRepository{ActionRepository: repo, Store: b.Store, Proof: proof, Run: run}, nil
}
