package docker

import (
	"context"
	"fmt"
	"github.com/oslab/sysbox/pkg/substrate"
)

// Docker Engine exposes no durable per-request operation identity or fencing
// primitive. Resource inspect/absence cannot prove an interrupted daemon call
// stopped, so recovery remains blocked until a provider-specific receipt exists.
func (s *Substrate) ProveQuiescence(ctx context.Context, handle substrate.NodeHandle, operationID string, identity substrate.OperationIdentity) (substrate.QuiescenceProof, error) {
	if err := ctx.Err(); err != nil {
		return substrate.QuiescenceProof{}, err
	}
	return substrate.QuiescenceProof{}, fmt.Errorf("docker operation %q lacks durable daemon fencing: %w", operationID, substrate.ErrQuiescenceUnsupported)
}
